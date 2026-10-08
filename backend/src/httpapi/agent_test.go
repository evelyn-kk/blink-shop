package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

const agentSessionsPath = "/api/v1/agent/sessions"

// ---------- SSE 测试客户端 ----------

type sseEvent struct {
	Name string
	Data map[string]any
	Raw  string
}

type sseStream struct {
	t      *testing.T
	resp   *http.Response
	events chan sseEvent
	done   chan struct{}
	pings  atomic.Int32
}

// openStream 发起流式请求。非 SSE 响应（预检失败）时返回 nil 和响应。
func openStream(t *testing.T, base, token, sessionID string, body any) (*sseStream, *http.Response) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+agentSessionsPath+"/"+sessionID+"/messages:stream", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, resp
	}
	s := &sseStream{t: t, resp: resp, events: make(chan sseEvent, 256), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(s.events)
		r := bufio.NewReader(resp.Body)
		var name, data string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if data != "" {
					ev := sseEvent{Name: name, Raw: data}
					_ = json.Unmarshal([]byte(data), &ev.Data)
					s.events <- ev
				}
				name, data = "", ""
			case strings.HasPrefix(line, ":"):
				s.pings.Add(1)
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	t.Cleanup(func() { s.close() })
	return s, resp
}

func (s *sseStream) next() sseEvent {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			s.t.Fatal("stream ended")
		}
		return ev
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for event")
	}
	return sseEvent{}
}

// rest 读到流结束，返回剩下的事件。
func (s *sseStream) rest() []sseEvent {
	s.t.Helper()
	var out []sseEvent
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(5 * time.Second):
			s.t.Fatal("stream did not end")
		}
	}
}

func (s *sseStream) close() {
	_ = s.resp.Body.Close()
	<-s.done
}

func names(evs []sseEvent) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Name)
	}
	return out
}

// ---------- 测试运行器 ----------

// scriptRunner 按步骤输出；gate 不为 nil 时在输出第一段文字后等待放行（或运行被取消）。
type scriptRunner struct {
	calls atomic.Int32
	gate  chan struct{}
	err   error
}

func (r *scriptRunner) Run(ctx context.Context, in agent.Input, out agent.Output) error {
	r.calls.Add(1)
	out.Thinking(agent.Step{ID: "understand", Title: "理解问题", Status: agent.StepRunning})
	out.Thinking(agent.Step{ID: "understand", Title: "理解问题", Status: agent.StepDone})
	out.Trace("planner", "rule", "ok", time.Millisecond, map[string]any{"intent": "guide"})
	out.Text("你好，")
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	out.Text("这是回答。")
	out.Block(map[string]any{"type": "action", "label": "去购物车"})
	out.Followups([]string{"还有别的吗？"})
	return r.err
}

type agentTestEnv struct {
	*testServer
	base   string
	token  string
	token2 string
}

func newAgentEnv(t *testing.T, runner agent.Runner) *agentTestEnv {
	t.Helper()
	ts := newTestServer(t, nil, nil, nil)
	ts.runner = runner
	srv := httptest.NewServer(ts.handler)
	t.Cleanup(srv.Close)
	return &agentTestEnv{testServer: ts, base: srv.URL, token: ts.login(t, seed.UserUsername, seed.DevPassword).Token,
		token2: ts.login(t, seed.User2Username, seed.DevPassword).Token}
}

func (e *agentTestEnv) newSession(t *testing.T) sessionView {
	t.Helper()
	rec := e.call(t, http.MethodPost, agentSessionsPath, e.token, map[string]any{})
	expectStatus(t, rec, http.StatusCreated, "")
	return decodeBody[sessionView](t, rec)
}

func (e *agentTestEnv) run(t *testing.T, token, runID string) runView {
	t.Helper()
	rec := e.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", token, nil)
	expectStatus(t, rec, http.StatusOK, "")
	return decodeBody[runTraceResponse](t, rec).Run
}

