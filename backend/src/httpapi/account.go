package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// sessionResponse 是注册/登录成功的响应。token 只在这里出现一次，服务端只保存其摘要。
type sessionResponse struct {
	Token     string         `json:"token"`
	TokenType string         `json:"token_type"`
	ExpiresAt time.Time      `json:"expires_at"`
	Account   domain.Account `json:"account"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

func invalidArgument(message string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_argument", Message: message}
}

// audit 写一条审计日志。只记录动作和标识，不记录密码、token、手机号、邮箱等内容。
func (s *Server) audit(r *http.Request, action, accountID string, attrs ...any) {
	base := []any{
		"request_id", requestIDFromContext(r.Context()),
		"action", action,
		"account_id", accountID,
		"client_ip", clientIP(r, settingsFromContext(r.Context())),
	}
	s.logger.InfoContext(r.Context(), "audit", append(base, attrs...)...)
}

// ---------- 注册 / 登录 / 登出 ----------

type registerRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	username := strings.TrimSpace(req.Username)
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}
	if err := validateRegister(username, req.Password, displayName); err != nil {
		writeError(w, err)
		return
	}
	hash, err := s.passwords.hash(req.Password)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "hash password failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}

	var resp sessionResponse
	err = s.store.WithTx(r.Context(), func(ctx context.Context) error {
		acc, err := s.store.CreateAccount(ctx, store.NewAccount{
			Username: username, PasswordHash: hash, DisplayName: displayName, Role: domain.RoleUser,
		})
		if err != nil {
			return err
		}
		resp, err = s.newSession(ctx, acc)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, ErrUsernameExists)
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "register failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	s.audit(r, "account.register", resp.Account.AccountID)
	writeJSON(w, http.StatusCreated, resp)
}

func validateRegister(username, password, displayName string) error {
	if username == "" || password == "" {
		return invalidArgument("账号和密码不能为空")
	}
	if n := utf8.RuneCountInString(username); n < 3 || n > 32 {
		return invalidArgument("账号长度需要在 3 到 32 个字符之间")
	}
	for _, c := range username {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return invalidArgument("账号只能包含字母、数字、下划线和短横线")
		}
	}
	if n := utf8.RuneCountInString(password); n < 8 || n > 64 {
		return invalidArgument("密码长度需要在 8 到 64 个字符之间")
	}
	if len(password) > 72 { // bcrypt 只接受 72 字节以内
		return invalidArgument("密码过长，请减少中文等多字节字符")
	}
	return validateDisplayName(displayName, 32)
}

func validateDisplayName(name string, max int) error {
	if utf8.RuneCountInString(name) > max {
		return invalidArgument("昵称最多 " + strconv.Itoa(max) + " 个字符")
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return invalidArgument("昵称不能包含控制字符")
		}
	}
	return nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin：用户不存在、已注销和密码错误返回同一个 401；密码正确但账户 inactive/risk 时返回 403 说明原因。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		writeError(w, invalidArgument("账号和密码不能为空"))
		return
	}
	limit := settingsFromContext(r.Context()).LoginAttemptsPerMin
	if ok, retry := s.loginLimiter.allow("login:"+strings.ToLower(username), limit, time.Now()); !ok {
		setRetryAfter(w, retry)
		writeError(w, ErrLoginRateLimited)
		return
	}

	acc, hash, err := s.store.GetAccountByUsername(r.Context(), username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.ErrorContext(r.Context(), "login lookup failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	if err != nil {
		s.passwords.burn(req.Password)
		s.audit(r, "auth.login_failed", "", "reason", "unknown_username")
		writeError(w, ErrInvalidCredential)
		return
	}
	if !s.passwords.verify(hash, req.Password) {
		s.audit(r, "auth.login_failed", acc.AccountID, "reason", "bad_password")
		writeError(w, ErrInvalidCredential)
		return
	}
	switch acc.Status {
	case domain.StatusActive:
	case domain.StatusRisk:
		s.audit(r, "auth.login_denied", acc.AccountID, "status", string(acc.Status))
		writeError(w, ErrLoginRisk)
		return
	default:
		s.audit(r, "auth.login_denied", acc.AccountID, "status", string(acc.Status))
		writeError(w, ErrLoginInactive)
		return
	}

	if s.passwords.needsRehash(hash) {
		// 升级失败不影响本次登录，下次登录会再试。
		if upgraded, err := s.passwords.hash(req.Password); err == nil {
			if err := s.store.UpdatePasswordHash(r.Context(), acc.AccountID, upgraded); err == nil {
				s.audit(r, "account.password_rehashed", acc.AccountID)
			} else {
				s.logger.WarnContext(r.Context(), "password rehash failed", "request_id", requestIDFromContext(r.Context()), "error", err)
			}
		}
	}
	resp, err := s.newSession(r.Context(), acc)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "create token failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	s.audit(r, "auth.login", acc.AccountID)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) newSession(ctx context.Context, acc domain.Account) (sessionResponse, error) {
	token, expires, err := s.store.CreateAuthToken(ctx, acc.AccountID, settingsFromContext(ctx).AuthTokenTTL)
	if err != nil {
		return sessionResponse{}, err
	}
	return sessionResponse{Token: token, TokenType: "Bearer", ExpiresAt: expires, Account: acc}, nil
}

// handleLogout 撤销本次请求使用的 token；同一账户的其他会话不受影响。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	if err := s.store.DeleteAuthToken(r.Context(), bearerToken(r)); err != nil {
		s.logger.ErrorContext(r.Context(), "logout failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	s.audit(r, "auth.logout", acc.AccountID)
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	writeJSON(w, http.StatusOK, acc)
}

// ---------- 个人资料 ----------

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	writeJSON(w, http.StatusOK, acc)
}

// profileRequest：display_name 为空时取 nickname（兼容上游客户端）；空串和缺省都表示不修改。
type profileRequest struct {
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
	AvatarURL   string `json:"avatar_url"`
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var req profileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		name = strings.TrimSpace(req.Nickname)
	}
	avatar := strings.TrimSpace(req.AvatarURL)
	if name == "" && avatar == "" {
		writeError(w, invalidArgument("昵称或头像不能为空"))
		return
	}
	var in store.ProfileUpdate
	if name != "" {
		if err := validateDisplayName(name, 24); err != nil {
			writeError(w, err)
			return
		}
		in.DisplayName = &name
	}
	if avatar != "" {
		if !s.avatars.ownedBy(avatar, acc.AccountID) {
			writeError(w, invalidArgument("头像地址无效，请先上传头像"))
			return
		}
		in.AvatarURL = &avatar
	}
	updated, err := s.store.UpdateAccountProfile(r.Context(), acc.AccountID, in)
	if s.writeAccountUpdate(w, r, updated, err) {
		s.audit(r, "account.profile_updated", acc.AccountID, "display_name", in.DisplayName != nil, "avatar", in.AvatarURL != nil)
	}
}

// contactRequest：字段缺省表示不修改，空串表示清空。
type contactRequest struct {
	Phone *string `json:"phone"`
	Email *string `json:"email"`
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var req contactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.Phone == nil && req.Email == nil {
		writeError(w, invalidArgument("请填写手机号或邮箱"))
		return
	}
	var in store.ContactUpdate
	if req.Phone != nil {
		phone := strings.TrimSpace(*req.Phone)
		in.Phone = &phone
	}
	if req.Email != nil {
		email := strings.TrimSpace(*req.Email)
		in.Email = &email
	}
	if err := validateContact(in); err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.store.UpdateAccountContact(r.Context(), acc.AccountID, in)
	if s.writeAccountUpdate(w, r, updated, err) {
		s.audit(r, "account.contact_updated", acc.AccountID, "phone", req.Phone != nil, "email", req.Email != nil)
	}
}

func validateContact(in store.ContactUpdate) error {
	var phone, email string
	if in.Phone != nil {
		phone = *in.Phone
	}
	if in.Email != nil {
		email = *in.Email
	}
	if utf8.RuneCountInString(phone) > 32 || utf8.RuneCountInString(email) > 128 {
		return invalidArgument("手机号或邮箱过长")
	}
	for _, c := range phone {
		if !(c >= '0' && c <= '9' || c == '+' || c == '-' || c == ' ') {
			return invalidArgument("手机号格式不正确")
		}
	}
	if email != "" {
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Address != email || addr.Name != "" {
			return invalidArgument("邮箱格式不正确")
		}
	}
	return nil
}

// writeAccountUpdate 输出更新结果，成功返回 true。账户在更新前被注销时按登录失效处理。
func (s *Server) writeAccountUpdate(w http.ResponseWriter, r *http.Request, acc domain.Account, err error) bool {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, acc)
		return true
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrTokenInvalid)
	default:
		s.logger.ErrorContext(r.Context(), "account update failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
	}
	return false
}

// handleDeleteAccount 注销账户：软删并撤销全部 token。用户名不可再注册。
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	err := s.store.SoftDeleteAccount(r.Context(), acc.AccountID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrTokenInvalid)
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "delete account failed", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	s.audit(r, "account.deleted", acc.AccountID)
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}
