package mysqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ---------- 导购会话 ----------

const sessionColumns = `session_id, account_id, title, COALESCE(summary, ''), message_count, last_message_at, pinned_at, deleted_at,
	created_at, updated_at`

func scanSession(row rowScanner) (domain.ChatSession, error) {
	var cs domain.ChatSession
	var last, pinned, deleted sql.NullTime
	if err := row.Scan(&cs.SessionID, &cs.AccountID, &cs.Title, &cs.Summary, &cs.MessageCount, &last, &pinned, &deleted,
		&cs.CreatedAt, &cs.UpdatedAt); err != nil {
		return domain.ChatSession{}, mapErr(err)
	}
	cs.LastMessageAt, cs.PinnedAt, cs.DeletedAt = timePtr(last), timePtr(pinned), timePtr(deleted)
	cs.CreatedAt, cs.UpdatedAt = cs.CreatedAt.UTC(), cs.UpdatedAt.UTC()
	return cs, nil
}

func (s *Store) CreateChatSession(ctx context.Context, cs domain.ChatSession) (domain.ChatSession, error) {
	if err := store.ValidateChatSession(cs); err != nil {
		return domain.ChatSession{}, err
	}
	if cs.SessionID == "" {
		cs.SessionID = domain.NewID(domain.PrefixSession)
	}
	now := s.timestamp()
	cs.MessageCount, cs.LastMessageAt, cs.PinnedAt, cs.DeletedAt = 0, nil, nil, nil
	cs.CreatedAt, cs.UpdatedAt = now, now
	if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO chat_sessions (session_id, account_id, title, summary, message_count,
		created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`, cs.SessionID, cs.AccountID, cs.Title, cs.Summary, now, now); err != nil {
		return domain.ChatSession{}, mapErr(err)
	}
	return cs, nil
}

func (s *Store) ListChatSessions(ctx context.Context, q store.ChatSessionQuery) ([]domain.ChatSession, int, error) {
	where := ` FROM chat_sessions s WHERE s.account_id = ? AND s.deleted_at IS NULL AND s.message_count > 0`
	args := []any{q.AccountID}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		// 表的排序规则不区分大小写
		pattern := "%" + likeEscape.Replace(kw) + "%"
		where += ` AND (s.title LIKE ? OR s.summary LIKE ? OR EXISTS (SELECT 1 FROM user_messages m
			WHERE m.session_id = s.session_id AND m.account_id = s.account_id AND m.content LIKE ?))`
		args = append(args, pattern, pattern, pattern)
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+sessionColumns+where+`
		ORDER BY s.pinned_at IS NULL, s.pinned_at DESC, COALESCE(s.last_message_at, s.created_at) DESC, s.session_id DESC
		LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.ChatSession{}
	for rows.Next() {
		cs, err := scanSession(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, cs)
	}
	return out, total, mapErr(rows.Err())
}

func (s *Store) GetChatSession(ctx context.Context, accountID, sessionID string) (domain.ChatSession, error) {
	return scanSession(s.q(ctx).QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM chat_sessions
		WHERE session_id = ? AND account_id = ? AND deleted_at IS NULL`, sessionID, accountID))
}

func (s *Store) lockSession(ctx context.Context, accountID, sessionID string) (domain.ChatSession, error) {
	return scanSession(s.q(ctx).QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM chat_sessions
		WHERE session_id = ? AND account_id = ? AND deleted_at IS NULL FOR UPDATE`, sessionID, accountID))
}

