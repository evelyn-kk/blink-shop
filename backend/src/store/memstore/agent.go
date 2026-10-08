package memstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ---------- 导购会话 ----------

// deepCopy 用 JSON 往返复制含 map/切片的值（与 MySQL 读回的类型一致，数字为 float64）。
func deepCopy[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("memstore: copy %T: %v", v, err))
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		panic(fmt.Sprintf("memstore: copy %T: %v", v, err))
	}
	return out
}

// truncTime 复制时间指针并截到毫秒（与 DATETIME(3) 一致）。
func (s *Store) truncTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC().Truncate(time.Millisecond)
	return &v
}

func (s *Store) CreateChatSession(ctx context.Context, cs domain.ChatSession) (domain.ChatSession, error) {
	if err := store.ValidateChatSession(cs); err != nil {
		return domain.ChatSession{}, err
	}
	defer s.lock(ctx)()
	if cs.SessionID == "" {
		cs.SessionID = domain.NewID(domain.PrefixSession)
	}
	if _, ok := s.data.sessions[cs.SessionID]; ok {
		return domain.ChatSession{}, &store.ConflictError{Key: store.KeyPrimary}
	}
	now := s.timestamp()
	cs.MessageCount, cs.LastMessageAt, cs.PinnedAt, cs.DeletedAt = 0, nil, nil, nil
	cs.CreatedAt, cs.UpdatedAt = now, now
	s.data.sessions[cs.SessionID] = cs
	return cs, nil
}

// session 返回属于 accountID 且未删除的会话。调用方需持有锁。
func (s *Store) session(accountID, sessionID string) (domain.ChatSession, error) {
	cs, ok := s.data.sessions[sessionID]
	if !ok || cs.AccountID != accountID || cs.DeletedAt != nil {
		return domain.ChatSession{}, store.ErrNotFound
	}
	return cs, nil
}

func (s *Store) ListChatSessions(ctx context.Context, q store.ChatSessionQuery) ([]domain.ChatSession, int, error) {
	defer s.lock(ctx)()
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	var hitSessions map[string]bool
	if kw != "" {
		hitSessions = map[string]bool{}
		for _, m := range s.data.messages {
			if m.AccountID == q.AccountID && strings.Contains(strings.ToLower(m.Content), kw) {
				hitSessions[m.SessionID] = true
			}
		}
	}
	all := []domain.ChatSession{}
	for _, cs := range s.data.sessions {
		if cs.AccountID != q.AccountID || cs.DeletedAt != nil || cs.MessageCount == 0 {
			continue
		}
		if kw != "" && !hitSessions[cs.SessionID] && !strings.Contains(strings.ToLower(cs.Title), kw) &&
			!strings.Contains(strings.ToLower(cs.Summary), kw) {
			continue
		}
		all = append(all, cs)
	}
	sort.Slice(all, func(i, j int) bool { return sessionBefore(all[i], all[j]) })
	return pageOf(all, q.Page), len(all), nil
}

// sessionBefore：置顶在前（置顶时间倒序），其余按最近消息时间倒序，再按 ID 倒序。
func sessionBefore(a, b domain.ChatSession) bool {
	if (a.PinnedAt != nil) != (b.PinnedAt != nil) {
		return a.PinnedAt != nil
	}
	if a.PinnedAt != nil && !a.PinnedAt.Equal(*b.PinnedAt) {
		return a.PinnedAt.After(*b.PinnedAt)
	}
	at, bt := a.CreatedAt, b.CreatedAt
	if a.LastMessageAt != nil {
		at = *a.LastMessageAt
	}
	if b.LastMessageAt != nil {
		bt = *b.LastMessageAt
	}
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return a.SessionID > b.SessionID
}

func (s *Store) GetChatSession(ctx context.Context, accountID, sessionID string) (domain.ChatSession, error) {
	defer s.lock(ctx)()
	return s.session(accountID, sessionID)
}

func (s *Store) UpdateChatSession(ctx context.Context, accountID, sessionID string, fn func(cs *domain.ChatSession) error) (domain.ChatSession, error) {
	var out domain.ChatSession
	err := s.WithTx(ctx, func(ctx context.Context) error {
		before, err := s.session(accountID, sessionID)
		if err != nil {
			return err
		}
		cs := before
		if err := fn(&cs); err != nil {
			return err
		}
		after := before
		after.Title, after.Summary, after.PinnedAt, after.DeletedAt = cs.Title, cs.Summary, s.truncTime(cs.PinnedAt), s.truncTime(cs.DeletedAt)
		if err := store.ValidateChatSession(after); err != nil {
			return err
		}
		after.UpdatedAt = s.timestamp()
		s.data.sessions[sessionID] = after
		out = after
		return nil
	})
	return out, err
}

