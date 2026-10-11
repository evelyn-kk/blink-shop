package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

const promptsPath = "/api/v1/admin/prompts"

type promptListResponse struct {
	Items          []promptSummaryView               `json:"items"`
	PublishRecords []domain.AgentPromptPublishRecord `json:"publish_records"`
}

type promptSwitchResponse struct {
	Prompt promptDetailView                `json:"prompt"`
	Record domain.AgentPromptPublishRecord `json:"record"`
}

func TestAdminPromptLifecycle(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	key := promptsPath + "/" + agent.PromptReact

	// 列表：4 个 Prompt，都是内置默认
	list := decodeBody[promptListResponse](t, ts.call(t, http.MethodGet, promptsPath, admin, nil))
	if len(list.Items) != 4 || list.Items[1].PromptKey != agent.PromptReact || list.Items[1].ActiveSource != "builtin" || list.Items[1].ActiveVersion != 0 ||
		list.Items[1].Draft != nil || len(list.Items[1].Placeholders) != 1 {
		t.Fatalf("list: %+v", list)
	}
	expectStatus(t, ts.call(t, http.MethodGet, promptsPath+"/agent.nope", admin, nil), http.StatusNotFound, "prompt_not_found")

	// 草稿校验：工具循环必须保留 {{TOOLS}}
	expectStatus(t, ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "你是导购"}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPatch, key, admin, map[string]any{}), http.StatusBadRequest, "invalid_argument")
	rec := ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "新的工具循环规则 v1\n{{TOOLS}}", "description": "加强规则"})
	expectStatus(t, rec, http.StatusOK, "")
	d := decodeBody[promptVersionView](t, rec)
	if d.Version != 1 || d.Status != domain.PromptDraft || d.Title != "工具循环" {
		t.Fatalf("draft: %+v", d)
	}
	// 再保存覆盖同一个草稿
	d = decodeBody[promptVersionView](t, ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "新的工具循环规则 v1（改）\n{{TOOLS}}"}))
	if d.Version != 1 {
		t.Fatalf("draft overwrite: %+v", d)
	}

	// 发布：必须给出看到的生效版本；版本不对 409；发布不存在的版本 404；发布内置 400
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1, "expected_active_version": 3}), http.StatusConflict, "prompt_active_changed")
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 9, "expected_active_version": 0}), http.StatusNotFound, "prompt_version_not_found")
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 0, "expected_active_version": 0}), http.StatusBadRequest, "invalid_argument")
	rec = ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1, "expected_active_version": 0, "reason": "上线新规则"})
	expectStatus(t, rec, http.StatusOK, "")
	sw := decodeBody[promptSwitchResponse](t, rec)
	if sw.Prompt.ActiveVersion != 1 || sw.Prompt.ActiveSource != "published" || sw.Record.Result != domain.PublishSuccess || sw.Record.Target != "database" ||
		sw.Record.FromVersion != 0 || sw.Record.Action != domain.PromptActionPublish || sw.Prompt.Versions[0].Status != domain.PromptActive {
		t.Fatalf("publish: %+v", sw)
	}
	// 本实例立即生效
	if content, v := ts.Server.prompts.Prompt(context.Background(), agent.PromptReact); v != 1 || !strings.HasPrefix(content, "新的工具循环规则 v1（改）") {
		t.Fatalf("runtime after publish: v%d", v)
	}
	// 发布已生效的版本：409；回滚到草稿 / 未填原因：拒绝
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1, "expected_active_version": 1}), http.StatusConflict, "prompt_state_conflict")
	expectStatus(t, ts.call(t, http.MethodPost, key+"/rollback", admin, map[string]any{"version": 0, "expected_active_version": 1}), http.StatusBadRequest, "invalid_argument")

	// v2 发布后回滚到 v1，再回到内置默认
	ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "v2 规则\n{{TOOLS}}"})
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 2, "expected_active_version": 1}), http.StatusOK, "")
	sw = decodeBody[promptSwitchResponse](t, ts.call(t, http.MethodPost, key+"/rollback", admin, map[string]any{"version": 1, "expected_active_version": 2, "reason": "v2 回答变差"}))
	if sw.Prompt.ActiveVersion != 1 || sw.Record.Action != domain.PromptActionRollback || sw.Record.FromVersion != 2 {
		t.Fatalf("rollback: %+v", sw.Record)
	}
	sw = decodeBody[promptSwitchResponse](t, ts.call(t, http.MethodPost, key+"/rollback", admin, map[string]any{"version": 0, "expected_active_version": 1, "reason": "回到内置"}))
	if sw.Prompt.ActiveSource != "builtin" || sw.Record.Version != 0 {
		t.Fatalf("builtin: %+v", sw)
	}
	if _, v := ts.Server.prompts.Prompt(context.Background(), agent.PromptReact); v != 0 {
		t.Fatalf("runtime after builtin rollback: v%d", v)
	}
	detail := decodeBody[promptDetailView](t, ts.call(t, http.MethodGet, key, admin, nil))
	if len(detail.Versions) != 2 || len(detail.PublishRecords) != 4 || detail.BuiltinContent == "" || detail.PublishRecords[0].Reason != "回到内置" {
		t.Fatalf("detail: versions=%d records=%d", len(detail.Versions), len(detail.PublishRecords))
	}
	// 审计：草稿、发布、回滚都有记录，管理端可以按对象类型 prompt 筛选
	logs := decodeBody[pageResponse[auditView]](t, ts.call(t, http.MethodGet, "/api/v1/admin/audit-logs?target_type=prompt&page_size=50", admin, nil))
	actions := map[string]int{}
	for _, l := range logs.Items {
		actions[l.Action]++
	}
	if actions["prompt.draft_saved"] != 3 || actions["prompt.published"] != 2 || actions["prompt.rolled_back"] != 2 {
		t.Fatalf("audit: %v", actions)
	}

	// 只有管理员能看和改
	for _, u := range []string{seed.MerchantUsername, seed.UserUsername} {
		tok := ts.login(t, u, seed.DevPassword).Token
		expectStatus(t, ts.call(t, http.MethodGet, promptsPath, tok, nil), http.StatusForbidden, "")
		expectStatus(t, ts.call(t, http.MethodPatch, key, tok, map[string]any{"content": "x {{TOOLS}}"}), http.StatusForbidden, "")
		expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", tok, map[string]any{"version": 1, "expected_active_version": 0}), http.StatusForbidden, "")
	}
	expectStatus(t, ts.call(t, http.MethodGet, promptsPath, "", nil), http.StatusUnauthorized, "")
}