func (s *Store) UpdateChatSession(ctx context.Context, accountID, sessionID string, fn func(cs *domain.ChatSession) error) (domain.ChatSession, error) {
	var out domain.ChatSession
	err := s.WithTx(ctx, func(ctx context.Context) error {
		before, err := s.lockSession(ctx, accountID, sessionID)
		if err != nil {
			return err
		}
		cs := before
		if err := fn(&cs); err != nil {
			return err
		}
		after := before
		after.Title, after.Summary = cs.Title, cs.Summary
		after.PinnedAt, after.DeletedAt = truncPtr(cs.PinnedAt), truncPtr(cs.DeletedAt)
		if err := store.ValidateChatSession(after); err != nil {
			return err
		}
		after.UpdatedAt = s.timestamp()
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE chat_sessions SET title = ?, summary = ?, pinned_at = ?, deleted_at = ?, updated_at = ?
			WHERE session_id = ?`, after.Title, after.Summary, nullTime(after.PinnedAt), nullTime(after.DeletedAt), after.UpdatedAt, sessionID); err != nil {
			return mapErr(err)
		}
		out = after
		return nil
	})
	return out, err
}

func truncPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC().Truncate(time.Millisecond)
	return &v
}

// ---------- 消息与运行 ----------

const messageColumns = `m.message_id, m.session_id, m.account_id, m.client_message_id, m.content, m.attachments_json, m.created_at`

const runColumns = `r.run_id, r.session_id, r.message_id, r.account_id, r.trace_id, r.status, COALESCE(r.content, ''), r.blocks_json,
	r.followups_json, r.prompt_versions_json, r.error_code, r.created_at, r.updated_at`

func scanMessage(dest []any, m *domain.UserMessage, attachments *[]byte) []any {
	return append(dest, &m.MessageID, &m.SessionID, &m.AccountID, &m.ClientMessageID, &m.Content, attachments, &m.CreatedAt)
}

func finishMessage(m *domain.UserMessage, attachments []byte) error {
	m.Attachments = []domain.Attachment{}
	if err := fromJSON("user_messages.attachments_json", attachments, &m.Attachments); err != nil {
		return err
	}
	if m.Attachments == nil {
		m.Attachments = []domain.Attachment{}
	}
	m.CreatedAt = m.CreatedAt.UTC()
	return nil
}

// runScan 收集一行运行的扫描目标，finish 时解析 JSON 列。
type runScan struct {
	r                                 domain.AgentRun
	runID                             sql.NullString
	sessionID, messageID, accountID   sql.NullString
	traceID, status, content, errCode sql.NullString
	blocks, followups, prompts        []byte
	created, updated                  sql.NullTime
}

func (x *runScan) dest() []any {
	return []any{&x.runID, &x.sessionID, &x.messageID, &x.accountID, &x.traceID, &x.status, &x.content, &x.blocks, &x.followups,
		&x.prompts, &x.errCode, &x.created, &x.updated}
}

// finish 返回运行；LEFT JOIN 没有运行时返回 nil。
func (x *runScan) finish() (*domain.AgentRun, error) {
	if !x.runID.Valid {
		return nil, nil
	}
	r := domain.AgentRun{RunID: x.runID.String, SessionID: x.sessionID.String, MessageID: x.messageID.String, AccountID: x.accountID.String,
		TraceID: x.traceID.String, Status: domain.RunStatus(x.status.String), Content: x.content.String, ErrorCode: x.errCode.String,
		CreatedAt: x.created.Time.UTC(), UpdatedAt: x.updated.Time.UTC()}
	if err := fromJSON("agent_runs.blocks_json", x.blocks, &r.Blocks); err != nil {
		return nil, err
	}
	if err := fromJSON("agent_runs.followups_json", x.followups, &r.Followups); err != nil {
		return nil, err
	}
	if err := fromJSON("agent_runs.prompt_versions_json", x.prompts, &r.PromptVersions); err != nil {
		return nil, err
	}
	if len(r.Blocks) == 0 {
		r.Blocks = nil
	}
	if len(r.Followups) == 0 {
		r.Followups = nil
	}
	if len(r.PromptVersions) == 0 {
		r.PromptVersions = nil
	}
	return &r, nil
}

func (s *Store) ListChatMessages(ctx context.Context, accountID, sessionID string) ([]store.ChatTurn, error) {
	if _, err := s.GetChatSession(ctx, accountID, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+messageColumns+`, `+runColumns+` FROM user_messages m
		LEFT JOIN agent_runs r ON r.message_id = m.message_id AND r.account_id = m.account_id
		WHERE m.session_id = ? AND m.account_id = ? ORDER BY m.seq`, sessionID, accountID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.ChatTurn{}
	for rows.Next() {
		var m domain.UserMessage
		var att []byte
		var rs runScan
		if err := rows.Scan(append(scanMessage(nil, &m, &att), rs.dest()...)...); err != nil {
			return nil, mapErr(err)
		}
		if err := finishMessage(&m, att); err != nil {
			return nil, err
		}
		run, err := rs.finish()
		if err != nil {
			return nil, err
		}
		out = append(out, store.ChatTurn{Message: m, Run: run})
	}
	return out, mapErr(rows.Err())
}

func (s *Store) getRun(ctx context.Context, where string, args ...any) (domain.AgentRun, error) {
	var rs runScan
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT `+runColumns+` FROM agent_runs r WHERE `+where, args...).Scan(rs.dest()...); err != nil {
		return domain.AgentRun{}, mapErr(err)
	}
	r, err := rs.finish()
	if err != nil {
		return domain.AgentRun{}, err
	}
	return *r, nil
}

func (s *Store) StartAgentRun(ctx context.Context, msg domain.UserMessage, fn func(cs *domain.ChatSession)) (store.StartedRun, error) {
	if err := store.ValidateUserMessage(msg); err != nil {
		return store.StartedRun{}, err
	}
	var out store.StartedRun
	err := s.WithTx(ctx, func(ctx context.Context) error {
		// 锁会话：同一会话的提交串行，重复的 client_message_id 一定能看到前一次写入的消息
		cs, err := s.lockSession(ctx, msg.AccountID, msg.SessionID)
		if err != nil {
			return err
		}
		var existing domain.UserMessage
		var att []byte
		err = s.q(ctx).QueryRowContext(ctx, `SELECT `+messageColumns+` FROM user_messages m
			WHERE m.account_id = ? AND m.session_id = ? AND m.client_message_id = ?`, msg.AccountID, msg.SessionID, msg.ClientMessageID).
			Scan(scanMessage(nil, &existing, &att)...)
		switch {
		case err == nil:
			if err := finishMessage(&existing, att); err != nil {
				return err
			}
			run, err := s.getRun(ctx, `r.account_id = ? AND r.message_id = ?`, msg.AccountID, existing.MessageID)
			if err != nil {
				return fmt.Errorf("message %s run: %w", existing.MessageID, err)
			}
			out = store.StartedRun{Message: existing, Run: run, Created: false}
			return nil
		case err != sql.ErrNoRows:
			return mapErr(err)
		}

		now := s.timestamp()
		msg.MessageID = domain.NewID(domain.PrefixMessage)
		msg.CreatedAt = now
		if msg.Attachments == nil {
			msg.Attachments = []domain.Attachment{}
		}
		attJSON, err := jsonArray(msg.Attachments)
		if err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO user_messages (message_id, session_id, account_id, client_message_id, content,
			attachments_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, msg.MessageID, msg.SessionID, msg.AccountID, msg.ClientMessageID,
			msg.Content, attJSON, now); err != nil {
			return mapErr(err)
		}
		run := domain.AgentRun{RunID: domain.NewID(domain.PrefixRun), SessionID: msg.SessionID, MessageID: msg.MessageID,
			AccountID: msg.AccountID, TraceID: domain.NewID(domain.PrefixTrace), Status: domain.RunRunning, CreatedAt: now, UpdatedAt: now}
		if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO agent_runs (run_id, session_id, message_id, account_id, trace_id, status,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, run.RunID, run.SessionID, run.MessageID, run.AccountID, run.TraceID,
			run.Status, now, now); err != nil {
			return mapErr(err)
		}
		before := cs
		if fn != nil {
			fn(&cs)
		}
		cs.SessionID, cs.AccountID, cs.CreatedAt = before.SessionID, before.AccountID, before.CreatedAt
		cs.MessageCount, cs.LastMessageAt, cs.UpdatedAt = before.MessageCount+1, &now, now
		if err := store.ValidateChatSession(cs); err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE chat_sessions SET title = ?, summary = ?, message_count = ?, last_message_at = ?,
			updated_at = ? WHERE session_id = ?`, cs.Title, cs.Summary, cs.MessageCount, now, now, cs.SessionID); err != nil {
			return mapErr(err)
		}
		out = store.StartedRun{Message: msg, Run: run, Created: true}
		return nil
	})
	return out, err
}

func (s *Store) GetAgentRun(ctx context.Context, accountID, runID string) (domain.AgentRun, error) {
	return s.getRun(ctx, `r.run_id = ? AND r.account_id = ?`, runID, accountID)
}

func (s *Store) UpdateAgentRun(ctx context.Context, accountID, runID string, fn func(r *domain.AgentRun) error) (domain.AgentRun, error) {
	var out domain.AgentRun
	err := s.WithTx(ctx, func(ctx context.Context) error {
		before, err := s.getRun(ctx, `r.run_id = ? AND r.account_id = ? FOR UPDATE`, runID, accountID)
		if err != nil {
			return err
		}
		r := before
		r.Blocks = append([]map[string]any(nil), before.Blocks...)
		r.Followups = append([]string(nil), before.Followups...)
		if err := fn(&r); err != nil {
			return err
		}
		if err := store.NextRunState(before.Status, r.Status); err != nil {
			return err
		}
		r.RunID, r.SessionID, r.MessageID, r.AccountID, r.TraceID, r.CreatedAt =
			before.RunID, before.SessionID, before.MessageID, before.AccountID, before.TraceID, before.CreatedAt
		r.UpdatedAt = s.timestamp()
		blocks, err := nullableJSON(r.Blocks)
		if err != nil {
			return err
		}
		followups, err := nullableJSON(r.Followups)
		if err != nil {
			return err
		}
		prompts, err := nullableJSON(r.PromptVersions)
		if err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE agent_runs SET status = ?, content = ?, blocks_json = ?, followups_json = ?,
			prompt_versions_json = ?, error_code = ?, updated_at = ? WHERE run_id = ?`, r.Status, r.Content, blocks, followups, prompts,
			r.ErrorCode, r.UpdatedAt, runID); err != nil {
			return mapErr(err)
		}
		// 与读回的值保持一致（JSON 列里数字读回为 float64）
		out, err = s.getRun(ctx, `r.run_id = ? AND r.account_id = ?`, runID, accountID)
		return err
	})
	return out, err
}

