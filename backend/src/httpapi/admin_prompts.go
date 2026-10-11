package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// PromptMirror 把发布后的 Prompt 推送到配置中心（Nacos）；nil 表示不推送（发布记录 target=database）。
type PromptMirror interface {
	Target(key string) string
	Publish(ctx context.Context, key string, version int, content string) error
}

const promptMirrorTimeout = 5 * time.Second

var (
	ErrPromptNotFound      = &APIError{Status: http.StatusNotFound, Code: "prompt_not_found", Message: "没有这个 Prompt"}
	ErrPromptVersionNF     = &APIError{Status: http.StatusNotFound, Code: "prompt_version_not_found", Message: "没有这个版本", Field: "version"}
	ErrPromptActiveChanged = &APIError{Status: http.StatusConflict, Code: "prompt_active_changed", Message: "生效版本已被其他人修改，请刷新后确认再操作"}
	ErrPromptState         = &APIError{Status: http.StatusConflict, Code: "prompt_state_conflict", Message: "这个版本当前的状态不能执行该操作", Field: "version"}
)

type promptVersionView struct {
	PromptID    string     `json:"prompt_id"`
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Content     string     `json:"content,omitempty"`
	Runes       int        `json:"runes"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	PublishedAt *time.Time `json:"published_at"`
}

type promptSummaryView struct {
	PromptKey     string             `json:"prompt_key"`
	Title         string             `json:"title"`
	Description   string             `json:"description"`
	Placeholders  []string           `json:"placeholders"`
	ActiveVersion int                `json:"active_version"`
	ActiveSource  string             `json:"active_source"` // builtin / published
	Draft         *promptVersionView `json:"draft"`
	VersionCount  int                `json:"version_count"`
}

type promptDetailView struct {
	promptSummaryView
	BuiltinContent string                            `json:"builtin_content"`
	Versions       []promptVersionView               `json:"versions"`
	PublishRecords []domain.AgentPromptPublishRecord `json:"publish_records"`
}

func toVersionView(p domain.AgentPrompt, withContent bool) promptVersionView {
	v := promptVersionView{PromptID: p.PromptID, Version: p.Version, Status: p.Status, Title: p.Title, Description: p.Description,
		Runes: utf8.RuneCountInString(p.Content), CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, PublishedAt: p.PublishedAt}
	if withContent {
		v.Content = p.Content
	}
	return v
}

func summarize(spec agent.PromptSpec, versions []domain.AgentPrompt) promptSummaryView {
	out := promptSummaryView{PromptKey: spec.Key, Title: spec.Title, Description: spec.Description, Placeholders: append([]string{}, spec.Placeholders...),
		ActiveSource: "builtin", VersionCount: len(versions)}
	for _, p := range versions {
		switch p.Status {
		case domain.PromptActive:
			out.ActiveVersion, out.ActiveSource = p.Version, "published"
		case domain.PromptDraft:
			d := toVersionView(p, false)
			out.Draft = &d
		}
	}
	return out
}

func (s *Server) handleListAdminPrompts(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListPrompts(r.Context(), "")
	if err != nil {
		s.storeFailed(w, r, "list prompts", err)
		return
	}
	byKey := map[string][]domain.AgentPrompt{}
	for _, p := range all {
		byKey[p.PromptKey] = append(byKey[p.PromptKey], p)
	}
	items := []promptSummaryView{}
	for _, spec := range agent.PromptSpecs() {
		items = append(items, summarize(spec, byKey[spec.Key]))
	}
	records, _, err := s.store.ListPromptPublishRecords(r.Context(), "", store.Page{Page: 1, PageSize: 20})
	if err != nil {
		s.storeFailed(w, r, "list prompt publish records", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "publish_records": records})
}

func (s *Server) promptDetail(ctx context.Context, spec agent.PromptSpec) (promptDetailView, error) {
	versions, err := s.store.ListPrompts(ctx, spec.Key)
	if err != nil {
		return promptDetailView{}, err
	}
	records, _, err := s.store.ListPromptPublishRecords(ctx, spec.Key, store.Page{Page: 1, PageSize: 50})
	if err != nil {
		return promptDetailView{}, err
	}
	d := promptDetailView{promptSummaryView: summarize(spec, versions), BuiltinContent: spec.Builtin, Versions: []promptVersionView{}, PublishRecords: records}
	for _, p := range versions {
		d.Versions = append(d.Versions, toVersionView(p, true))
	}
	return d, nil
}

func (s *Server) promptSpec(w http.ResponseWriter, r *http.Request) (agent.PromptSpec, bool) {
	spec, ok := agent.LookupPromptSpec(r.PathValue("key"))
	if !ok {
		writeError(w, ErrPromptNotFound)
	}
	return spec, ok
}

func (s *Server) handleGetAdminPrompt(w http.ResponseWriter, r *http.Request) {
	spec, ok := s.promptSpec(w, r)
	if !ok {
		return
	}
	d, err := s.promptDetail(r.Context(), spec)
	if err != nil {
		s.storeFailed(w, r, "get prompt", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type promptDraftInput struct {
	Content     *string `json:"content"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
}

