package memstore

import (
	"context"
	"sort"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func (s *Store) promptsOf(key string) []domain.AgentPrompt {
	var out []domain.AgentPrompt
	for _, p := range s.data.prompts {
		if key == "" || p.PromptKey == key {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PromptKey != out[j].PromptKey {
			return out[i].PromptKey < out[j].PromptKey
		}
		return out[i].Version > out[j].Version
	})
	return out
}

func (s *Store) ListPrompts(ctx context.Context, key string) ([]domain.AgentPrompt, error) {
	defer s.lock(ctx)()
	out := s.promptsOf(key)
	if out == nil {
		out = []domain.AgentPrompt{}
	}
	return out, nil
}

func (s *Store) ActivePrompt(ctx context.Context, key string) (domain.AgentPrompt, error) {
	defer s.lock(ctx)()
	for _, p := range s.promptsOf(key) {
		if p.Status == domain.PromptActive {
			return p, nil
		}
	}
	return domain.AgentPrompt{}, store.ErrNotFound
}

func (s *Store) SavePromptDraft(ctx context.Context, in domain.AgentPrompt) (domain.AgentPrompt, error) {
	defer s.lock(ctx)()
	now := s.timestamp()
	versions := s.promptsOf(in.PromptKey)
	if len(versions) > 0 && versions[0].Status == domain.PromptDraft {
		d := versions[0]
		d.Title, d.Content, d.Description, d.CreatedBy, d.UpdatedAt = in.Title, in.Content, in.Description, in.CreatedBy, now
		s.data.prompts[d.PromptID] = d
		return d, nil
	}
	next := 1
	if len(versions) > 0 {
		next = versions[0].Version + 1
	}
	d := domain.AgentPrompt{PromptID: domain.NewID(domain.PrefixPrompt), PromptKey: in.PromptKey, Title: in.Title, Content: in.Content,
		Status: domain.PromptDraft, Version: next, Description: in.Description, CreatedBy: in.CreatedBy, CreatedAt: now, UpdatedAt: now}
	s.data.prompts[d.PromptID] = d
	return d, nil
}

func (s *Store) SwitchPrompt(ctx context.Context, sw store.PromptSwitch) (*domain.AgentPrompt, domain.AgentPromptPublishRecord, error) {
	defer s.lock(ctx)()
	versions := s.promptsOf(sw.Key)
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
		return nil, domain.AgentPromptPublishRecord{}, store.ErrPromptActiveChanged
	}
	if err := checkSwitch(sw, versions, target); err != nil {
		return nil, domain.AgentPromptPublishRecord{}, err
	}
	now := s.timestamp()
	for _, p := range versions {
		if p.Status == domain.PromptActive {
			p.Status, p.UpdatedAt = domain.PromptArchived, now
			s.data.prompts[p.PromptID] = p
		}
	}
	rec := domain.AgentPromptPublishRecord{RecordID: domain.NewID(domain.PrefixPublish), PromptKey: sw.Key, Version: sw.Version, Action: sw.Action,
		FromVersion: current, PublishedBy: sw.By, Reason: sw.Reason, Target: sw.Target, Result: domain.PublishPending, CreatedAt: now}
	var active *domain.AgentPrompt
	if sw.Version > 0 {
		p := versions[target]
		p.Status, p.PublishedAt, p.UpdatedAt = domain.PromptActive, &now, now
		s.data.prompts[p.PromptID] = p
		rec.PromptID = p.PromptID
		active = &p
	}
	s.data.publishes[rec.RecordID] = rec
	s.data.publishSeq[rec.RecordID] = int64(len(s.data.publishSeq)) + 1
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
	defer s.lock(ctx)()
	rec, ok := s.data.publishes[recordID]
	if !ok {
		return store.ErrNotFound
	}
	rec.Result, rec.Detail = result, detail
	s.data.publishes[recordID] = rec
	return nil
}

func (s *Store) ListPromptPublishRecords(ctx context.Context, key string, page store.Page) ([]domain.AgentPromptPublishRecord, int, error) {
	defer s.lock(ctx)()
	var all []domain.AgentPromptPublishRecord
	for _, r := range s.data.publishes {
		if key == "" || r.PromptKey == key {
			all = append(all, r)
		}
	}
	sort.Slice(all, func(i, j int) bool { return s.data.publishSeq[all[i].RecordID] > s.data.publishSeq[all[j].RecordID] })
	total := len(all)
	start := min(page.Offset(), total)
	end := min(start+page.PageSize, total)
	out := append([]domain.AgentPromptPublishRecord{}, all[start:end]...)
	return out, total, nil
}
