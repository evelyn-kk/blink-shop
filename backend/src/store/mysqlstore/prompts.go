package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const promptColumns = `prompt_id, prompt_key, title, content, status, version, COALESCE(description, ''), created_by, published_at, created_at, updated_at`

func scanPrompt(sc interface{ Scan(...any) error }) (domain.AgentPrompt, error) {
	var p domain.AgentPrompt
	var published sql.NullTime
	var created, updated time.Time
	if err := sc.Scan(&p.PromptID, &p.PromptKey, &p.Title, &p.Content, &p.Status, &p.Version, &p.Description, &p.CreatedBy, &published, &created, &updated); err != nil {
		return p, err
	}
	p.CreatedAt, p.UpdatedAt = created.UTC(), updated.UTC()
	if published.Valid {
		t := published.Time.UTC()
		p.PublishedAt = &t
	}
	return p, nil
}

// promptsOf 读取 key 的全部版本（版本降序）；lock 为 true 时在当前事务里加行锁（FOR UPDATE），串行化同一 key 的写操作。
func (s *Store) promptsOf(ctx context.Context, key string, lock bool) ([]domain.AgentPrompt, error) {
	q := `SELECT ` + promptColumns + ` FROM agent_prompts`
	var args []any
	if key != "" {
		q += ` WHERE prompt_key = ?`
		args = append(args, key)
	}
	q += ` ORDER BY prompt_key ASC, version DESC`
	if lock {
		q += ` FOR UPDATE`
	}
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.AgentPrompt{}
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListPrompts(ctx context.Context, key string) ([]domain.AgentPrompt, error) {
	return s.promptsOf(ctx, key, false)
}

func (s *Store) ActivePrompt(ctx context.Context, key string) (domain.AgentPrompt, error) {
	p, err := scanPrompt(s.q(ctx).QueryRowContext(ctx, `SELECT `+promptColumns+` FROM agent_prompts WHERE prompt_key = ? AND status = ? LIMIT 1`,
		key, domain.PromptActive))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentPrompt{}, store.ErrNotFound
	}
	return p, mapErr(err)
}

func (s *Store) SavePromptDraft(ctx context.Context, in domain.AgentPrompt) (domain.AgentPrompt, error) {
	var out domain.AgentPrompt
	save := func() error {
		return s.WithTx(ctx, func(ctx context.Context) error {
			versions, err := s.promptsOf(ctx, in.PromptKey, true)
			if err != nil {
				return err
			}
			now := s.orNow(time.Time{})
			if len(versions) > 0 && versions[0].Status == domain.PromptDraft {
				d := versions[0]
				d.Title, d.Content, d.Description, d.CreatedBy, d.UpdatedAt = in.Title, in.Content, in.Description, in.CreatedBy, now
				if _, err := s.q(ctx).ExecContext(ctx, `UPDATE agent_prompts SET title = ?, content = ?, description = ?, created_by = ?, updated_at = ? WHERE prompt_id = ?`,
					d.Title, d.Content, d.Description, d.CreatedBy, d.UpdatedAt, d.PromptID); err != nil {
					return mapErr(err)
				}
				out = d
				return nil
			}
			next := 1
			if len(versions) > 0 {
				next = versions[0].Version + 1
			}
			d := domain.AgentPrompt{PromptID: domain.NewID(domain.PrefixPrompt), PromptKey: in.PromptKey, Title: in.Title, Content: in.Content,
				Status: domain.PromptDraft, Version: next, Description: in.Description, CreatedBy: in.CreatedBy, CreatedAt: now, UpdatedAt: now}
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO agent_prompts (prompt_id, prompt_key, title, content, status, version, description, created_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.PromptID, d.PromptKey, d.Title, d.Content, d.Status, d.Version, d.Description, d.CreatedBy, d.CreatedAt, d.UpdatedAt); err != nil {
				return mapErr(err)
			}
			out = d
			return nil
		})
	}
	err := save()
	// key 还没有任何版本时 FOR UPDATE 锁不到行，两个请求可能同时插入版本 1：唯一键冲突后重试一次（这次能读到对方的草稿）
	if errors.Is(err, store.ErrConflict) {
		err = save()
	}
	return out, err
}

