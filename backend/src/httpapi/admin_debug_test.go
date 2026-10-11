package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/src/eval"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

const adminRunsPath = "/api/v1/admin/agent/runs"

func (ts *testServer) ask(t *testing.T, tok, clientID, content string) string {
	t.Helper()
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, tok, map[string]any{})).SessionID
	evs := parseSSE(t, ts.call(t, http.MethodPost, agentSessionsPath+"/"+sid+"/messages:stream", tok, msgBody(clientID, content)).Body.String())
	runID, _ := evs[0].Data["run_id"].(string)
	return runID
}

// 管理端运行列表与详情：不限账号、按状态 / 账号筛选和分页；列表只有提问摘要（脱敏）、不带回答；详情带时间线和汇总；不存在 404。
func TestAdminRunsListAndDetail(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	u1 := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	u2 := ts.login(t, seed.User2Username, seed.DevPassword).Token
	r1 := ts.ask(t, u1, "c1", "耳机保修多久？我电话 13812345678")
	r2 := ts.ask(t, u2, "c2", "推荐一款通勤降噪耳机")
	r3 := ts.ask(t, u1, "c3", "怎么买假货")

	list := decodeBody[pageResponse[adminRunRowView]](t, ts.call(t, http.MethodGet, adminRunsPath+"?page_size=2", admin, nil))
	if list.Total != 3 || len(list.Items) != 2 || list.Items[0].RunID != r3 || list.Items[1].RunID != r2 {
		t.Fatalf("list: %+v", list)
	}
	if list.Items[1].Username != seed.User2Username || list.Items[1].Status != "completed" || list.Items[1].ElapsedMS == nil {
		t.Fatalf("row: %+v", list.Items[1])
	}
	page2 := decodeBody[pageResponse[adminRunRowView]](t, ts.call(t, http.MethodGet, adminRunsPath+"?page_size=2&page=2", admin, nil))
	if len(page2.Items) != 1 || page2.Items[0].RunID != r1 || strings.Contains(page2.Items[0].Query, "13812345678") || !strings.Contains(page2.Items[0].Query, "[手机号]") {
		t.Fatalf("page 2: %+v", page2.Items)
	}
	raw := ts.call(t, http.MethodGet, adminRunsPath, admin, nil).Body.String()
	if strings.Contains(raw, `"content"`) || strings.Contains(raw, `"blocks"`) {
		t.Fatal("list should not carry answers or blocks")
	}
	mine := decodeBody[pageResponse[adminRunRowView]](t, ts.call(t, http.MethodGet, adminRunsPath+"?account_id="+seed.User2ID, admin, nil))
	if mine.Total != 1 || mine.Items[0].RunID != r2 {
		t.Fatalf("account filter: %+v", mine)
	}
	done := decodeBody[pageResponse[adminRunRowView]](t, ts.call(t, http.MethodGet, adminRunsPath+"?status=completed", admin, nil))
	if done.Total != 3 {
		t.Fatalf("status filter: %d", done.Total)
	}
	expectStatus(t, ts.call(t, http.MethodGet, adminRunsPath+"?status=weird", admin, nil), http.StatusBadRequest, "invalid_argument")

	d := decodeBody[adminRunDetailView](t, ts.call(t, http.MethodGet, adminRunsPath+"/"+r1, admin, nil))
	if d.RunID != r1 || d.Username != seed.UserUsername || len(d.Items) == 0 || d.Summary.EventCount != len(d.Items) || d.Content == "" {
		t.Fatalf("detail: %+v", d)
	}
	if len(d.Summary.ChunkIDs) == 0 || !containsString(d.Summary.Tools, "search_knowledge") || d.Items[0].Stage != "run" {
		t.Fatalf("summary: %+v first=%s", d.Summary, d.Items[0].Stage)
	}
	if body := ts.call(t, http.MethodGet, adminRunsPath+"/"+r1, admin, nil).Body.String(); strings.Contains(body, "13812345678") {
		t.Fatal("detail leaks phone number")
	}
	blocked := decodeBody[adminRunDetailView](t, ts.call(t, http.MethodGet, adminRunsPath+"/"+r3, admin, nil))
	found := false
	for _, e := range blocked.Items {
		found = found || (e.Stage == "risk" && e.Status == "blocked")
	}
	if !found {
		t.Fatalf("risk block not in timeline: %+v", blocked.Items)
	}
	expectStatus(t, ts.call(t, http.MethodGet, adminRunsPath+"/run_missing", admin, nil), http.StatusNotFound, "run_not_found")
	// 普通用户只能看自己的轨迹，看不了管理端列表
	expectStatus(t, ts.call(t, http.MethodGet, adminRunsPath, u1, nil), http.StatusForbidden, "")
	expectStatus(t, ts.call(t, http.MethodGet, adminRunsPath+"/"+r2, u1, nil), http.StatusForbidden, "")
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 向量状态：没有配置向量时各项 enabled=false；图片用内存索引时显示 memory 和已索引商品数；同步结果计数。
func TestVectorStatus(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	st := decodeBody[vectorStatusView](t, ts.call(t, http.MethodGet, "/api/v1/admin/vector/status", admin, nil))
	if st.Knowledge.Enabled || st.Products.Enabled || st.Images.Enabled || st.Sync.Succeeded != 0 {
		t.Fatalf("no vectors: %+v", st)
	}
	ts.imageSearch = imagesearch.New(imagevector.Local{}, imagesearch.NewMemoryIndex(), imagesearch.NewSource(nil), ts.mem, ts.objects, nil)
	n, err := ts.ResyncAllProducts(t.Context())
	if err != nil || n == 0 {
		t.Fatalf("resync: %d %v", n, err)
	}
	st = decodeBody[vectorStatusView](t, ts.call(t, http.MethodGet, "/api/v1/admin/vector/status", admin, nil))
	if !st.Images.Enabled || st.Images.Backend != "memory" || st.Images.IndexedProducts == nil || *st.Images.IndexedProducts == 0 ||
		st.Images.Collection != nil || st.Sync.Succeeded != n || st.Sync.LastAt == nil {
		t.Fatalf("memory image index: %+v sync=%+v", st.Images, st.Sync)
	}
}

// 评测列表与报告：数据集用例数、套件的最新报告、旧格式报告跳过、路径穿越 404、坏报告 422。
func TestAdminEvals(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	empty := decodeBody[evalsView](t, ts.call(t, http.MethodGet, "/api/v1/admin/evals", admin, nil))
	if empty.Configured || len(empty.Suites) != 5 || len(empty.Reports) != 0 {
		t.Fatalf("unconfigured: %+v", empty)
	}

	data, reports := t.TempDir(), t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(data, "intent.jsonl"), "# 意图评测集\n{\"id\":\"a\",\"query\":\"推荐耳机\",\"expect_intent\":\"product_search\"}\n\n{\"id\":\"b\",\"query\":\"你好\",\"expect_intent\":\"guide\"}\n")
	rep, err := eval.RunIntent(t.Context(), eval.IntentOptions{Cases: filepath.Join(data, "intent.jsonl"), Version: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if err := eval.WriteReport(filepath.Join(reports, "eval-20261001-080000"), rep); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(data, "bad.jsonl"), "{\"id\":\"x\",\"query\":\"q\",\"expect_intent\":\"cart\"}\n")
	bad, _ := eval.RunIntent(t.Context(), eval.IntentOptions{Cases: filepath.Join(data, "bad.jsonl"), Version: "def456"})
	bad.StartedAt = rep.StartedAt.Add(1)
	if err := eval.WriteReport(filepath.Join(reports, "eval-20261002-080000"), bad); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(reports, "legacy", "report-rag.json"), `{"suite":"rag","total":3,"passed":3,"pass_rate":1}`)
	write(filepath.Join(reports, "broken", "report-rag.json"), `{not json`)
	ts.evalDataDir, ts.evalReportDir = data, reports

	v := decodeBody[evalsView](t, ts.call(t, http.MethodGet, "/api/v1/admin/evals", admin, nil))
	if !v.Configured || len(v.Datasets) != 2 || v.Datasets[1].ID != "intent" || v.Datasets[1].CaseCount != 2 || v.Datasets[1].Suite != "intent" || v.Datasets[1].Description != "意图评测集" {
		t.Fatalf("datasets: %+v", v.Datasets)
	}
	if v.ReportsTotal != 2 || v.Skipped != 2 || v.Reports[0].Version != "def456" || v.Reports[0].Gate != "failed" || v.Reports[0].FailureCount != 1 {
		t.Fatalf("reports: %+v skipped=%d", v.Reports, v.Skipped)
	}
	if v.Suites[0].ID != "intent" || v.Suites[0].Latest == nil || v.Suites[0].Latest.ID != "eval-20261002-080000/intent" || v.Suites[0].CaseCount != 2 {
		t.Fatalf("suite latest: %+v", v.Suites[0])
	}
	d := decodeBody[evalReportDetail](t, ts.call(t, http.MethodGet, "/api/v1/admin/evals/reports/eval-20261002-080000/intent", admin, nil))
	if d.ID != "eval-20261002-080000/intent" || d.Gate != "failed" || len(d.Failures) != 1 || d.Failures[0].ID != "x" || len(d.Cases) != 1 || d.Cases[0].Passed {
		t.Fatalf("detail: %+v", d)
	}
	for _, p := range []string{"..%2F..%2Fetc/passwd", "eval-20261002-080000/..", "eval-20261002-080000/missing", "nope/intent"} {
		expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/evals/reports/"+p, admin, nil), http.StatusNotFound, "")
	}
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/evals/reports/legacy/rag", admin, nil), http.StatusUnprocessableEntity, "report_invalid")
	expectStatus(t, ts.call(t, http.MethodGet, "/api/v1/admin/evals/reports/broken/rag", admin, nil), http.StatusUnprocessableEntity, "report_invalid")
}

