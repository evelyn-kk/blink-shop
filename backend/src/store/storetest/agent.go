package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func agentCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"ChatSessionCRUDAndIsolation", testChatSessionCRUD},
		{"StartAgentRunIsIdempotent", testStartAgentRun},
		{"StartAgentRunConcurrentDuplicates", testStartAgentRunConcurrent},
		{"ChatSessionListOrderAndSearch", testChatSessionList},
		{"AgentRunUpdatesFollowStateMachine", testAgentRunUpdates},
		{"ChatMessagesInWriteOrder", testChatMessageOrder},
		{"TraceEventsInWriteOrder", testTraceEvents},
		{"FailInterruptedRuns", testFailInterruptedRuns},
	}
}

func newSession(t *testing.T, s store.Store, accountID, title string) domain.ChatSession {
	t.Helper()
	cs, err := s.CreateChatSession(context.Background(), domain.ChatSession{AccountID: accountID, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func send(t *testing.T, s store.Store, accountID, sessionID, clientID, content string) store.StartedRun {
	t.Helper()
	r, err := s.StartAgentRun(context.Background(), domain.UserMessage{AccountID: accountID, SessionID: sessionID,
		ClientMessageID: clientID, Content: content}, nil)
	if err != nil {
		t.Fatalf("start %s: %v", clientID, err)
	}
	return r
}

func testChatSessionCRUD(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := newSession(t, s, seed.UserID, "AI 导购")
	if cs.SessionID == "" || cs.MessageCount != 0 || cs.PinnedAt != nil || cs.CreatedAt.IsZero() {
		t.Fatalf("created %+v", cs)
	}
	if _, err := s.CreateChatSession(ctx, domain.ChatSession{AccountID: seed.UserID, Title: ""}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty title: %v", err)
	}
	if got, err := s.GetChatSession(ctx, seed.UserID, cs.SessionID); err != nil || got.Title != "AI 导购" {
		t.Fatalf("get %+v %v", got, err)
	}
	// 别人的会话按不存在处理
	if _, err := s.GetChatSession(ctx, seed.User2ID, cs.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account get: %v", err)
	}
	if _, err := s.UpdateChatSession(ctx, seed.User2ID, cs.SessionID, func(*domain.ChatSession) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account update: %v", err)
	}
	if _, err := s.ListChatMessages(ctx, seed.User2ID, cs.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account messages: %v", err)
	}
	pinAt := time.Now().UTC()
	up, err := s.UpdateChatSession(ctx, seed.UserID, cs.SessionID, func(c *domain.ChatSession) error {
		c.Title, c.Summary, c.PinnedAt = "耳机咨询", "通勤降噪耳机", &pinAt
		c.MessageCount, c.AccountID = 99, seed.User2ID // 不会写入
		return nil
	})
	if err != nil || up.Title != "耳机咨询" || up.Summary != "通勤降噪耳机" || up.PinnedAt == nil || up.MessageCount != 0 || up.AccountID != seed.UserID {
		t.Fatalf("update %+v %v", up, err)
	}
	if _, err := s.UpdateChatSession(ctx, seed.UserID, cs.SessionID, func(c *domain.ChatSession) error { c.Title = ""; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty title update: %v", err)
	}
	if _, err := s.UpdateChatSession(ctx, seed.UserID, cs.SessionID, func(c *domain.ChatSession) error { c.Title = "x"; return errBoom }); !errors.Is(err, errBoom) {
		t.Fatalf("fn error: %v", err)
	}
	if got, _ := s.GetChatSession(ctx, seed.UserID, cs.SessionID); got.Title != "耳机咨询" || got.PinnedAt == nil {
		t.Fatalf("after rejected updates: %+v", got)
	}
	now := time.Now().UTC()
	if _, err := s.UpdateChatSession(ctx, seed.UserID, cs.SessionID, func(c *domain.ChatSession) error { c.DeletedAt = &now; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChatSession(ctx, seed.UserID, cs.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted get: %v", err)
	}
	if _, err := s.StartAgentRun(ctx, domain.UserMessage{AccountID: seed.UserID, SessionID: cs.SessionID, ClientMessageID: "c1", Content: "hi"}, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("send to deleted: %v", err)
	}
}

func testStartAgentRun(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := newSession(t, s, seed.UserID, "AI 导购")
	r, err := s.StartAgentRun(ctx, domain.UserMessage{AccountID: seed.UserID, SessionID: cs.SessionID, ClientMessageID: "c-1",
		Content: "推荐通勤耳机", Attachments: []domain.Attachment{{FileID: "file_1", MimeType: "image/png"}}}, func(c *domain.ChatSession) {
		c.Title, c.Summary = "耳机咨询", "用户咨询：推荐通勤耳机"
		c.MessageCount = 50 // 计数由存储维护
	})
	if err != nil || !r.Created || r.Message.MessageID == "" || r.Run.RunID == "" || r.Run.TraceID == "" || r.Run.Status != domain.RunRunning ||
		r.Run.MessageID != r.Message.MessageID || len(r.Message.Attachments) != 1 {
		t.Fatalf("start %+v %v", r, err)
	}
	got, _ := s.GetChatSession(ctx, seed.UserID, cs.SessionID)
	if got.MessageCount != 1 || got.LastMessageAt == nil || got.Title != "耳机咨询" || got.Summary != "用户咨询：推荐通勤耳机" {
		t.Fatalf("session after start %+v", got)
	}
	// 同一 client_message_id：返回已有的，不再写入，fn 也不调用
	again, err := s.StartAgentRun(ctx, domain.UserMessage{AccountID: seed.UserID, SessionID: cs.SessionID, ClientMessageID: "c-1",
		Content: "别的内容"}, func(c *domain.ChatSession) { c.Title = "不应生效" })
	if err != nil || again.Created || again.Message.MessageID != r.Message.MessageID || again.Run.RunID != r.Run.RunID || again.Message.Content != "推荐通勤耳机" {
		t.Fatalf("duplicate %+v %v", again, err)
	}
	if got, _ := s.GetChatSession(ctx, seed.UserID, cs.SessionID); got.MessageCount != 1 || got.Title != "耳机咨询" {
		t.Fatalf("duplicate changed session %+v", got)
	}
	// 同一个 client_message_id 在别的会话里是新消息
	other := newSession(t, s, seed.UserID, "AI 导购")
	if r2 := send(t, s, seed.UserID, other.SessionID, "c-1", "x"); !r2.Created {
		t.Fatal("same client id in another session should be new")
	}
	// 别人的会话不能发
	if _, err := s.StartAgentRun(ctx, domain.UserMessage{AccountID: seed.User2ID, SessionID: cs.SessionID, ClientMessageID: "c-9", Content: "x"}, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account start: %v", err)
	}
	if _, err := s.StartAgentRun(ctx, domain.UserMessage{AccountID: seed.UserID, SessionID: cs.SessionID, Content: "x"}, nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("missing client id: %v", err)
	}
	time.Sleep(3 * time.Millisecond)
	second := send(t, s, seed.UserID, cs.SessionID, "c-2", "第二句")
	turns, err := s.ListChatMessages(ctx, seed.UserID, cs.SessionID)
	if err != nil || len(turns) != 2 || turns[0].Message.ClientMessageID != "c-1" || turns[1].Message.MessageID != second.Message.MessageID ||
		turns[0].Run == nil || turns[0].Run.RunID != r.Run.RunID || turns[0].Message.Attachments[0].FileID != "file_1" {
		t.Fatalf("turns %+v %v", turns, err)
	}
	if _, err := s.GetAgentRun(ctx, seed.User2ID, r.Run.RunID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account run: %v", err)
	}
}

func testStartAgentRunConcurrent(t *testing.T, s store.Store) {
	cs := newSession(t, s, seed.UserID, "AI 导购")
	const n = 8
	results := make([]store.StartedRun, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.StartAgentRun(context.Background(), domain.UserMessage{AccountID: seed.UserID, SessionID: cs.SessionID,
				ClientMessageID: "same", Content: "并发"}, nil)
		}(i)
	}
	wg.Wait()
	created := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if results[i].Created {
			created++
		}
		if results[i].Run.RunID != results[0].Run.RunID {
			t.Fatalf("different runs: %s vs %s", results[i].Run.RunID, results[0].Run.RunID)
		}
	}
	got, _ := s.GetChatSession(context.Background(), seed.UserID, cs.SessionID)
	if created != 1 || got.MessageCount != 1 {
		t.Fatalf("created %d, count %d", created, got.MessageCount)
	}
}

func testChatSessionList(t *testing.T, s store.Store) {
	ctx := context.Background()
	newSession(t, s, seed.UserID, "空会话") // 没有消息：不列出
	a := newSession(t, s, seed.UserID, "手机咨询")
	send(t, s, seed.UserID, a.SessionID, "1", "预算三千的拍照手机")
	time.Sleep(3 * time.Millisecond)
	b := newSession(t, s, seed.UserID, "耳机咨询")
	send(t, s, seed.UserID, b.SessionID, "1", "降噪 100% 的耳机")
	time.Sleep(3 * time.Millisecond)
	c := newSession(t, s, seed.UserID, "台灯")
	send(t, s, seed.UserID, c.SessionID, "1", "Desk Lamp 推荐")
	d := newSession(t, s, seed.User2ID, "别人的手机咨询")
	send(t, s, seed.User2ID, d.SessionID, "1", "手机")
	gone := newSession(t, s, seed.UserID, "删掉的手机咨询")
	send(t, s, seed.UserID, gone.SessionID, "1", "手机")
	now := time.Now().UTC()
	if _, err := s.UpdateChatSession(ctx, seed.UserID, gone.SessionID, func(x *domain.ChatSession) error { x.DeletedAt = &now; return nil }); err != nil {
		t.Fatal(err)
	}
	ids := func(q store.ChatSessionQuery) ([]string, int) {
		t.Helper()
		list, total, err := s.ListChatSessions(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, x := range list {
			out = append(out, x.SessionID)
		}
		return out, total
	}
	q := func(kw string) store.ChatSessionQuery {
		return store.ChatSessionQuery{AccountID: seed.UserID, Keyword: kw, Page: page100}
	}
	if got, total := ids(q("")); total != 3 || len(got) != 3 || got[0] != c.SessionID || got[1] != b.SessionID || got[2] != a.SessionID {
		t.Fatalf("default order %v (%d)", got, total)
	}
	// 置顶的排最前（即使消息更早）
	pin := time.Now().UTC()
	if _, err := s.UpdateChatSession(ctx, seed.UserID, a.SessionID, func(x *domain.ChatSession) error { x.PinnedAt = &pin; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids(q("")); got[0] != a.SessionID || got[1] != c.SessionID {
		t.Fatalf("pinned order %v", got)
	}
	for kw, want := range map[string][]string{
		"手机":        {a.SessionID}, // 标题
		"拍照":        {a.SessionID}, // 消息内容
		"desk lamp": {c.SessionID}, // 不区分大小写
		"100%":      {b.SessionID}, // % 按字面匹配
		"%":         {b.SessionID},
		"不存在的词":     {},
	} {
		got, total := ids(q(kw))
		if total != len(want) || len(got) != len(want) || (len(want) > 0 && got[0] != want[0]) {
			t.Fatalf("keyword %q: %v (%d), want %v", kw, got, total, want)
		}
	}
	if got, total := ids(store.ChatSessionQuery{AccountID: seed.UserID, Page: store.Page{Page: 2, PageSize: 2}}); total != 3 || len(got) != 1 || got[0] != b.SessionID {
		t.Fatalf("page 2: %v (%d)", got, total)
	}
}

func testAgentRunUpdates(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := newSession(t, s, seed.UserID, "AI 导购")
	r := send(t, s, seed.UserID, cs.SessionID, "1", "hi").Run
	if _, err := s.UpdateAgentRun(ctx, seed.User2ID, r.RunID, func(x *domain.AgentRun) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account: %v", err)
	}
	if _, err := s.UpdateAgentRun(ctx, seed.UserID, r.RunID, func(x *domain.AgentRun) error { x.Status = domain.RunQueued; return nil }); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("running -> queued: %v", err)
	}
	if _, err := s.UpdateAgentRun(ctx, seed.UserID, r.RunID, func(x *domain.AgentRun) error { x.Content = "x"; return errBoom }); !errors.Is(err, errBoom) {
		t.Fatalf("fn error: %v", err)
	}
	done, err := s.UpdateAgentRun(ctx, seed.UserID, r.RunID, func(x *domain.AgentRun) error {
		x.Status, x.Content = domain.RunCompleted, "推荐 A 和 B"
		x.Blocks = []map[string]any{{"type": "product_list", "product_ids": []any{"p_1", "p_2"}, "count": 2}}
		x.Followups = []string{"要对比吗？"}
		x.RunID, x.AccountID = "run_other", seed.User2ID // 不会写入
		return nil
	})
	if err != nil || done.Status != domain.RunCompleted || done.Content != "推荐 A 和 B" || len(done.Blocks) != 1 ||
		done.Blocks[0]["count"] != float64(2) || len(done.Followups) != 1 || done.RunID != r.RunID || done.AccountID != seed.UserID {
		t.Fatalf("complete %+v %v", done, err)
	}
	got, _ := s.GetAgentRun(ctx, seed.UserID, r.RunID)
	if got.Content != "推荐 A 和 B" || got.Blocks[0]["type"] != "product_list" || got.Followups[0] != "要对比吗？" {
		t.Fatalf("read back %+v", got)
	}
	// 已结束的运行不能再改，包括“改成同样的结束状态”
	for _, st := range []domain.RunStatus{domain.RunCompleted, domain.RunCancelled, domain.RunFailed} {
		if _, err := s.UpdateAgentRun(ctx, seed.UserID, r.RunID, func(x *domain.AgentRun) error { x.Status = st; return nil }); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("completed -> %s: %v", st, err)
		}
	}
}

// 同一毫秒内连续发送的消息也按发送顺序返回。
func testChatMessageOrder(t *testing.T, s store.Store) {
	cs := newSession(t, s, seed.UserID, "AI 导购")
	var want []string
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		send(t, s, seed.UserID, cs.SessionID, id, "第 "+id+" 句")
		want = append(want, id)
	}
	turns, err := s.ListChatMessages(context.Background(), seed.UserID, cs.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for i, tr := range turns {
		if tr.Message.ClientMessageID != want[i] {
			t.Fatalf("order at %d: %s, want %s", i, tr.Message.ClientMessageID, want[i])
		}
	}
}

func testTraceEvents(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := newSession(t, s, seed.UserID, "AI 导购")
	r := send(t, s, seed.UserID, cs.SessionID, "1", "hi").Run
	at := time.Now().UTC().Truncate(time.Millisecond)
	for i, stage := range []string{"run", "planner", "answer", "run"} {
		if _, err := s.AppendTraceEvent(ctx, domain.AgentTraceEvent{RunID: r.RunID, TraceID: r.TraceID, AccountID: seed.UserID, Stage: stage,
			EventType: "step", Status: "ok", DurationMS: int64(i), Metadata: map[string]any{"i": i}, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendTraceEvent(ctx, domain.AgentTraceEvent{RunID: r.RunID, AccountID: seed.UserID}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("missing stage: %v", err)
	}
	evs, err := s.ListTraceEvents(ctx, seed.UserID, r.RunID)
	if err != nil || len(evs) != 4 || evs[1].Stage != "planner" || evs[2].Stage != "answer" || evs[3].DurationMS != 3 || evs[2].Metadata["i"] != float64(2) {
		t.Fatalf("events %+v %v", evs, err)
	}
	if evs, _ := s.ListTraceEvents(ctx, seed.User2ID, r.RunID); len(evs) != 0 {
		t.Fatalf("other account sees %d events", len(evs))
	}
}

func testFailInterruptedRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := newSession(t, s, seed.UserID, "AI 导购")
	running := send(t, s, seed.UserID, cs.SessionID, "1", "a").Run
	done := send(t, s, seed.UserID, cs.SessionID, "2", "b").Run
	if _, err := s.UpdateAgentRun(ctx, seed.UserID, done.RunID, func(x *domain.AgentRun) error { x.Status = domain.RunCompleted; return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	startedAt := time.Now().UTC()
	time.Sleep(3 * time.Millisecond)
	fresh := send(t, s, seed.UserID, cs.SessionID, "3", "c").Run // 本进程启动后才开始的运行不受影响
	n, err := s.FailInterruptedRuns(ctx, "interrupted", startedAt)
	if err != nil || n != 1 {
		t.Fatalf("failed %d %v", n, err)
	}
	if r, _ := s.GetAgentRun(ctx, seed.UserID, running.RunID); r.Status != domain.RunFailed || r.ErrorCode != "interrupted" {
		t.Fatalf("running after sweep %+v", r)
	}
	if r, _ := s.GetAgentRun(ctx, seed.UserID, done.RunID); r.Status != domain.RunCompleted || r.ErrorCode != "" {
		t.Fatalf("completed after sweep %+v", r)
	}
	if r, _ := s.GetAgentRun(ctx, seed.UserID, fresh.RunID); r.Status != domain.RunRunning {
		t.Fatalf("fresh run after sweep %+v", r)
	}
	if n, _ := s.FailInterruptedRuns(ctx, "interrupted", startedAt); n != 0 {
		t.Fatalf("second sweep %d", n)
	}
}