// waitRun 等到运行进入结束状态。
func (e *agentTestEnv) waitRun(t *testing.T, runID string) runView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := e.run(t, e.token, runID); r.Status.Terminal() {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", runID)
	return runView{}
}

func msgBody(clientID, content string) map[string]any {
	return map[string]any{"client_message_id": clientID, "content": content}
}

// ---------- 测试 ----------

// 事件顺序：message_start → thinking → text_delta → block → followups → message_done；
// 每个事件立即 flush（运行器卡在第一段文字后时，客户端已经能收到它）。
func TestAgentStreamOrderFlushAndPersist(t *testing.T) {
	gate := make(chan struct{})
	runner := &scriptRunner{gate: gate}
	e := newAgentEnv(t, runner)
	cs := e.newSession(t)
	s, resp := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-1", "  推荐一款通勤降噪耳机  "))
	if s == nil {
		t.Fatalf("not a stream: %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Cache-Control") != "no-cache, no-transform" {
		t.Fatalf("headers %v", resp.Header)
	}
	start := s.next()
	if start.Name != EventMessageStart || start.Data["run_id"] == "" || start.Data["trace_id"] == "" || start.Data["replayed"] != false {
		t.Fatalf("start %+v", start)
	}
	runID := start.Data["run_id"].(string)
	if ev := s.next(); ev.Name != EventThinking || ev.Data["step"].(map[string]any)["status"] != "running" {
		t.Fatalf("thinking %+v", ev)
	}
	s.next()
	if ev := s.next(); ev.Name != EventTextDelta || ev.Data["delta"] != "你好，" {
		t.Fatalf("first text %+v", ev)
	}
	// 运行还卡着：状态是 running
	if r := e.run(t, e.token, runID); r.Status != domain.RunRunning {
		t.Fatalf("status while running %s", r.Status)
	}
	close(gate)
	rest := s.rest()
	if got := strings.Join(names(rest), ","); got != "text_delta,block,followups,message_done" {
		t.Fatalf("rest %s", got)
	}
	if last := rest[len(rest)-1]; last.Data["status"] != "completed" || last.Data["run_id"] != runID {
		t.Fatalf("done %+v", last)
	}
	r := e.waitRun(t, runID)
	if r.Content != "你好，这是回答。" || len(r.Blocks) != 1 || r.Blocks[0]["type"] != "action" || len(r.Followups) != 1 || r.ErrorCode != "" {
		t.Fatalf("persisted %+v", r)
	}
	// 会话：第一条消息生成标题和摘要，计数加一
	rec := e.call(t, http.MethodGet, agentSessionsPath+"/"+cs.SessionID, e.token, nil)
	expectStatus(t, rec, http.StatusOK, "")
	d := decodeBody[sessionDetailView](t, rec)
	if d.Session.Title != "耳机咨询" || d.Session.MessageCount != 1 || !strings.HasPrefix(d.Session.Summary, "用户咨询：推荐一款") ||
		len(d.Messages) != 1 || d.Messages[0].Content != "推荐一款通勤降噪耳机" || d.Messages[0].Run == nil || d.Messages[0].Run.RunID != runID {
		t.Fatalf("detail %+v", d)
	}
	// 轨迹：run.start、运行器写的、run.end
	trace := decodeBody[runTraceResponse](t, e.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", e.token, nil))
	var stages []string
	for _, it := range trace.Items {
		stages = append(stages, it.Stage+"."+it.EventType+"."+it.Status)
	}
	if got := strings.Join(stages, ","); got != "run.start.ok,planner.rule.ok,run.end.completed" {
		t.Fatalf("trace %s", got)
	}
}

func TestAgentStreamValidation(t *testing.T) {
	e := newAgentEnv(t, &scriptRunner{})
	cs := e.newSession(t)
	own, err := e.mem.CreateStoredFile(context.Background(), domain.StoredFile{AccountID: seed.UserID, ObjectKey: "k/own", MimeType: "image/png",
		SizeBytes: 3, ContentHash: strings.Repeat("a", 64), StorageProvider: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	others, err := e.mem.CreateStoredFile(context.Background(), domain.StoredFile{AccountID: seed.User2ID, ObjectKey: "k/other", MimeType: "image/png",
		SizeBytes: 3, ContentHash: strings.Repeat("b", 64), StorageProvider: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"content": "hi"}, "client_message_id"},
		{map[string]any{"client_message_id": "有中文", "content": "hi"}, "client_message_id"},
		{map[string]any{"client_message_id": strings.Repeat("a", 129), "content": "hi"}, "client_message_id"},
		{msgBody("c1", "   "), "content"},
		{msgBody("c1", strings.Repeat("字", 2001)), "content"},
		{map[string]any{"client_message_id": "c1", "content": "hi", "attachments": []any{map[string]any{"file_id": others.FileID}}}, "attachments[0].file_id"},
		{map[string]any{"client_message_id": "c1", "content": "hi", "attachments": []any{map[string]any{"file_id": own.FileID}, map[string]any{}}}, "attachments[1].file_id"},
	} {
		s, resp := openStream(t, e.base, e.token, cs.SessionID, c.body)
		if s != nil {
			t.Fatalf("%v: should not stream", c.body)
		}
		var er ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&er)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || er.Field != c.field {
			t.Fatalf("%v: %d %+v", c.body, resp.StatusCode, er)
		}
	}
	// 不存在的会话、别人的会话：404，不写入
	for _, sid := range []string{"s_missing", cs.SessionID} {
		token := e.token
		if sid == cs.SessionID {
			token = e.token2
		}
		s, resp := openStream(t, e.base, token, sid, msgBody("c1", "hi"))
		if s != nil || resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", sid, resp.StatusCode)
		}
		resp.Body.Close()
	}
	// 自己的附件可以，类型以服务端记录为准
	s, _ := openStream(t, e.base, e.token, cs.SessionID, map[string]any{"client_message_id": "c-att", "content": "看看这张图",
		"attachments": []any{map[string]any{"file_id": own.FileID}}})
	s.rest()
	d := decodeBody[sessionDetailView](t, e.call(t, http.MethodGet, agentSessionsPath+"/"+cs.SessionID, e.token, nil))
	if len(d.Messages) != 1 || d.Messages[0].Attachments[0].MimeType != "image/png" {
		t.Fatalf("attachments %+v", d.Messages)
	}
	// 商家不能用导购
	merchant := e.login(t, seed.MerchantUsername, seed.DevPassword).Token
	expectStatus(t, e.call(t, http.MethodPost, agentSessionsPath, merchant, map[string]any{}), http.StatusForbidden, "forbidden")
}

// 客户端中途断开：运行被取消，状态 cancelled，已生成的内容保留。
func TestAgentStreamClientDisconnect(t *testing.T) {
	runner := &scriptRunner{gate: make(chan struct{})} // 永不放行，只能被取消
	e := newAgentEnv(t, runner)
	cs := e.newSession(t)
	s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-1", "hi"))
	runID := s.next().Data["run_id"].(string)
	for ev := s.next(); ev.Name != EventTextDelta; ev = s.next() {
	}
	s.close()
	r := e.waitRun(t, runID)
	if r.Status != domain.RunCancelled || r.ErrorCode != "cancelled" || r.Content != "你好，" {
		t.Fatalf("after disconnect %+v", r)
	}
	if e.runs.get(runID) != nil {
		t.Fatal("run still registered")
	}
}

// 显式取消：运行停止，流以 error{cancelled} 结束；重复取消幂等；已完成的不能取消；别人的 404。
func TestAgentCancelAPI(t *testing.T) {
	runner := &scriptRunner{gate: make(chan struct{})}
	e := newAgentEnv(t, runner)
	cs := e.newSession(t)
	s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-1", "hi"))
	runID := s.next().Data["run_id"].(string)
	for ev := s.next(); ev.Name != EventTextDelta; ev = s.next() {
	}
	expectStatus(t, e.call(t, http.MethodPost, "/api/v1/agent/runs/"+runID+":cancel", e.token2, nil), http.StatusNotFound, "run_not_found")
	rec := e.call(t, http.MethodPost, "/api/v1/agent/runs/"+runID+":cancel", e.token, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[runView](t, rec); v.Status != domain.RunCancelled || v.Content != "你好，" {
		t.Fatalf("cancel response %+v", v)
	}
	rest := s.rest()
	last := rest[len(rest)-1]
	if last.Name != EventError || last.Data["code"] != "cancelled" || last.Data["message"] != "已停止生成" {
		t.Fatalf("stream end %+v", rest)
	}
	for _, ev := range rest {
		if ev.Name == EventMessageDone {
			t.Fatal("cancelled stream must not send message_done")
		}
	}
	expectStatus(t, e.call(t, http.MethodPost, "/api/v1/agent/runs/"+runID+":cancel", e.token, nil), http.StatusOK, "")
	expectStatus(t, e.call(t, http.MethodPost, "/api/v1/agent/runs/run_missing:cancel", e.token, nil), http.StatusNotFound, "run_not_found")

	runner.gate = nil
	s2, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-2", "hi"))
	done := s2.next().Data["run_id"].(string)
	s2.rest()
	expectStatus(t, e.call(t, http.MethodPost, "/api/v1/agent/runs/"+done+":cancel", e.token, nil), http.StatusConflict, "run_finished")
}

// 重复的 client_message_id 不再运行：已结束的重放保存的结果；进行中的等它结束再重放。
func TestAgentDuplicateMessage(t *testing.T) {
	runner := &scriptRunner{}
	e := newAgentEnv(t, runner)
	cs := e.newSession(t)
	s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("dup", "hi"))
	first := s.next().Data
	s.rest()

	again, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("dup", "这次内容不同"))
	start := again.next()
	if start.Data["run_id"] != first["run_id"] || start.Data["message_id"] != first["message_id"] || start.Data["replayed"] != true {
		t.Fatalf("replay start %+v", start)
	}
	rest := again.rest()
	if got := strings.Join(names(rest), ","); got != "text_delta,block,followups,message_done" || rest[0].Data["delta"] != "你好，这是回答。" {
		t.Fatalf("replay %s %+v", got, rest)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("runner called %d times", runner.calls.Load())
	}
	d := decodeBody[sessionDetailView](t, e.call(t, http.MethodGet, agentSessionsPath+"/"+cs.SessionID, e.token, nil))
	if d.Session.MessageCount != 1 || len(d.Messages) != 1 || d.Messages[0].Content != "hi" {
		t.Fatalf("duplicate wrote %+v", d)
	}

	// 进行中的重复提交：第一条连接断了但运行还在跑，第二次提交等到结束后拿到完整结果
	gate := make(chan struct{})
	runner.gate = gate
	s3, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("live", "hi"))
	live := s3.next().Data["run_id"].(string)
	for ev := s3.next(); ev.Name != EventTextDelta; ev = s3.next() {
	}
	waiting, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("live", "hi"))
	if ev := waiting.next(); ev.Data["run_id"] != live || ev.Data["replayed"] != true {
		t.Fatalf("waiting start %+v", ev)
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	s3.rest()
	rest = waiting.rest()
	if got := strings.Join(names(rest), ","); got != "text_delta,block,followups,message_done" {
		t.Fatalf("waited replay %s", got)
	}
	if runner.calls.Load() != 2 {
		t.Fatalf("runner called %d times", runner.calls.Load())
	}
}

