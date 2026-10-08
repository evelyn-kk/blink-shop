package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// SSE 事件类型（docs/03）：先 message_start，再 0..n 个 thinking / text_delta / block / followups，最后恰好一个 message_done 或 error。
const (
	EventMessageStart = "message_start"
	EventThinking     = "thinking"
	EventTextDelta    = "text_delta"
	EventBlock        = "block"
	EventFollowups    = "followups"
	EventMessageDone  = "message_done"
	EventError        = "error"
)

const (
	maxMessageRunes         = 2000
	DefaultAgentRunTimeout  = 2 * time.Minute
	DefaultSSEHeartbeat     = 15 * time.Second
	replayPollInterval      = 300 * time.Millisecond
	cancelWaitTimeout       = 5 * time.Second
	runPersistTimeout       = 5 * time.Second
	interruptedRunErrorCode = "interrupted"
)

var clientMessageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// 运行被取消的原因（context.Cause）。
var (
	errCancelledByUser = errors.New("cancelled by user")
	errClientGone      = errors.New("client disconnected")
	errRunTimeout      = errors.New("run timeout")
	errServerShutdown  = errors.New("server shutdown")
)

// ---------- SSE 写出 ----------

// sseWriter 串行写 SSE 帧并立即 flush；写失败（客户端断开）后静默丢弃后续内容。
type sseWriter struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	rc     *http.ResponseController
	broken bool
}

func startSSE(w http.ResponseWriter) *sseWriter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 关闭 Nginx 等反向代理的缓冲
	w.WriteHeader(http.StatusOK)
	s := &sseWriter{w: w, rc: http.NewResponseController(w)}
	s.flush()
	return s
}

func (s *sseWriter) flush() {
	if err := s.rc.Flush(); err != nil {
		s.broken = true
	}
}

func (s *sseWriter) event(name string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte(`{}`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		s.broken = true
		return
	}
	s.flush()
}

// comment 写一行注释（心跳），客户端解析时忽略。
func (s *sseWriter) comment(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return
	}
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		s.broken = true
		return
	}
	s.flush()
}

// heartbeat 定期写心跳，直到 stop 被关闭。
func (s *sseWriter) heartbeat(every time.Duration, stop <-chan struct{}) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.comment("ping")
		}
	}
}

// ---------- 事件数据 ----------

type messageStartData struct {
	RunID     string `json:"run_id"`
	MessageID string `json:"message_id"`
	SessionID string `json:"session_id"`
	TraceID   string `json:"trace_id"`
	Replayed  bool   `json:"replayed"`
}

type messageDoneData struct {
	RunID  string           `json:"run_id"`
	Status domain.RunStatus `json:"status"`
}

