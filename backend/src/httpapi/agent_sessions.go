package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

var (
	ErrSessionNotFound = &APIError{Status: http.StatusNotFound, Code: "session_not_found", Message: "会话不存在"}
	ErrRunNotFound     = &APIError{Status: http.StatusNotFound, Code: "run_not_found", Message: "运行记录不存在"}
	ErrRunFinished     = &APIError{Status: http.StatusConflict, Code: "run_finished", Message: "这次回答已经结束，不能取消"}
)

const (
	defaultSessionTitle = "AI 导购"
	maxSummaryRunes     = 500
	firstSummaryRunes   = 120
)

// ---------- 视图 ----------

type sessionView struct {
	SessionID     string     `json:"session_id"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	MessageCount  int        `json:"message_count"`
	LastMessageAt *time.Time `json:"last_message_at"`
	Pinned        bool       `json:"pinned"`
	PinnedAt      *time.Time `json:"pinned_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func toSessionView(s domain.ChatSession) sessionView {
	return sessionView{SessionID: s.SessionID, Title: s.Title, Summary: s.Summary, MessageCount: s.MessageCount,
		LastMessageAt: s.LastMessageAt, Pinned: s.PinnedAt != nil, PinnedAt: s.PinnedAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

type runView struct {
	RunID     string           `json:"run_id"`
	SessionID string           `json:"session_id"`
	MessageID string           `json:"message_id"`
	TraceID   string           `json:"trace_id"`
	Status    domain.RunStatus `json:"status"`
	Content   string           `json:"content"`
	Blocks    []map[string]any `json:"blocks"`
	Followups []string         `json:"followups"`
	ErrorCode string           `json:"error_code"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func toRunView(r domain.AgentRun) runView {
	v := runView{RunID: r.RunID, SessionID: r.SessionID, MessageID: r.MessageID, TraceID: r.TraceID, Status: r.Status, Content: r.Content,
		Blocks: r.Blocks, Followups: r.Followups, ErrorCode: r.ErrorCode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if v.Blocks == nil {
		v.Blocks = []map[string]any{}
	}
	if v.Followups == nil {
		v.Followups = []string{}
	}
	return v
}

type attachmentView struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
}

type messageView struct {
	MessageID       string           `json:"message_id"`
	ClientMessageID string           `json:"client_message_id"`
	Content         string           `json:"content"`
	Attachments     []attachmentView `json:"attachments"`
	CreatedAt       time.Time        `json:"created_at"`
	Run             *runView         `json:"run"`
}

func toMessageView(t store.ChatTurn) messageView {
	v := messageView{MessageID: t.Message.MessageID, ClientMessageID: t.Message.ClientMessageID, Content: t.Message.Content,
		CreatedAt: t.Message.CreatedAt, Attachments: []attachmentView{}}
	for _, a := range t.Message.Attachments {
		v.Attachments = append(v.Attachments, attachmentView{FileID: a.FileID, MimeType: a.MimeType})
	}
	if t.Run != nil {
		rv := toRunView(*t.Run)
		v.Run = &rv
	}
	return v
}

type sessionDetailView struct {
	Session  sessionView   `json:"session"`
	Messages []messageView `json:"messages"`
}

// ---------- 标题与摘要（规则，不调用模型） ----------

var titleFillers = []string{"帮我", "给我", "我想", "我要", "想要", "请问", "请", "推荐", "一下", "一个", "一款", "有没有", "有什么", "什么", "吗", "呢", "吧", "啊"}

var titleHints = []string{"手机", "耳机", "音箱", "键盘", "鼠标", "台灯", "电脑", "笔记本", "平板", "手表", "相机", "充电"}

// deriveSessionTitle 从第一条消息生成会话标题：命中品类词时为“××咨询”，否则取去掉语气词和标点后的前 8 个字。
func deriveSessionTitle(content string) string {
	for _, h := range titleHints {
		if strings.Contains(content, h) {
			return h + "咨询"
		}
	}
	s := content
	for _, f := range titleFillers {
		s = strings.ReplaceAll(s, f, "")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return "导购咨询"
	}
	return truncateRunes(s, 8)
}

// applyFirstMessage 在第一条消息时补上标题和摘要（用户改过的标题、已有的摘要不动）。
func applyFirstMessage(cs *domain.ChatSession, content string) {
	if cs.Title == "" || cs.Title == defaultSessionTitle {
		cs.Title = deriveSessionTitle(content)
	}
	if cs.Summary == "" {
		cs.Summary = truncateRunes("用户咨询："+content, firstSummaryRunes)
	}
}

// ---------- 处理器 ----------

func (s *Server) sessionError(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrSessionNotFound)
		return
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		writeError(w, apiErr)
		return
	}
	s.storeFailed(w, r, op, err)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	kw := strings.TrimSpace(r.URL.Query().Get("keyword"))
	if utf8.RuneCountInString(kw) > 64 {
		writeError(w, fieldError("keyword", "关键词最多 64 个字"))
		return
	}
	page := readPage(r)
	items, total, err := s.store.ListChatSessions(r.Context(), store.ChatSessionQuery{AccountID: acc.AccountID, Keyword: kw, Page: page})
	if err != nil {
		s.storeFailed(w, r, "list sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, page, total, toSessionView))
}

type sessionInput struct {
	Title   *string `json:"title"`
	Summary *string `json:"summary"`
}

func validTitle(t string) error {
	if n := utf8.RuneCountInString(t); n == 0 || n > store.MaxSessionTitleRunes {
		return fieldError("title", "标题需要 1–128 个字")
	}
	return nil
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in sessionInput
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, err)
			return
		}
	}
	title := defaultSessionTitle
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
		if err := validTitle(title); err != nil {
			writeError(w, err)
			return
		}
	}
	cs, err := s.store.CreateChatSession(r.Context(), domain.ChatSession{AccountID: acc.AccountID, Title: title})
	if err != nil {
		s.storeFailed(w, r, "create session", err)
		return
	}
	writeJSON(w, http.StatusCreated, toSessionView(cs))
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id := r.PathValue("id")
	cs, err := s.store.GetChatSession(r.Context(), acc.AccountID, id)
	if err != nil {
		s.sessionError(w, r, "get session", err)
		return
	}
	turns, err := s.store.ListChatMessages(r.Context(), acc.AccountID, id)
	if err != nil {
		s.sessionError(w, r, "list messages", err)
		return
	}
	out := sessionDetailView{Session: toSessionView(cs), Messages: make([]messageView, 0, len(turns))}
	for _, t := range turns {
		out.Messages = append(out.Messages, toMessageView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	var in sessionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Title == nil && in.Summary == nil {
		writeError(w, invalidArgument("请提供 title 或 summary"))
		return
	}
	cs, err := s.store.UpdateChatSession(r.Context(), acc.AccountID, r.PathValue("id"), func(cs *domain.ChatSession) error {
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if err := validTitle(t); err != nil {
				return err
			}
			cs.Title = t
		}
		if in.Summary != nil {
			sum := strings.TrimSpace(*in.Summary)
			if utf8.RuneCountInString(sum) > maxSummaryRunes {
				return fieldError("summary", "摘要最多 500 个字")
			}
			cs.Summary = sum
		}
		return nil
	})
	if err != nil {
		s.sessionError(w, r, "update session", err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionView(cs))
}

type sessionDeletedResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	id := r.PathValue("id")
	now := s.now().UTC()
	if _, err := s.store.UpdateChatSession(r.Context(), acc.AccountID, id, func(cs *domain.ChatSession) error {
		cs.DeletedAt = &now
		return nil
	}); err != nil {
		s.sessionError(w, r, "delete session", err)
		return
	}
	s.audit(r, "agent_session.deleted", acc.AccountID, "session_id", id)
	writeJSON(w, http.StatusOK, sessionDeletedResponse{SessionID: id, Status: "deleted"})
}

// handleSessionAction 处理 POST /agent/sessions/{id}:pin 和 {id}:summarize。
func (s *Server) handleSessionAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(r.PathValue("action"), ":")
	if !ok || id == "" {
		writeError(w, ErrNotFound)
		return
	}
	switch action {
	case "pin":
		s.pinSession(w, r, id)
	case "summarize":
		s.summarizeSession(w, r, id)
	default:
		writeError(w, ErrNotFound)
	}
}

type pinInput struct {
	Pinned *bool `json:"pinned"`
}

func (s *Server) pinSession(w http.ResponseWriter, r *http.Request, id string) {
	acc, _ := accountFromContext(r.Context())
	var in pinInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Pinned == nil {
		writeError(w, fieldError("pinned", "请提供 pinned（true 置顶，false 取消置顶）"))
		return
	}
	now := s.now().UTC()
	cs, err := s.store.UpdateChatSession(r.Context(), acc.AccountID, id, func(cs *domain.ChatSession) error {
		switch {
		case *in.Pinned && cs.PinnedAt == nil:
			cs.PinnedAt = &now
		case !*in.Pinned:
			cs.PinnedAt = nil
		}
		return nil
	})
	if err != nil {
		s.sessionError(w, r, "pin session", err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionView(cs))
}

// summarizeSession 用规则生成摘要：前三条用户消息用“；”连接（最多 500 字）；标题仍是默认值时按第一条消息生成。
func (s *Server) summarizeSession(w http.ResponseWriter, r *http.Request, id string) {
	acc, _ := accountFromContext(r.Context())
	turns, err := s.store.ListChatMessages(r.Context(), acc.AccountID, id)
	if err != nil {
		s.sessionError(w, r, "list messages", err)
		return
	}
	var parts []string
	for _, t := range turns {
		if c := strings.TrimSpace(t.Message.Content); c != "" && len(parts) < 3 {
			parts = append(parts, c)
		}
	}
	cs, err := s.store.UpdateChatSession(r.Context(), acc.AccountID, id, func(cs *domain.ChatSession) error {
		if len(parts) > 0 {
			cs.Summary = truncateRunes(strings.Join(parts, "；"), maxSummaryRunes)
			if cs.Title == defaultSessionTitle {
				cs.Title = deriveSessionTitle(parts[0])
			}
		}
		return nil
	})
	if err != nil {
		s.sessionError(w, r, "summarize session", err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionView(cs))
}