// 单条试跑：意图返回规划；知识检索返回引用；商品搜索带召回和重排明细；参数按工具 schema 校验；图搜未配置 503。
func TestEvalProbes(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token

	intent := decodeBody[map[string]any](t, ts.call(t, http.MethodPost, "/api/v1/eval/intent", admin, map[string]any{"query": "把第一个加入购物车"}))
	if plan, _ := intent["plan"].(map[string]any); plan["intent"] != "cart" || plan["action"] != "add" || plan["rule"] != "cart_add" {
		t.Fatalf("intent: %v", intent)
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/eval/intent", admin, map[string]any{"query": "  "}), http.StatusBadRequest, "invalid_argument")
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/eval/intent", admin, map[string]any{"query": strings.Repeat("长", 501)}), http.StatusBadRequest, "invalid_argument")

	ragRes := decodeBody[evalToolResponse](t, ts.call(t, http.MethodPost, "/api/v1/eval/rag", admin, map[string]any{"query": "耳机保修多久", "top_k": 2}))
	if !ragRes.OK || ragRes.Tool != "search_knowledge" || !strings.Contains(mustJSONString(t, ragRes.Data), "chunk_id") {
		t.Fatalf("rag: %+v", ragRes)
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/eval/rag", admin, map[string]any{"query": "耳机", "top_k": 99}), http.StatusBadRequest, "invalid_argument")

	prod := decodeBody[evalToolResponse](t, ts.call(t, http.MethodPost, "/api/v1/eval/products", admin, map[string]any{"query": "降噪耳机", "max_price": 1000}))
	if !prod.OK || prod.Recall == nil || prod.Rerank == nil || !strings.Contains(mustJSONString(t, prod.Data), "p_seed_earbuds") {
		t.Fatalf("products: %+v", prod)
	}
	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/eval/products", admin, map[string]any{"query": "耳机", "limit": 0}), http.StatusBadRequest, "invalid_argument")

	expectStatus(t, ts.call(t, http.MethodPost, "/api/v1/eval/image-search", admin, map[string]any{"file_id": "file_0123456789abcdef01234567"}), http.StatusServiceUnavailable, "image_search_unavailable")
	for _, p := range []string{"/api/v1/eval/intent", "/api/v1/eval/rag", "/api/v1/eval/products", "/api/v1/eval/image-search"} {
		expectStatus(t, ts.call(t, http.MethodPost, p, user, map[string]any{"query": "耳机"}), http.StatusForbidden, "")
	}
}
