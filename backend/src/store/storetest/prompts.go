package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func promptCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"PromptLifecycle", testPromptLifecycle},
		{"PromptConcurrentPublish", testPromptConcurrentPublish},
	}
}

func draft(t *testing.T, s store.Store, key, content string) domain.AgentPrompt {
	t.Helper()
	p, err := s.SavePromptDraft(context.Background(), domain.AgentPrompt{PromptKey: key, Title: "规划", Content: content, CreatedBy: "acct_admin"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sw(t *testing.T, s store.Store, key string, version, expected int, action string) (*domain.AgentPrompt, domain.AgentPromptPublishRecord, error) {
	t.Helper()
	return s.SwitchPrompt(context.Background(), store.PromptSwitch{Key: key, Version: version, ExpectedActive: expected, Action: action, By: "acct_admin",
		Reason: "测试", Target: "database"})
}

func testPromptLifecycle(t *testing.T, s store.Store) {
	ctx := context.Background()
	key := "agent.planner"
	if _, err := s.ActivePrompt(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no active yet: %v", err)
	}
	// 草稿：第一次新建版本 1，再保存原地更新
	d := draft(t, s, key, "v1 草稿")
	d2 := draft(t, s, key, "v1 改过")
	if d.Version != 1 || d2.PromptID != d.PromptID || d2.Content != "v1 改过" || d2.Status != domain.PromptDraft {
		t.Fatalf("draft: %+v %+v", d, d2)
	}
	// 期望版本不对：拒绝；发布草稿：成为 active
	if _, _, err := sw(t, s, key, 1, 3, domain.PromptActionPublish); !errors.Is(err, store.ErrPromptActiveChanged) {
		t.Fatalf("stale expected: %v", err)
	}
	active, rec, err := sw(t, s, key, 1, 0, domain.PromptActionPublish)
	if err != nil || active == nil || active.Version != 1 || active.Status != domain.PromptActive || active.PublishedAt == nil ||
		rec.FromVersion != 0 || rec.Version != 1 || rec.Result != domain.PublishPending || rec.Action != domain.PromptActionPublish {
		t.Fatalf("publish v1: %+v %+v %v", active, rec, err)
	}
	if got, err := s.ActivePrompt(ctx, key); err != nil || got.Content != "v1 改过" {
		t.Fatalf("active: %+v %v", got, err)
	}
	// 发布后再保存：新建版本 2 的草稿，v1 不受影响
	d3 := draft(t, s, key, "v2")
	if d3.Version != 2 {
		t.Fatalf("v2 draft: %+v", d3)
	}
	// 状态不对：不能“发布”已生效的版本、不能“回滚”到草稿；不存在的版本
	if _, _, err := sw(t, s, key, 1, 1, domain.PromptActionPublish); !errors.Is(err, store.ErrPromptState) {
		t.Fatalf("publish active: %v", err)
	}
	if _, _, err := sw(t, s, key, 2, 1, domain.PromptActionRollback); !errors.Is(err, store.ErrPromptState) {
		t.Fatalf("rollback to draft: %v", err)
	}
	if _, _, err := sw(t, s, key, 9, 1, domain.PromptActionPublish); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
	// 发布 v2：v1 归档；回滚到 v1：v2 归档；回到内置（0）：没有 active
	if _, _, err := sw(t, s, key, 2, 1, domain.PromptActionPublish); err != nil {
		t.Fatal(err)
	}
	active, rec, err = sw(t, s, key, 1, 2, domain.PromptActionRollback)
	if err != nil || active.Version != 1 || rec.FromVersion != 2 || rec.Action != domain.PromptActionRollback {
		t.Fatalf("rollback: %+v %+v %v", active, rec, err)
	}
	active, rec, err = sw(t, s, key, 0, 1, domain.PromptActionRollback)
	if err != nil || active != nil || rec.Version != 0 || rec.PromptID != "" {
		t.Fatalf("builtin: %+v %+v %v", active, rec, err)
	}
	if _, err := s.ActivePrompt(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("builtin active: %v", err)
	}
	versions, err := s.ListPrompts(ctx, key)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[0].Status != domain.PromptArchived || versions[1].Status != domain.PromptArchived {
		t.Fatalf("versions: %+v %v", versions, err)
	}
	// 推送结果回写；记录按写入先后倒序（同一毫秒也有序）
	if err := s.UpdatePromptPublishResult(ctx, rec.RecordID, domain.PublishFallback, "nacos_unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePromptPublishResult(ctx, "pub_missing", domain.PublishSuccess, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing record: %v", err)
	}
	recs, total, err := s.ListPromptPublishRecords(ctx, key, store.Page{Page: 1, PageSize: 2})
	if err != nil || total != 4 || len(recs) != 2 || recs[0].RecordID != rec.RecordID || recs[0].Result != domain.PublishFallback ||
		recs[0].Detail != "nacos_unavailable" || recs[1].Action != domain.PromptActionRollback || recs[1].Version != 1 {
		t.Fatalf("records: %d %+v %v", total, recs, err)
	}
	// 别的 key 互不影响
	if other, _ := s.ListPrompts(ctx, "agent.react"); len(other) != 0 {
		t.Fatalf("other key: %+v", other)
	}
	if all, _ := s.ListPrompts(ctx, ""); len(all) != 2 {
		t.Fatalf("all: %d", len(all))
	}
}

// 两个管理员同时发布（都以为当前是内置）：只有一个成功，另一个得到 ErrPromptActiveChanged；最终只有一个 active。
func testPromptConcurrentPublish(t *testing.T, s store.Store) {
	ctx := context.Background()
	key := "agent.react"
	draft(t, s, key, "并发 v1")
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, results[i] = s.SwitchPrompt(ctx, store.PromptSwitch{Key: key, Version: 1, ExpectedActive: 0, Action: domain.PromptActionPublish,
				By: fmt.Sprintf("acct_%d", i), Target: "database"})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrPromptActiveChanged), errors.Is(err, store.ErrPromptState):
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	versions, _ := s.ListPrompts(ctx, key)
	actives := 0
	for _, p := range versions {
		if p.Status == domain.PromptActive {
			actives++
		}
	}
	_, total, _ := s.ListPromptPublishRecords(ctx, key, store.Page{Page: 1, PageSize: 10})
	if ok != 1 || actives != 1 || total != 1 {
		t.Fatalf("ok=%d actives=%d records=%d", ok, actives, total)
	}
}