// 运行器出错、panic、超时：状态 failed，流以 error 结束且不暴露内部信息；已生成的内容保留。
func TestAgentRunFailures(t *testing.T) {
	for _, c := range []struct {
		name   string
		runner agent.Runner
		code   string
		event  string
	}{
		{"error", &scriptRunner{err: errors.New("db password=secret")}, "agent_error", "agent_failed"},
		{"panic", agent.RunnerFunc(func(ctx context.Context, in agent.Input, out agent.Output) error {
			out.Text("一半")
			panic("boom")
		}), "agent_error", "agent_failed"},
		{"timeout", &scriptRunner{gate: make(chan struct{})}, "run_timeout", "run_timeout"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t, c.runner)
			e.runTimeout = 300 * time.Millisecond
			cs := e.newSession(t)
			s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m", "hi"))
			runID := s.next().Data["run_id"].(string)
			rest := s.rest()
			last := rest[len(rest)-1]
			if last.Name != EventError || last.Data["code"] != c.event || strings.Contains(last.Raw, "secret") || strings.Contains(last.Raw, "boom") {
				t.Fatalf("end %+v", last)
			}
			r := e.waitRun(t, runID)
			if r.Status != domain.RunFailed || r.ErrorCode != c.code || r.Content == "" {
				t.Fatalf("run %+v", r)
			}
		})
	}
}