func (s *Store) ListChatMessages(ctx context.Context, accountID, sessionID string) ([]store.ChatTurn, error) {
	defer s.lock(ctx)()
	if _, err := s.session(accountID, sessionID); err != nil {
		return nil, err
	}
	msgs := []domain.UserMessage{}
	for _, m := range s.data.messages {
		if m.SessionID == sessionID && m.AccountID == accountID {
			msgs = append(msgs, m)
		}
	}
	sort.Slice(msgs, func(i, j int) bool {
		return s.data.messageSeq[msgs[i].MessageID] < s.data.messageSeq[msgs[j].MessageID]
	})
	out := make([]store.ChatTurn, 0, len(msgs))
	for _, m := range msgs {
		t := store.ChatTurn{Message: deepCopy(m)}
		if r, ok := s.runOfMessage(accountID, m.MessageID); ok {
			rc := deepCopy(r)
			t.Run = &rc
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) runOfMessage(accountID, messageID string) (domain.AgentRun, bool) {
	for _, r := range s.data.runs {
		if r.AccountID == accountID && r.MessageID == messageID {
			return r, true
		}
	}
	return domain.AgentRun{}, false
}

func (s *Store) StartAgentRun(ctx context.Context, msg domain.UserMessage, fn func(cs *domain.ChatSession)) (store.StartedRun, error) {
	if err := store.ValidateUserMessage(msg); err != nil {
		return store.StartedRun{}, err
	}
	var out store.StartedRun
	err := s.WithTx(ctx, func(ctx context.Context) error {
		cs, err := s.session(msg.AccountID, msg.SessionID)
		if err != nil {
			return err
		}
		for _, m := range s.data.messages {
			if m.AccountID == msg.AccountID && m.SessionID == msg.SessionID && m.ClientMessageID == msg.ClientMessageID {
				r, ok := s.runOfMessage(msg.AccountID, m.MessageID)
				if !ok {
					return fmt.Errorf("memstore: message %s has no run", m.MessageID)
				}
				out = store.StartedRun{Message: deepCopy(m), Run: deepCopy(r), Created: false}
				return nil
			}
		}
		now := s.timestamp()
		msg.MessageID = domain.NewID(domain.PrefixMessage)
		msg.CreatedAt = now
		if msg.Attachments == nil {
			msg.Attachments = []domain.Attachment{}
		}
		msg.Attachments = slices.Clone(msg.Attachments)
		run := domain.AgentRun{RunID: domain.NewID(domain.PrefixRun), SessionID: msg.SessionID, MessageID: msg.MessageID,
			AccountID: msg.AccountID, TraceID: domain.NewID(domain.PrefixTrace), Status: domain.RunRunning, CreatedAt: now, UpdatedAt: now}
		before := cs
		if fn != nil {
			fn(&cs)
		}
		cs.SessionID, cs.AccountID, cs.CreatedAt = before.SessionID, before.AccountID, before.CreatedAt
		cs.MessageCount = before.MessageCount + 1
		cs.LastMessageAt = &now
		cs.UpdatedAt = now
		if err := store.ValidateChatSession(cs); err != nil {
			return err
		}
		s.data.messages[msg.MessageID] = msg
		s.data.messageSeq[msg.MessageID] = int64(len(s.data.messageSeq)) + 1
		s.data.runs[run.RunID] = run
		s.data.sessions[cs.SessionID] = cs
		out = store.StartedRun{Message: deepCopy(msg), Run: deepCopy(run), Created: true}
		return nil
	})
	return out, err
}

func (s *Store) GetAgentRun(ctx context.Context, accountID, runID string) (domain.AgentRun, error) {
	defer s.lock(ctx)()
	r, ok := s.data.runs[runID]
	if !ok || r.AccountID != accountID {
		return domain.AgentRun{}, store.ErrNotFound
	}
	return deepCopy(r), nil
}

func (s *Store) UpdateAgentRun(ctx context.Context, accountID, runID string, fn func(r *domain.AgentRun) error) (domain.AgentRun, error) {
	var out domain.AgentRun
	err := s.WithTx(ctx, func(ctx context.Context) error {
		before, ok := s.data.runs[runID]
		if !ok || before.AccountID != accountID {
			return store.ErrNotFound
		}
		r := deepCopy(before)
		if err := fn(&r); err != nil {
			return err
		}
		if err := store.NextRunState(before.Status, r.Status); err != nil {
			return err
		}
		r.RunID, r.SessionID, r.MessageID, r.AccountID, r.TraceID, r.CreatedAt =
			before.RunID, before.SessionID, before.MessageID, before.AccountID, before.TraceID, before.CreatedAt
		r.UpdatedAt = s.timestamp()
		r = deepCopy(r)
		s.data.runs[runID] = r
		out = deepCopy(r)
		return nil
	})
	return out, err
}

func (s *Store) AppendTraceEvent(ctx context.Context, e domain.AgentTraceEvent) (domain.AgentTraceEvent, error) {
	if e.RunID == "" || e.AccountID == "" || e.Stage == "" || e.EventType == "" {
		return domain.AgentTraceEvent{}, fmt.Errorf("%w: 轨迹缺少运行、账户、阶段或类型", store.ErrInvalid)
	}
	defer s.lock(ctx)()
	if e.TraceEventID == "" {
		e.TraceEventID = domain.NewID(domain.PrefixTraceEvt)
	}
	e.CreatedAt = s.orNow(e.CreatedAt)
	e = deepCopy(e)
	s.data.traces[e.TraceEventID] = e
	s.data.traceSeq[e.TraceEventID] = int64(len(s.data.traceSeq)) + 1
	return e, nil
}

func (s *Store) ListTraceEvents(ctx context.Context, accountID, runID string) ([]domain.AgentTraceEvent, error) {
	defer s.lock(ctx)()
	out := []domain.AgentTraceEvent{}
	for _, e := range s.data.traces {
		if e.RunID == runID && e.AccountID == accountID {
			out = append(out, deepCopy(e))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return s.data.traceSeq[out[i].TraceEventID] < s.data.traceSeq[out[j].TraceEventID]
	})
	return out, nil
}

func (s *Store) FailInterruptedRuns(ctx context.Context, errorCode string, createdBefore time.Time) (int, error) {
	defer s.lock(ctx)()
	n := 0
	now := s.timestamp()
	for id, r := range s.data.runs {
		if (r.Status == domain.RunQueued || r.Status == domain.RunRunning) && r.CreatedAt.Before(createdBefore) {
			r.Status, r.ErrorCode, r.UpdatedAt = domain.RunFailed, errorCode, now
			s.data.runs[id] = r
			n++
		}
	}
	return n, nil
}