// handleSavePromptDraft 保存草稿：每个 Prompt 同时只有一个草稿（已有就覆盖，否则新建下一个版本号）。草稿不影响运行，发布后才生效。
func (s *Server) handleSavePromptDraft(w http.ResponseWriter, r *http.Request) {
	spec, ok := s.promptSpec(w, r)
	if !ok {
		return
	}
	acc, _ := accountFromContext(r.Context())
	var in promptDraftInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Content == nil {
		writeError(w, fieldError("content", "请提供 content"))
		return
	}
	content := strings.TrimSpace(*in.Content)
	if err := agent.ValidatePrompt(spec.Key, content); err != nil {
		writeError(w, fieldError("content", err.Error()))
		return
	}
	title, desc := strings.TrimSpace(in.Title), strings.TrimSpace(in.Description)
	if title == "" {
		title = spec.Title
	}
	if utf8.RuneCountInString(title) > 64 || utf8.RuneCountInString(desc) > 500 {
		writeError(w, fieldError("title", "标题最多 64 字、说明最多 500 字"))
		return
	}
	var saved domain.AgentPrompt
	err := s.changeWithAudit(r, "prompt.draft_saved", "prompt", spec.Key, "", func(ctx context.Context) (string, string, error) {
		var err error
		saved, err = s.store.SavePromptDraft(ctx, domain.AgentPrompt{PromptKey: spec.Key, Title: title, Content: content, Description: desc, CreatedBy: acc.AccountID})
		return "", fmt.Sprintf("草稿 v%d（%d 字）", saved.Version, utf8.RuneCountInString(content)), err
	})
	if err != nil {
		s.storeFailed(w, r, "save prompt draft", err)
		return
	}
	writeJSON(w, http.StatusOK, toVersionView(saved, true))
}

type promptSwitchInput struct {
	Version         *int   `json:"version"`
	ExpectedVersion *int   `json:"expected_active_version"`
	Reason          string `json:"reason"`
}

func (s *Server) handlePublishPrompt(w http.ResponseWriter, r *http.Request) {
	s.switchPrompt(w, r, domain.PromptActionPublish)
}

func (s *Server) handleRollbackPrompt(w http.ResponseWriter, r *http.Request) {
	s.switchPrompt(w, r, domain.PromptActionRollback)
}

