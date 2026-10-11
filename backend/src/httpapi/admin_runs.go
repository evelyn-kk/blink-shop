package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// 管理端调试观测：全部账号的导购运行列表和单次运行的轨迹时间线。
// 用户提问和回答在返回前经 agent.RedactPII 脱敏（轨迹 metadata 写入时已经脱敏）；列表只带提问摘要，不带回答和结构化块。

const runQueryPreviewRunes = 80

type adminRunRowView struct {
	RunID          string           `json:"run_id"`
	SessionID      string           `json:"session_id"`
	AccountID      string           `json:"account_id"`
	Username       string           `json:"username"`
	TraceID        string           `json:"trace_id"`
	Status         domain.RunStatus `json:"status"`
	Query          string           `json:"query"`
	Attachments    int              `json:"attachments"`
	PromptVersions map[string]int   `json:"prompt_versions"`
	ErrorCode      string           `json:"error_code"`
	ElapsedMS      *int64           `json:"elapsed_ms"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type adminRunSummaryView struct {
	EventCount  int      `json:"event_count"`
	FailedCount int      `json:"failed_count"`
	EventsMS    int64    `json:"events_ms"`
	Models      []string `json:"models"`
	Tools       []string `json:"tools"`
	ChunkIDs    []string `json:"chunk_ids"`
	ProductIDs  []string `json:"product_ids"`
}

type adminRunDetailView struct {
	adminRunRowView
	Content   string              `json:"content"`
	Blocks    []map[string]any    `json:"blocks"`
	Followups []string            `json:"followups"`
	Summary   adminRunSummaryView `json:"summary"`
	Items     []traceEventView    `json:"items"`
}

func terminalRun(s domain.RunStatus) bool {
	return s == domain.RunCompleted || s == domain.RunFailed || s == domain.RunCancelled
}

func toAdminRunRow(a store.AdminRun, preview bool) adminRunRowView {
	r := a.Run
	q := agent.RedactPII(a.Query)
	if preview && utf8.RuneCountInString(q) > runQueryPreviewRunes {
		q = string([]rune(q)[:runQueryPreviewRunes]) + "…"
	}
	v := adminRunRowView{RunID: r.RunID, SessionID: r.SessionID, AccountID: r.AccountID, Username: a.Username, TraceID: r.TraceID, Status: r.Status,
		Query: q, Attachments: a.Attachments, PromptVersions: r.PromptVersions, ErrorCode: r.ErrorCode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if v.PromptVersions == nil {
		v.PromptVersions = map[string]int{}
	}
	if terminalRun(r.Status) {
		ms := r.UpdatedAt.Sub(r.CreatedAt).Milliseconds()
		v.ElapsedMS = &ms
	}
	return v
}

// handleListAdminRuns：GET /admin/agent/runs?status=&account_id=&page=&page_size=
func (s *Server) handleListAdminRuns(w http.ResponseWriter, r *http.Request) {
	status, err := readEnum[domain.RunStatus](r, "status", string(domain.RunQueued), string(domain.RunRunning), string(domain.RunCompleted),
		string(domain.RunFailed), string(domain.RunCancelled))
	if err != nil {
		writeError(w, err)
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if len(accountID) > 64 {
		writeError(w, fieldError("account_id", "account_id 最多 64 个字符"))
		return
	}
	q := store.RunQuery{Status: status, AccountID: accountID, Page: readPage(r)}
	items, total, err := s.store.ListRunsForAdmin(r.Context(), q)
	if err != nil {
		s.storeFailed(w, r, "list admin runs", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(items, q.Page, total, func(a store.AdminRun) adminRunRowView { return toAdminRunRow(a, true) }))
}

// handleGetAdminRun：GET /admin/agent/runs/{id}，运行结果、按写入顺序的轨迹和汇总。
func (s *Server) handleGetAdminRun(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetRunForAdmin(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, ErrRunNotFound)
			return
		}
		s.storeFailed(w, r, "get admin run", err)
		return
	}
	events, err := s.store.ListTraceEvents(r.Context(), a.Run.AccountID, a.Run.RunID)
	if err != nil {
		s.storeFailed(w, r, "list trace", err)
		return
	}
	run := a.Run
	d := adminRunDetailView{adminRunRowView: toAdminRunRow(a, false), Content: agent.RedactPII(run.Content), Blocks: run.Blocks, Followups: run.Followups,
		Items: make([]traceEventView, 0, len(events))}
	if d.Blocks == nil {
		d.Blocks = []map[string]any{}
	}
	if d.Followups == nil {
		d.Followups = []string{}
	}
	d.Summary = summarizeTrace(events)
	for _, e := range events {
		meta := e.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		d.Items = append(d.Items, traceEventView{TraceEventID: e.TraceEventID, Stage: e.Stage, EventType: e.EventType, Model: e.Model,
			Status: e.Status, DurationMS: e.DurationMS, Error: e.Error, Metadata: meta, CreatedAt: e.CreatedAt})
	}
	writeJSON(w, http.StatusOK, d)
}

// summarizeTrace 汇总一次运行的轨迹：事件数、失败数（error / failed）、事件耗时合计、用到的模型和工具、检索到的 chunk 与商品。
func summarizeTrace(events []domain.AgentTraceEvent) adminRunSummaryView {
	sum := adminRunSummaryView{EventCount: len(events)}
	models, tools, chunks, products := strSet{}, strSet{}, strSet{}, strSet{}
	for _, e := range events {
		if e.Status == "error" || e.Status == "failed" {
			sum.FailedCount++
		}
		sum.EventsMS += e.DurationMS
		models.add(e.Model)
		if e.Stage == "tool" {
			tools.add(e.EventType)
		}
		if e.Stage == "retrieval" {
			chunks.addAll(e.Metadata["chunk_ids"])
			products.addAll(e.Metadata["product_ids"])
		}
	}
	sum.Models, sum.Tools, sum.ChunkIDs, sum.ProductIDs = models.list(), tools.list(), chunks.list(), products.list()
	return sum
}

type strSet map[string]struct{}

func (s strSet) add(v string) {
	if v = strings.TrimSpace(v); v != "" {
		s[v] = struct{}{}
	}
}

// addAll 接受 []string 或从 JSON 读回的 []any。
func (s strSet) addAll(v any) {
	switch xs := v.(type) {
	case []string:
		for _, x := range xs {
			s.add(x)
		}
	case []any:
		for _, x := range xs {
			if str, ok := x.(string); ok {
				s.add(str)
			}
		}
	}
}

func (s strSet) list() []string {
	out := make([]string, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