type errorData struct {
	RunID   string `json:"run_id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// finalError 是运行没有正常完成时给客户端的错误（不含内部细节）。
func finalError(r domain.AgentRun) errorData {
	d := errorData{RunID: r.RunID, Code: r.ErrorCode}
	switch {
	case r.Status == domain.RunCancelled:
		d.Code, d.Message = "cancelled", "已停止生成"
	case r.ErrorCode == "run_timeout":
		d.Message = "回答超时，请稍后重试"
	case r.ErrorCode == "server_shutdown" || r.ErrorCode == interruptedRunErrorCode:
		d.Message = "服务重启中断了这次回答，请重新发送"
	default:
		d.Code, d.Message = "agent_failed", "这次回答没有完成，请稍后重试"
	}
	return d
}

// ---------- 进行中的运行 ----------

type activeRun struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

type runRegistry struct {
	mu   sync.Mutex
	runs map[string]*activeRun
}

func newRunRegistry() *runRegistry { return &runRegistry{runs: map[string]*activeRun{}} }

func (g *runRegistry) add(runID string, a *activeRun) {
	g.mu.Lock()
	g.runs[runID] = a
	g.mu.Unlock()
}

func (g *runRegistry) remove(runID string) {
	g.mu.Lock()
	delete(g.runs, runID)
	g.mu.Unlock()
}

func (g *runRegistry) get(runID string) *activeRun {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runs[runID]
}

func (g *runRegistry) all() []*activeRun {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*activeRun, 0, len(g.runs))
	for _, a := range g.runs {
		out = append(out, a)
	}
	return out
}

// ---------- 运行输出 ----------

// runOutput 实现 agent.Output：推给客户端，同时累积内容用于持久化。
type runOutput struct {
	s         *Server
	sse       *sseWriter
	run       domain.AgentRun
	mu        sync.Mutex
	text      strings.Builder
	blocks    []map[string]any
	followups []string
}

func (o *runOutput) Thinking(step agent.Step) {
	o.sse.event(EventThinking, map[string]any{"step": step})
}

func (o *runOutput) Text(delta string) {
	if delta == "" {
		return
	}
	o.mu.Lock()
	o.text.WriteString(delta)
	o.mu.Unlock()
	o.sse.event(EventTextDelta, map[string]any{"delta": delta})
}

func (o *runOutput) Block(b map[string]any) {
	o.mu.Lock()
	o.blocks = append(o.blocks, b)
	o.mu.Unlock()
	o.sse.event(EventBlock, map[string]any{"block": b})
}

func (o *runOutput) Followups(q []string) {
	o.mu.Lock()
	o.followups = append([]string(nil), q...)
	o.mu.Unlock()
	o.sse.event(EventFollowups, map[string]any{"questions": q})
}

func (o *runOutput) Trace(stage, eventType, status string, d time.Duration, meta map[string]any) {
	o.s.trace(o.run, stage, eventType, status, d, "", meta)
}

// trace 写一条轨迹；写失败只记日志，不影响运行。
func (s *Server) trace(r domain.AgentRun, stage, eventType, status string, d time.Duration, errText string, meta map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), runPersistTimeout)
	defer cancel()
	if _, err := s.store.AppendTraceEvent(ctx, domain.AgentTraceEvent{RunID: r.RunID, TraceID: r.TraceID, AccountID: r.AccountID,
		Stage: stage, EventType: eventType, Status: status, DurationMS: d.Milliseconds(), Error: errText, Metadata: meta}); err != nil {
		s.logger.Error("append trace event failed", "run_id", r.RunID, "error", err)
	}
}

// ---------- 流式端点 ----------

type streamInput struct {
	ClientMessageID string `json:"client_message_id"`
	Content         string `json:"content"`
	Attachments     []struct {
		FileID string `json:"file_id"`
	} `json:"attachments"`
}

// handleSessionSubAction 处理 POST /agent/sessions/{id}/messages:stream。
func (s *Server) handleSessionSubAction(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("action") != "messages:stream" {
		writeError(w, ErrNotFound)
		return
	}
	s.streamMessage(w, r, r.PathValue("id"))
}

func (s *Server) streamMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	acc, _ := accountFromContext(r.Context())
	var in streamInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	msg, apiErr := s.validateStreamInput(r, acc, sessionID, in)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	started, err := s.store.StartAgentRun(r.Context(), msg, func(cs *domain.ChatSession) {
		if cs.MessageCount == 0 {
			applyFirstMessage(cs, msg.Content)
		}
	})
	if err != nil {
		s.sessionError(w, r, "start agent run", err)
		return
	}
	if !started.Created {
		s.replayRun(w, r, started)
		return
	}
	s.executeRun(w, r, started)
}

func (s *Server) validateStreamInput(r *http.Request, acc domain.Account, sessionID string, in streamInput) (domain.UserMessage, *APIError) {
	if !clientMessageIDPattern.MatchString(in.ClientMessageID) {
		return domain.UserMessage{}, fieldError("client_message_id", "client_message_id 必填，1–128 位字母、数字或 . _ : -")
	}
	content := strings.TrimSpace(in.Content)
	if n := utf8.RuneCountInString(content); n == 0 || n > maxMessageRunes {
		return domain.UserMessage{}, fieldError("content", "请输入 1–2000 个字的问题")
	}
	if len(in.Attachments) > store.MaxMessageAttachments {
		return domain.UserMessage{}, fieldError("attachments", "最多 10 个附件")
	}
	msg := domain.UserMessage{AccountID: acc.AccountID, SessionID: sessionID, ClientMessageID: in.ClientMessageID, Content: content,
		Attachments: []domain.Attachment{}}
	for i, a := range in.Attachments {
		field := fmt.Sprintf("attachments[%d].file_id", i)
		if a.FileID == "" {
			return domain.UserMessage{}, fieldError(field, "附件缺少 file_id")
		}
		f, err := s.store.GetStoredFile(r.Context(), a.FileID)
		// 只能引用自己上传的文件；别人的文件与不存在同样处理
		if errors.Is(err, store.ErrNotFound) || (err == nil && f.AccountID != acc.AccountID) {
			return domain.UserMessage{}, fieldError(field, "附件不存在")
		}
		if err != nil {
			return domain.UserMessage{}, ErrInternal
		}
		msg.Attachments = append(msg.Attachments, domain.Attachment{FileID: f.FileID, MimeType: f.MimeType})
	}
	return msg, nil
}

// executeRun 执行新运行并把输出推给客户端。运行的 context 与请求分离，取消原因可区分：
// 用户取消、客户端断开、超时、服务关闭；结束后无论哪种都写回最终状态和已生成的内容。
func (s *Server) executeRun(w http.ResponseWriter, r *http.Request, started store.StartedRun) {
	run := started.Run
	base, cancel := context.WithCancelCause(context.WithoutCancel(r.Context()))
	ctx, cancelTimeout := context.WithTimeoutCause(base, s.runTimeout, errRunTimeout)
	defer cancelTimeout()
	active := &activeRun{cancel: cancel, done: make(chan struct{})}
	s.runs.add(run.RunID, active)
	defer func() {
		s.runs.remove(run.RunID)
		close(active.done)
		cancel(nil)
	}()
	// 客户端断开时取消运行
	stopWatch := context.AfterFunc(r.Context(), func() { cancel(errClientGone) })
	defer stopWatch()

	sse := startSSE(w)
	stopBeat := make(chan struct{})
	go sse.heartbeat(s.heartbeat, stopBeat)
	defer close(stopBeat)
	sse.event(EventMessageStart, messageStartData{RunID: run.RunID, MessageID: started.Message.MessageID, SessionID: run.SessionID,
		TraceID: run.TraceID})
	s.trace(run, "run", "start", "ok", 0, "", map[string]any{"attachments": len(started.Message.Attachments)})

	out := &runOutput{s: s, sse: sse, run: run}
	begin := time.Now()
	runErr := s.runAgent(ctx, agent.Input{AccountID: run.AccountID, SessionID: run.SessionID, RunID: run.RunID, TraceID: run.TraceID,
		Content: started.Message.Content, Attachments: started.Message.Attachments}, out)

	status, code := domain.RunCompleted, ""
	if cause := context.Cause(ctx); ctx.Err() != nil {
		switch {
		case errors.Is(cause, errCancelledByUser), errors.Is(cause, errClientGone):
			status, code = domain.RunCancelled, "cancelled"
		case errors.Is(cause, errRunTimeout):
			status, code = domain.RunFailed, "run_timeout"
		case errors.Is(cause, errServerShutdown):
			status, code = domain.RunFailed, "server_shutdown"
		default:
			status, code = domain.RunFailed, "agent_error"
		}
	} else if runErr != nil {
		status, code = domain.RunFailed, "agent_error"
	}

	out.mu.Lock()
	content, blocks, followups := out.text.String(), out.blocks, out.followups
	out.mu.Unlock()
	pctx, pcancel := context.WithTimeout(context.Background(), runPersistTimeout)
	defer pcancel()
	final, err := s.store.UpdateAgentRun(pctx, run.AccountID, run.RunID, func(x *domain.AgentRun) error {
		x.Status, x.ErrorCode, x.Content, x.Blocks, x.Followups = status, code, content, blocks, followups
		return nil
	})
	if err != nil {
		// 已被别处结束（如另一个实例上的取消）：以库里的状态为准
		s.logger.Warn("persist agent run failed", "run_id", run.RunID, "error", err)
		if got, gerr := s.store.GetAgentRun(pctx, run.AccountID, run.RunID); gerr == nil {
			final = got
		} else {
			final = run
			final.Status, final.ErrorCode = domain.RunFailed, "agent_error"
		}
	}
	errText := ""
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		errText = runErr.Error()
	}
	s.trace(run, "run", "end", string(final.Status), time.Since(begin), errText, map[string]any{"error_code": final.ErrorCode,
		"content_runes": utf8.RuneCountInString(content), "blocks": len(blocks)})
	if final.Status == domain.RunCompleted {
		sse.event(EventMessageDone, messageDoneData{RunID: run.RunID, Status: final.Status})
	} else {
		sse.event(EventError, finalError(final))
	}
}

// runAgent 调用运行器；运行器 panic 时按失败处理，不影响服务。
func (s *Server) runAgent(ctx context.Context, in agent.Input, out agent.Output) (err error) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("agent runner panic", "run_id", in.RunID, "panic", fmt.Sprint(p))
			err = fmt.Errorf("runner panic: %v", p)
		}
	}()
	return s.runner.Run(ctx, in, out)
}

// replayRun 处理重复提交（同一 client_message_id）：不再运行。已结束的直接重放保存的结果；
// 还在进行的（例如前一次连接断了但运行仍在当前进程里）等到结束后重放，期间保持心跳。
func (s *Server) replayRun(w http.ResponseWriter, r *http.Request, started store.StartedRun) {
	run := started.Run
	sse := startSSE(w)
	stopBeat := make(chan struct{})
	go sse.heartbeat(s.heartbeat, stopBeat)
	defer close(stopBeat)
	sse.event(EventMessageStart, messageStartData{RunID: run.RunID, MessageID: started.Message.MessageID, SessionID: run.SessionID,
		TraceID: run.TraceID, Replayed: true})
	for !run.Status.Terminal() {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(replayPollInterval):
		}
		got, err := s.store.GetAgentRun(r.Context(), run.AccountID, run.RunID)
		if err != nil {
			if r.Context().Err() == nil {
				s.logger.Error("replay poll failed", "run_id", run.RunID, "error", err)
				sse.event(EventError, errorData{RunID: run.RunID, Code: "internal_error", Message: "暂时无法获取回答，请稍后重试"})
			}
			return
		}
		run = got
	}
	if run.Content != "" {
		sse.event(EventTextDelta, map[string]any{"delta": run.Content})
	}
	for _, b := range run.Blocks {
		sse.event(EventBlock, map[string]any{"block": b})
	}
	if len(run.Followups) > 0 {
		sse.event(EventFollowups, map[string]any{"questions": run.Followups})
	}
	if run.Status == domain.RunCompleted {
		sse.event(EventMessageDone, messageDoneData{RunID: run.RunID, Status: run.Status})
	} else {
		sse.event(EventError, finalError(run))
	}
}

// ---------- 运行查询与取消 ----------

type traceEventView struct {
	TraceEventID string         `json:"trace_event_id"`
	Stage        string         `json:"stage"`
	EventType    string         `json:"event_type"`
	Model        string         `json:"model"`
	Status       string         `json:"status"`
	DurationMS   int64          `json:"duration_ms"`
	Error        string         `json:"error"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"created_at"`
}

type runTraceResponse struct {
	Run   runView          `json:"run"`
	Items []traceEventView `json:"items"`
}

// handleRunTrace 返回运行的当前状态和轨迹（服务重启后也能查到最终状态）。
func (s *Server) handleRunTrace(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	run, err := s.store.GetAgentRun(r.Context(), acc.AccountID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrRunNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get run", err)
		return
	}
	events, err := s.store.ListTraceEvents(r.Context(), acc.AccountID, run.RunID)
	if err != nil {
		s.storeFailed(w, r, "list trace", err)
		return
	}
	out := runTraceResponse{Run: toRunView(run), Items: make([]traceEventView, 0, len(events))}
	for _, e := range events {
		meta := e.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		out.Items = append(out.Items, traceEventView{TraceEventID: e.TraceEventID, Stage: e.Stage, EventType: e.EventType, Model: e.Model,
			Status: e.Status, DurationMS: e.DurationMS, Error: e.Error, Metadata: meta, CreatedAt: e.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunAction 处理 POST /agent/runs/{id}:cancel。
func (s *Server) handleRunAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(r.PathValue("action"), ":")
	if !ok || id == "" || action != "cancel" {
		writeError(w, ErrNotFound)
		return
	}
	acc, _ := accountFromContext(r.Context())
	run, err := s.store.GetAgentRun(r.Context(), acc.AccountID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, ErrRunNotFound)
		return
	}
	if err != nil {
		s.storeFailed(w, r, "get run", err)
		return
	}
	switch {
	case run.Status == domain.RunCancelled:
		// 重复取消：幂等
	case run.Status.Terminal():
		writeError(w, ErrRunFinished)
		return
	default:
		if a := s.runs.get(id); a != nil {
			// 在本进程里运行：取消 context，等它写完最终状态和已生成的内容
			a.cancel(errCancelledByUser)
			select {
			case <-a.done:
			case <-time.After(cancelWaitTimeout):
			}
		} else if _, err := s.store.UpdateAgentRun(r.Context(), acc.AccountID, id, func(x *domain.AgentRun) error {
			x.Status, x.ErrorCode = domain.RunCancelled, "cancelled"
			return nil
		}); err != nil && !errors.Is(err, store.ErrInvalid) {
			s.storeFailed(w, r, "cancel run", err)
			return
		}
		if run, err = s.store.GetAgentRun(r.Context(), acc.AccountID, id); err != nil {
			s.storeFailed(w, r, "get run", err)
			return
		}
		if run.Status != domain.RunCancelled && run.Status.Terminal() {
			writeError(w, ErrRunFinished) // 取消前已经完成
			return
		}
	}
	s.audit(r, "agent_run.cancelled", acc.AccountID, "run_id", id)
	writeJSON(w, http.StatusOK, toRunView(run))
}

// ---------- 启动与关闭 ----------

// RecoverInterruptedRuns 把 startedAt 之前创建、仍在进行中的运行标为失败（上一个进程被杀或崩溃时留下的）。
func (s *Server) RecoverInterruptedRuns(ctx context.Context, startedAt time.Time) (int, error) {
	n, err := s.store.FailInterruptedRuns(ctx, interruptedRunErrorCode, startedAt)
	if err == nil && n > 0 {
		s.logger.Warn("marked interrupted agent runs as failed", "count", n)
	}
	return n, err
}

// StopAgentRuns 取消本进程所有进行中的运行并等待它们写完最终状态（最多 timeout）。优雅关闭时在关 HTTP 服务前调用。
func (s *Server) StopAgentRuns(timeout time.Duration) {
	deadline := time.After(timeout)
	for _, a := range s.runs.all() {
		a.cancel(errServerShutdown)
	}
	for _, a := range s.runs.all() {
		select {
		case <-a.done:
		case <-deadline:
			return
		}
	}
}