// switchPrompt 发布草稿或回滚到旧版本（version=0 回到内置默认）：调用方必须给出看到的当前生效版本，别人先改了就 409；
// 切换与审计在一个事务里。提交后本实例立即生效，再推送配置中心（失败记 fallback，运行时以数据库为准）。
func (s *Server) switchPrompt(w http.ResponseWriter, r *http.Request, action string) {
	spec, ok := s.promptSpec(w, r)
	if !ok {
		return
	}
	acc, _ := accountFromContext(r.Context())
	var in promptSwitchInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	switch {
	case in.Version == nil || *in.Version < 0 || (action == domain.PromptActionPublish && *in.Version == 0):
		writeError(w, fieldError("version", "请指定要"+map[string]string{"publish": "发布", "rollback": "回滚到"}[action]+"的版本（回滚到内置默认用 0）"))
		return
	case in.ExpectedVersion == nil || *in.ExpectedVersion < 0:
		writeError(w, fieldError("expected_active_version", "请提供当前看到的生效版本（内置默认为 0），防止覆盖别人的发布"))
		return
	case utf8.RuneCountInString(in.Reason) > maxReasonRunes || (action == domain.PromptActionRollback && in.Reason == ""):
		writeError(w, fieldError("reason", fmt.Sprintf("请填写原因（最多 %d 字；回滚必填），会记入发布记录和审计", maxReasonRunes)))
		return
	}
	target := "database"
	if s.promptMirror != nil {
		target = s.promptMirror.Target(spec.Key)
	}
	var active *domain.AgentPrompt
	var rec domain.AgentPromptPublishRecord
	auditAction := "prompt." + action + "ed"
	if action == domain.PromptActionRollback {
		auditAction = "prompt.rolled_back"
	}
	err := s.changeWithAudit(r, auditAction, "prompt", spec.Key, in.Reason, func(ctx context.Context) (string, string, error) {
		var err error
		active, rec, err = s.store.SwitchPrompt(ctx, store.PromptSwitch{Key: spec.Key, Version: *in.Version, ExpectedActive: *in.ExpectedVersion,
			Action: action, By: acc.AccountID, Reason: in.Reason, Target: target})
		return versionLabel(*in.ExpectedVersion), versionLabel(*in.Version), err
	})
	switch {
	case errors.Is(err, store.ErrPromptActiveChanged):
		writeError(w, ErrPromptActiveChanged)
		return
	case errors.Is(err, store.ErrPromptState):
		writeError(w, ErrPromptState)
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, ErrPromptVersionNF)
		return
	case err != nil:
		s.storeFailed(w, r, "switch prompt", err)
		return
	}
	if s.prompts != nil {
		s.prompts.Invalidate(spec.Key)
	}
	rec.Result = s.mirrorPrompt(r, spec, rec, active)
	d, err := s.promptDetail(r.Context(), spec)
	if err != nil {
		s.storeFailed(w, r, "get prompt", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompt": d, "record": rec})
}

// mirrorPrompt 把新的生效内容推送到配置中心并回写发布记录的结果。
func (s *Server) mirrorPrompt(r *http.Request, spec agent.PromptSpec, rec domain.AgentPromptPublishRecord, active *domain.AgentPrompt) string {
	result, detail := domain.PublishSuccess, ""
	if s.promptMirror != nil {
		content := spec.Builtin
		if active != nil {
			content = active.Content
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), promptMirrorTimeout)
		defer cancel()
		if err := s.promptMirror.Publish(ctx, spec.Key, rec.Version, content); err != nil {
			result, detail = domain.PublishFallback, "config_center_unavailable"
			s.logger.WarnContext(r.Context(), "prompt mirror push failed", "request_id", requestIDFromContext(r.Context()), "prompt", spec.Key,
				"version", rec.Version, "error", err)
		}
	}
	if err := s.store.UpdatePromptPublishResult(context.WithoutCancel(r.Context()), rec.RecordID, result, detail); err != nil {
		s.logger.WarnContext(r.Context(), "update prompt publish result failed", "record_id", rec.RecordID, "error", err)
	}
	return result
}

func versionLabel(v int) string {
	if v == 0 {
		return "内置默认"
	}
	return "v" + strconv.Itoa(v)
}