// 并发发布同一个草稿：只有一个成功，其余 409，只有一条发布记录。
func TestAdminPromptConcurrentPublish(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	key := promptsPath + "/" + agent.PromptPlanner
	expectStatus(t, ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "规划 v1"}), http.StatusOK, "")
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1, "expected_active_version": 0}).Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		} else if c != http.StatusConflict {
			t.Fatalf("unexpected status %d", c)
		}
	}
	detail := decodeBody[promptDetailView](t, ts.call(t, http.MethodGet, key, admin, nil))
	if ok != 1 || len(detail.PublishRecords) != 1 || detail.ActiveVersion != 1 {
		t.Fatalf("ok=%d records=%d active=%d", ok, len(detail.PublishRecords), detail.ActiveVersion)
	}
}

type fakeMirror struct {
	mu     sync.Mutex
	fail   bool
	pushed map[string]string
}

func (m *fakeMirror) Target(key string) string { return "blink_shop.prompt." + key }
func (m *fakeMirror) Publish(_ context.Context, key string, version int, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("nacos down")
	}
	m.pushed[key] = content
	return nil
}

// 配置中心推送失败：发布照常生效（运行时以数据库为准），记录为 fallback；恢复后的发布记为 success 并推送内容。
func TestAdminPromptMirrorFallback(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	mirror := &fakeMirror{fail: true, pushed: map[string]string{}}
	ts.Server.promptMirror = mirror
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	key := promptsPath + "/" + agent.PromptSummary
	ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "摘要 v1"})
	sw := decodeBody[promptSwitchResponse](t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1, "expected_active_version": 0}))
	if sw.Record.Result != domain.PublishFallback || sw.Record.Target != "blink_shop.prompt.agent.summary" || sw.Prompt.ActiveVersion != 1 {
		t.Fatalf("fallback: %+v", sw.Record)
	}
	if _, v := ts.Server.prompts.Prompt(context.Background(), agent.PromptSummary); v != 1 {
		t.Fatal("publish not effective while config center down")
	}
	if recs := decodeBody[promptDetailView](t, ts.call(t, http.MethodGet, key, admin, nil)).PublishRecords; recs[0].Result != domain.PublishFallback || recs[0].Detail != "config_center_unavailable" {
		t.Fatalf("stored record: %+v", recs[0])
	}
	mirror.fail = false
	sw = decodeBody[promptSwitchResponse](t, ts.call(t, http.MethodPost, key+"/rollback", admin, map[string]any{"version": 0, "expected_active_version": 1, "reason": "回退"}))
	if sw.Record.Result != domain.PublishSuccess || !strings.HasPrefix(mirror.pushed[agent.PromptSummary], "你是导购会话摘要器") {
		t.Fatalf("mirror after recovery: %+v %q", sw.Record, mirror.pushed)
	}
}