func TestAgentHeartbeat(t *testing.T) {
	gate := make(chan struct{})
	e := newAgentEnv(t, &scriptRunner{gate: gate})
	e.heartbeat = 30 * time.Millisecond
	cs := e.newSession(t)
	s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m", "hi"))
	s.next()
	time.Sleep(200 * time.Millisecond)
	close(gate)
	s.rest()
	if s.pings.Load() < 2 {
		t.Fatalf("pings %d", s.pings.Load())
	}
}

// 服务重启：上一个进程留下的进行中运行在启动时标为失败，查询能看到最终状态，重复提交拿到说明；
// 优雅关闭：进行中的运行写为 failed(server_shutdown)，流以 error 结束。
func TestAgentRestartAndShutdown(t *testing.T) {
	gate := make(chan struct{})
	e := newAgentEnv(t, &scriptRunner{gate: gate})
	cs := e.newSession(t)
	// 模拟上一个进程：只写了消息和运行就退出了
	orphan, err := e.mem.StartAgentRun(context.Background(), domain.UserMessage{AccountID: seed.UserID, SessionID: cs.SessionID,
		ClientMessageID: "before-restart", Content: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	if n, err := e.RecoverInterruptedRuns(context.Background(), time.Now()); err != nil || n != 1 {
		t.Fatalf("recover %d %v", n, err)
	}
	if r := e.run(t, e.token, orphan.Run.RunID); r.Status != domain.RunFailed || r.ErrorCode != "interrupted" {
		t.Fatalf("orphan %+v", r)
	}
	s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("before-restart", "hi"))
	s.next()
	if rest := s.rest(); len(rest) != 1 || rest[0].Name != EventError || rest[0].Data["code"] != "interrupted" {
		t.Fatalf("replay orphan %+v", rest)
	}

	live, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("during-shutdown", "hi"))
	runID := live.next().Data["run_id"].(string)
	for ev := live.next(); ev.Name != EventTextDelta; ev = live.next() {
	}
	e.StopAgentRuns(2 * time.Second)
	rest := live.rest()
	if last := rest[len(rest)-1]; last.Name != EventError || last.Data["code"] != "server_shutdown" {
		t.Fatalf("shutdown end %+v", rest)
	}
	if r := e.run(t, e.token, runID); r.Status != domain.RunFailed || r.ErrorCode != "server_shutdown" || r.Content != "你好，" {
		t.Fatalf("after shutdown %+v", r)
	}
}