func (s *Store) AppendTraceEvent(ctx context.Context, e domain.AgentTraceEvent) (domain.AgentTraceEvent, error) {
	if e.RunID == "" || e.AccountID == "" || e.Stage == "" || e.EventType == "" {
		return domain.AgentTraceEvent{}, fmt.Errorf("%w: 轨迹缺少运行、账户、阶段或类型", store.ErrInvalid)
	}
	if e.TraceEventID == "" {
		e.TraceEventID = domain.NewID(domain.PrefixTraceEvt)
	}
	e.CreatedAt = s.orNow(e.CreatedAt)
	meta, err := nullableJSON(e.Metadata)
	if err != nil {
		return domain.AgentTraceEvent{}, err
	}
	var errText any
	if e.Error != "" {
		errText = e.Error
	}
	if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO agent_trace_events (trace_event_id, run_id, trace_id, account_id, stage, event_type,
		model, status, duration_ms, error, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.TraceEventID, e.RunID,
		e.TraceID, e.AccountID, e.Stage, e.EventType, e.Model, e.Status, e.DurationMS, errText, meta, e.CreatedAt); err != nil {
		return domain.AgentTraceEvent{}, mapErr(err)
	}
	return e, nil
}

func (s *Store) ListTraceEvents(ctx context.Context, accountID, runID string) ([]domain.AgentTraceEvent, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT trace_event_id, run_id, trace_id, account_id, stage, event_type, model, status,
		duration_ms, COALESCE(error, ''), metadata_json, created_at FROM agent_trace_events WHERE run_id = ? AND account_id = ? ORDER BY seq`,
		runID, accountID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.AgentTraceEvent{}
	for rows.Next() {
		var e domain.AgentTraceEvent
		var meta []byte
		if err := rows.Scan(&e.TraceEventID, &e.RunID, &e.TraceID, &e.AccountID, &e.Stage, &e.EventType, &e.Model, &e.Status,
			&e.DurationMS, &e.Error, &meta, &e.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		if err := fromJSON("agent_trace_events.metadata_json", meta, &e.Metadata); err != nil {
			return nil, err
		}
		e.CreatedAt = e.CreatedAt.UTC()
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

func (s *Store) FailInterruptedRuns(ctx context.Context, errorCode string, createdBefore time.Time) (int, error) {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE agent_runs SET status = ?, error_code = ?, updated_at = ?
		WHERE status IN (?, ?) AND created_at < ?`, domain.RunFailed, errorCode, s.timestamp(), domain.RunQueued, domain.RunRunning,
		createdBefore.UTC())
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}