// 发布后新运行用新版本（模型收到新内容，轨迹记新版本号）；之前运行的轨迹仍是原来的版本。
func TestPromptVersionInRunTraces(t *testing.T) {
	mock := &llm.Mock{}
	mock.Fallback = func(req llm.Request) (llm.Response, error) {
		if req.JSON && strings.Contains(req.Messages[0].Content, "意图") || strings.Contains(req.Messages[0].Content, "PLANNER-V1") {
			return llm.Response{Content: `{"intent":"guide","action":"greeting"}`}, nil
		}
		return llm.Response{Content: `{"type":"final","text":"你好"}`}, nil
	}
	ts, base := newModelServer(t, mock)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	user := ts.login(t, seed.User2Username, seed.DevPassword).Token
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, user, map[string]any{})).SessionID
	plannerVersion := func(runID string) any {
		t.Helper()
		trace := decodeBody[runTraceResponse](t, ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", user, nil))
		for _, it := range trace.Items {
			if it.Stage == "planner" && it.EventType == "model" {
				return it.Metadata["prompt_version"]
			}
		}
		t.Fatalf("no planner trace: %+v", trace.Items)
		return nil
	}
	run := func(id string) string {
		s, _ := openStream(t, base, user, sid, msgBody(id, "你好呀"))
		start := s.next()
		collect(t, s)
		return start.Data["run_id"].(string)
	}
	first := run("p-1")
	if v := plannerVersion(first); v != 0.0 {
		t.Fatalf("first run version: %v", v)
	}
	key := promptsPath + "/" + agent.PromptPlanner
	ts.call(t, http.MethodPatch, key, admin, map[string]any{"content": "PLANNER-V1 你是意图规划器，只输出 JSON"})
	expectStatus(t, ts.call(t, http.MethodPost, key+"/publish", admin, map[string]any{"version": 1, "expected_active_version": 0}), http.StatusOK, "")
	second := run("p-2")
	if v := plannerVersion(second); v != 1.0 {
		t.Fatalf("second run version: %v", v)
	}
	found := false
	for _, req := range mock.Requests {
		if strings.HasPrefix(req.Messages[0].Content, "PLANNER-V1") {
			found = true
		}
	}
	if !found {
		t.Fatal("model never received the published prompt")
	}
	if v := plannerVersion(first); v != 0.0 {
		t.Fatalf("old run trace changed: %v", v)
	}
}