func TestAgentSessionsAPI(t *testing.T) {
	e := newAgentEnv(t, &scriptRunner{})
	created := e.newSession(t)
	if created.Title != "AI 导购" || created.MessageCount != 0 || created.Pinned {
		t.Fatalf("created %+v", created)
	}
	list := func(q string) []sessionView {
		t.Helper()
		rec := e.call(t, http.MethodGet, agentSessionsPath+q, e.token, nil)
		expectStatus(t, rec, http.StatusOK, "")
		return decodeBody[pageResponse[sessionView]](t, rec).Items
	}
	if got := list(""); len(got) != 0 {
		t.Fatalf("empty session listed: %+v", got)
	}
	send := func(sid, cid, content string) {
		t.Helper()
		s, _ := openStream(t, e.base, e.token, sid, msgBody(cid, content))
		s.rest()
	}
	send(created.SessionID, "1", "帮我推荐一下吧！！")
	other := e.newSession(t)
	send(other.SessionID, "1", "预算三千的拍照手机")
	send(other.SessionID, "2", "要轻一点")
	send(other.SessionID, "3", "续航好")
	send(other.SessionID, "4", "第四句不进摘要")
	got := list("")
	if len(got) != 2 || got[0].SessionID != other.SessionID || got[1].Title != "导购咨询" {
		t.Fatalf("list %+v", got)
	}
	if got := list("?keyword=" + "拍照"); len(got) != 1 || got[0].SessionID != other.SessionID {
		t.Fatalf("search %+v", got)
	}
	expectStatus(t, e.call(t, http.MethodGet, agentSessionsPath+"?keyword="+strings.Repeat("a", 65), e.token, nil), http.StatusBadRequest, "invalid_argument")

	// 改标题、校验
	rec := e.call(t, http.MethodPatch, agentSessionsPath+"/"+created.SessionID, e.token, map[string]any{"title": "  我的会话 "})
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[sessionView](t, rec); v.Title != "我的会话" {
		t.Fatalf("patched %+v", v)
	}
	for _, body := range []map[string]any{{"title": " "}, {"title": strings.Repeat("长", 129)}, {"summary": strings.Repeat("长", 501)}, {}} {
		expectStatus(t, e.call(t, http.MethodPatch, agentSessionsPath+"/"+created.SessionID, e.token, body), http.StatusBadRequest, "invalid_argument")
	}
	// 置顶：显式设置，不是切换
	pin := func(pinned bool) sessionView {
		rec := e.call(t, http.MethodPost, agentSessionsPath+"/"+created.SessionID+":pin", e.token, map[string]any{"pinned": pinned})
		expectStatus(t, rec, http.StatusOK, "")
		return decodeBody[sessionView](t, rec)
	}
	if v := pin(true); !v.Pinned || v.PinnedAt == nil {
		t.Fatalf("pin %+v", v)
	}
	if v := pin(true); !v.Pinned {
		t.Fatal("pin twice should stay pinned")
	}
	if got := list(""); got[0].SessionID != created.SessionID {
		t.Fatalf("pinned first %+v", got)
	}
	if v := pin(false); v.Pinned || v.PinnedAt != nil {
		t.Fatalf("unpin %+v", v)
	}
	expectStatus(t, e.call(t, http.MethodPost, agentSessionsPath+"/"+created.SessionID+":pin", e.token, map[string]any{}), http.StatusBadRequest, "invalid_argument")
	// 摘要：前三条用户消息
	rec = e.call(t, http.MethodPost, agentSessionsPath+"/"+other.SessionID+":summarize", e.token, nil)
	expectStatus(t, rec, http.StatusOK, "")
	if v := decodeBody[sessionView](t, rec); v.Summary != "预算三千的拍照手机；要轻一点；续航好" || v.Title != "手机咨询" {
		t.Fatalf("summarize %+v", v)
	}
	// 别人的会话：都按不存在处理
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/" + other.SessionID}, {http.MethodPatch, "/" + other.SessionID}, {http.MethodDelete, "/" + other.SessionID},
		{http.MethodPost, "/" + other.SessionID + ":pin"}, {http.MethodPost, "/" + other.SessionID + ":summarize"},
	} {
		expectStatus(t, e.call(t, c.method, agentSessionsPath+c.path, e.token2, map[string]any{"title": "x", "pinned": true}), http.StatusNotFound, "session_not_found")
	}
	if got := decodeBody[pageResponse[sessionView]](t, e.call(t, http.MethodGet, agentSessionsPath, e.token2, nil)); got.Total != 0 {
		t.Fatalf("other user sees %d", got.Total)
	}
	runID := decodeBody[sessionDetailView](t, e.call(t, http.MethodGet, agentSessionsPath+"/"+other.SessionID, e.token, nil)).Messages[0].Run.RunID
	expectStatus(t, e.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", e.token2, nil), http.StatusNotFound, "run_not_found")
	expectStatus(t, e.call(t, http.MethodPost, agentSessionsPath+"/"+other.SessionID+":unknown", e.token, nil), http.StatusNotFound, "not_found")
	// 删除：软删，之后都按不存在处理
	rec = e.call(t, http.MethodDelete, agentSessionsPath+"/"+other.SessionID, e.token, nil)
	expectStatus(t, rec, http.StatusOK, "")
	expectStatus(t, e.call(t, http.MethodGet, agentSessionsPath+"/"+other.SessionID, e.token, nil), http.StatusNotFound, "session_not_found")
	expectStatus(t, e.call(t, http.MethodDelete, agentSessionsPath+"/"+other.SessionID, e.token, nil), http.StatusNotFound, "session_not_found")
	if got := list(""); len(got) != 1 {
		t.Fatalf("after delete %+v", got)
	}
}