func (s *Store) SwitchPrompt(ctx context.Context, sw store.PromptSwitch) (*domain.AgentPrompt, domain.AgentPromptPublishRecord, error) {
	var active *domain.AgentPrompt
	var rec domain.AgentPromptPublishRecord
	err := s.WithTx(ctx, func(ctx context.Context) error {
		versions, err := s.promptsOf(ctx, sw.Key, true)
		if err != nil {
			return err
		}
		current, target := 0, -1
		for i, p := range versions {
			if p.Status == domain.PromptActive {
				current = p.Version
			}
			if p.Version == sw.Version {
				target = i
			}
		}
		if current != sw.ExpectedActive {
			return store.ErrPromptActiveChanged
		}
		if err := checkSwitch(sw, versions, target); err != nil {
			return err
		}
		now := s.orNow(time.Time{})
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE agent_prompts SET status = ?, updated_at = ? WHERE prompt_key = ? AND status = ?`,
			domain.PromptArchived, now, sw.Key, domain.PromptActive); err != nil {
			return mapErr(err)
		}
		rec = domain.AgentPromptPublishRecord{RecordID: domain.NewID(domain.PrefixPublish), PromptKey: sw.Key, Version: sw.Version, Action: sw.Action,
			FromVersion: current, PublishedBy: sw.By, Reason: sw.Reason, Target: sw.Target, Result: domain.PublishPending, CreatedAt: now}
		if sw.Version > 0 {
			p := versions[target]
			p.Status, p.PublishedAt, p.UpdatedAt = domain.PromptActive, &now, now
			if _, err := s.q(ctx).ExecContext(ctx, `UPDATE agent_prompts SET status = ?, published_at = ?, updated_at = ? WHERE prompt_id = ?`,
				p.Status, now, now, p.PromptID); err != nil {
				return mapErr(err)
			}
			rec.PromptID = p.PromptID
			active = &p
		}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO agent_prompt_publish_records (record_id, prompt_key, prompt_id, version, action, from_version,
			published_by, reason, target, result, detail, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.RecordID, rec.PromptKey, rec.PromptID, rec.Version, rec.Action, rec.FromVersion, rec.PublishedBy, rec.Reason, rec.Target, rec.Result, rec.Detail, rec.CreatedAt)
		return mapErr(err)
	})
	if err != nil {
		return nil, domain.AgentPromptPublishRecord{}, err
	}
	return active, rec, nil
}

// checkSwitch 校验目标版本：publish 只能发布草稿；rollback 只能回到已归档版本，或 Version=0 回到内置默认。
func checkSwitch(sw store.PromptSwitch, versions []domain.AgentPrompt, target int) error {
	switch {
	case sw.Action == domain.PromptActionRollback && sw.Version == 0:
		return nil
	case target < 0:
		return store.ErrNotFound
	case sw.Action == domain.PromptActionPublish && versions[target].Status != domain.PromptDraft,
		sw.Action == domain.PromptActionRollback && versions[target].Status != domain.PromptArchived,
		sw.Action != domain.PromptActionPublish && sw.Action != domain.PromptActionRollback:
		return store.ErrPromptState
	}
	return nil
}

func (s *Store) UpdatePromptPublishResult(ctx context.Context, recordID, result, detail string) error {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE agent_prompt_publish_records SET result = ?, detail = ? WHERE record_id = ?`, result, detail, recordID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

const publishColumns = `record_id, prompt_key, prompt_id, version, action, from_version, published_by, reason, target, result, detail, created_at`

func (s *Store) ListPromptPublishRecords(ctx context.Context, key string, page store.Page) ([]domain.AgentPromptPublishRecord, int, error) {
	where := ` FROM agent_prompt_publish_records`
	var args []any
	if key != "" {
		where += ` WHERE prompt_key = ?`
		args = append(args, key)
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+publishColumns+where+` ORDER BY seq DESC LIMIT ? OFFSET ?`,
		append(args, page.PageSize, page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.AgentPromptPublishRecord{}
	for rows.Next() {
		var r domain.AgentPromptPublishRecord
		var created time.Time
		if err := rows.Scan(&r.RecordID, &r.PromptKey, &r.PromptID, &r.Version, &r.Action, &r.FromVersion, &r.PublishedBy, &r.Reason, &r.Target,
			&r.Result, &r.Detail, &created); err != nil {
			return nil, 0, err
		}
		r.CreatedAt = created.UTC()
		out = append(out, r)
	}
	return out, total, rows.Err()
}