// 真实 MySQL：流式输出、持久化、重复提交重放、断开后取消。
func TestAgentStreamMySQL(t *testing.T) {
	ts, _ := newMySQLTestServer(t, time.Now)
	runner := &scriptRunner{}
	ts.runner = runner
	srv := httptest.NewServer(ts.handler)
	t.Cleanup(srv.Close)
	e := &agentTestEnv{testServer: ts, base: srv.URL, token: ts.login(t, seed.UserUsername, seed.DevPassword).Token,
		token2: ts.login(t, seed.User2Username, seed.DevPassword).Token}
	cs := e.newSession(t)
	s, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-1", "推荐耳机"))
	runID := s.next().Data["run_id"].(string)
	if got := strings.Join(names(s.rest()), ","); got != "thinking,thinking,text_delta,text_delta,block,followups,message_done" {
		t.Fatalf("events %s", got)
	}
	if r := e.waitRun(t, runID); r.Content != "你好，这是回答。" || r.Blocks[0]["label"] != "去购物车" || r.Followups[0] != "还有别的吗？" {
		t.Fatalf("persisted %+v", r)
	}
	again, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-1", "推荐耳机"))
	if ev := again.next(); ev.Data["replayed"] != true || ev.Data["run_id"] != runID {
		t.Fatalf("replay %+v", ev)
	}
	again.rest()
	if runner.calls.Load() != 1 {
		t.Fatalf("calls %d", runner.calls.Load())
	}
	runner.gate = make(chan struct{})
	live, _ := openStream(t, e.base, e.token, cs.SessionID, msgBody("m-2", "hi"))
	liveID := live.next().Data["run_id"].(string)
	for ev := live.next(); ev.Name != EventTextDelta; ev = live.next() {
	}
	live.close()
	if r := e.waitRun(t, liveID); r.Status != domain.RunCancelled || r.Content != "你好，" {
		t.Fatalf("disconnect %+v", r)
	}
	d := decodeBody[sessionDetailView](t, e.call(t, http.MethodGet, agentSessionsPath+"/"+cs.SessionID, e.token, nil))
	if d.Session.MessageCount != 2 || len(d.Messages) != 2 || d.Session.Title != "耳机咨询" {
		t.Fatalf("detail %+v", d)
	}
	expectStatus(t, e.call(t, http.MethodGet, agentSessionsPath+"/"+cs.SessionID, e.token2, nil), http.StatusNotFound, "session_not_found")
}
