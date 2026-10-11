package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/eval"
)

// 质量评测：管理端查看数据集、评测套件和 cmd/eval 写出的报告（只读文件，不在接口里跑整套评测），
// 以及按单条输入试跑意图、知识检索、商品搜索和图片搜索（与导购用同一套规则和工具，同步返回，不保存）。

var (
	ErrReportNotFound = &APIError{Status: http.StatusNotFound, Code: "report_not_found", Message: "评测报告不存在"}
	ErrReportInvalid  = &APIError{Status: http.StatusUnprocessableEntity, Code: "report_invalid", Message: "评测报告格式不对（不是当前版本的报告）"}
)

const (
	maxReportRuns   = 200      // 最多扫描的报告目录数（按名称倒序，即最新的）
	maxReportList   = 50       // 列表最多返回的报告数
	maxReportBytes  = 32 << 20 // 单个报告文件上限
	maxEvalQueryLen = 500
)

var reportPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// evalSuite 是一个评测套件：数据集、默认门槛、本地运行命令和单条试跑接口。
type evalSuite struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Dataset     string  `json:"dataset"`
	MinPassRate float64 `json:"min_pass_rate"`
	Command     string  `json:"command"`
	TryPath     string  `json:"try_path"`
}

var evalSuites = []evalSuite{
	{ID: "intent", Name: "意图识别", Dataset: "intent", MinPassRate: eval.DefaultIntentMinPass, Command: "go run ./cmd/eval -suite=intent", TryPath: "/api/v1/eval/intent"},
	{ID: "rag", Name: "知识检索", Dataset: "rag", MinPassRate: 1, Command: "go run ./cmd/eval -suite=rag", TryPath: "/api/v1/eval/rag"},
	{ID: "products", Name: "商品搜索", Dataset: "product_search", MinPassRate: 1, Command: "go run ./cmd/eval -suite=products", TryPath: "/api/v1/eval/products"},
	{ID: "images", Name: "图片搜索", Dataset: "image_search", MinPassRate: eval.DefaultImageMinPass, Command: "go run ./cmd/eval -suite=images", TryPath: "/api/v1/eval/image-search"},
	{ID: "agent", Name: "端到端导购", Dataset: "agent", MinPassRate: 1, Command: "go run ./cmd/eval -suite=agent"},
}

type evalDatasetView struct {
	ID          string    `json:"id"`
	Suite       string    `json:"suite"`
	Description string    `json:"description"`
	CaseCount   int       `json:"case_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// evalReportSummary 是报告列表的一行（不含逐条结果和失败样本）。
type evalReportSummary struct {
	ID           string    `json:"id"`
	Suite        string    `json:"suite"`
	Version      string    `json:"version"`
	Fingerprint  string    `json:"config_fingerprint"`
	StartedAt    time.Time `json:"started_at"`
	Total        int       `json:"total"`
	Passed       int       `json:"passed"`
	PassRate     float64   `json:"pass_rate"`
	MinPassRate  float64   `json:"min_pass_rate"`
	Gate         string    `json:"gate"`
	FailureCount int       `json:"failure_count"`
}

type evalSuiteView struct {
	evalSuite
	CaseCount int                `json:"case_count"`
	Latest    *evalReportSummary `json:"latest"`
}

type evalsView struct {
	Configured   bool                `json:"configured"`
	Suites       []evalSuiteView     `json:"suites"`
	Datasets     []evalDatasetView   `json:"datasets"`
	Reports      []evalReportSummary `json:"reports"`
	ReportsTotal int                 `json:"reports_total"`
	Skipped      int                 `json:"skipped"`
}

// handleListEvals：GET /admin/evals。
func (s *Server) handleListEvals(w http.ResponseWriter, r *http.Request) {
	out := evalsView{Configured: s.evalDataDir != "" || s.evalReportDir != "", Suites: []evalSuiteView{}, Datasets: []evalDatasetView{}, Reports: []evalReportSummary{}}
	if s.evalDataDir != "" {
		out.Datasets = listDatasets(s.evalDataDir)
	}
	if s.evalReportDir != "" {
		out.Reports, out.Skipped = listReports(s.evalReportDir)
	}
	out.ReportsTotal = len(out.Reports)
	counts := map[string]int{}
	for _, d := range out.Datasets {
		counts[d.ID] = d.CaseCount
	}
	for _, suite := range evalSuites {
		v := evalSuiteView{evalSuite: suite, CaseCount: counts[suite.Dataset]}
		for i := range out.Reports {
			if out.Reports[i].Suite == suite.ID {
				v.Latest = &out.Reports[i]
				break
			}
		}
		out.Suites = append(out.Suites, v)
	}
	if len(out.Reports) > maxReportList {
		out.Reports = out.Reports[:maxReportList]
	}
	writeJSON(w, http.StatusOK, out)
}

func suiteOfDataset(id string) string {
	for _, s := range evalSuites {
		if s.Dataset == id {
			return s.ID
		}
	}
	return ""
}

// listDatasets 列出数据目录下的 *.jsonl：用例数（不算空行和 # 注释）和第一行注释作为说明。
func listDatasets(dir string) []evalDatasetView {
	out := []evalDatasetView{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files)
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		d := evalDatasetView{ID: id, Suite: suiteOfDataset(id), UpdatedAt: info.ModTime().UTC()}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case line == "":
			case strings.HasPrefix(line, "#"):
				if d.Description == "" {
					d.Description = truncate(strings.TrimSpace(strings.TrimPrefix(line, "#")), 300)
				}
			default:
				d.CaseCount++
			}
		}
		f.Close()
		out = append(out, d)
	}
	return out
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// listReports 扫描报告目录下一层子目录里的 report-<suite>.json（cmd/eval 的输出布局），按开始时间倒序。
// 不是当前格式版本的报告跳过并计数（旧版本没有门槛和分组，不能按同一口径展示）。
func listReports(dir string) ([]evalReportSummary, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []evalReportSummary{}, 0
	}
	var runs []string
	for _, e := range entries {
		if e.IsDir() && reportPart.MatchString(e.Name()) {
			runs = append(runs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(runs)))
	if len(runs) > maxReportRuns {
		runs = runs[:maxReportRuns]
	}
	out, skipped := []evalReportSummary{}, 0
	for _, run := range runs {
		files, _ := filepath.Glob(filepath.Join(dir, run, "report-*.json"))
		for _, path := range files {
			suite := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "report-"), ".json")
			if !reportPart.MatchString(suite) {
				continue
			}
			rep, err := readReport(dir, filepath.Join(run, filepath.Base(path)))
			if err != nil {
				skipped++
				continue
			}
			out = append(out, summarizeReport(run+"/"+suite, rep))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, skipped
}

func summarizeReport(id string, r eval.Report) evalReportSummary {
	return evalReportSummary{ID: id, Suite: r.Suite, Version: r.Version, Fingerprint: r.Fingerprint, StartedAt: r.StartedAt, Total: r.Total, Passed: r.Passed,
		PassRate: r.PassRate, MinPassRate: r.MinPassRate, Gate: r.Gate, FailureCount: len(r.Failures)}
}

var errReportFormat = errors.New("report format")

// readReport 读 dir 下的 name（相对路径）；用 os.Root 打开，目录里的符号链接也不能把读取带到报告目录之外。
func readReport(dir, name string) (eval.Report, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return eval.Report{}, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return eval.Report{}, err
	}
	defer f.Close()
	var rep eval.Report
	if err := json.NewDecoder(io.LimitReader(f, maxReportBytes)).Decode(&rep); err != nil {
		return rep, errReportFormat
	}
	if rep.SchemaVersion != eval.SchemaVersion || (rep.Gate != eval.GatePassed && rep.Gate != eval.GateFailed) {
		return rep, errReportFormat
	}
	return rep, nil
}

type evalReportDetail struct {
	ID string `json:"id"`
	eval.Report
}

// handleGetEvalReport：GET /admin/evals/reports/{run}/{suite}。两段都只允许字母、数字、点、下划线和短横线，不能跳出报告目录。
func (s *Server) handleGetEvalReport(w http.ResponseWriter, r *http.Request) {
	run, suite := r.PathValue("run"), r.PathValue("suite")
	if s.evalReportDir == "" || !reportPart.MatchString(run) || !reportPart.MatchString(suite) || strings.Contains(run, "..") || strings.Contains(suite, "..") {
		writeError(w, ErrReportNotFound)
		return
	}
	rep, err := readReport(s.evalReportDir, filepath.Join(run, "report-"+suite+".json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, ErrReportNotFound)
		return
	case errors.Is(err, errReportFormat):
		writeError(w, ErrReportInvalid)
		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "read eval report", "request_id", requestIDFromContext(r.Context()), "error", err)
		writeError(w, ErrInternal)
		return
	}
	writeJSON(w, http.StatusOK, evalReportDetail{ID: run + "/" + suite, Report: rep})
}

// ---------- 单条试跑 ----------

func evalQuery(raw string) (string, error) {
	q := strings.TrimSpace(raw)
	if q == "" || utf8.RuneCountInString(q) > maxEvalQueryLen {
		return "", fieldError("query", "请输入 1–500 字的内容")
	}
	return q, nil
}

type evalIntentInput struct {
	Query    string `json:"query"`
	HasImage bool   `json:"has_image"`
}

// handleEvalIntent：POST /eval/intent，规则规划的结果（意图、动作、槽位、命中的规则）。
func (s *Server) handleEvalIntent(w http.ResponseWriter, r *http.Request) {
	var in evalIntentInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	q, err := evalQuery(in.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	start := time.Now()
	p := agent.Classify(q, in.HasImage)
	writeJSON(w, http.StatusOK, map[string]any{"query": agent.RedactPII(q), "plan": p, "duration_ms": time.Since(start).Milliseconds()})
}

type evalToolResponse struct {
	Query      string `json:"query"`
	Tool       string `json:"tool"`
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	Data       any    `json:"data"`
	Recall     any    `json:"recall,omitempty"`
	Rerank     any    `json:"rerank,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// callEvalTool 以管理员身份调用导购的只读工具（参数按工具 schema 校验，与导购调用时一致）。
func (s *Server) callEvalTool(w http.ResponseWriter, r *http.Request, intent agent.Intent, tool, query string, args map[string]any) {
	acc, _ := accountFromContext(r.Context())
	obs := s.evalTools.Call(r.Context(), &agent.ToolContext{AccountID: acc.AccountID, Intent: intent}, tool, args)
	out := evalToolResponse{Query: agent.RedactPII(query), Tool: tool, OK: obs.OK, Code: obs.Code, Message: obs.Message, Data: obs.Data, DurationMS: obs.Duration.Milliseconds()}
	if res, ok := obs.Data.(agent.ProductSearchResult); ok {
		out.Recall, out.Rerank = res.Recall, res.Rerank
	}
	if !obs.OK && obs.Code == agent.CodeInvalidArgument {
		writeError(w, fieldError(obs.Field, obs.Message))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type evalRAGInput struct {
	Query     string `json:"query"`
	TopK      *int   `json:"top_k"`
	ProductID string `json:"product_id"`
}

// handleEvalRAG：POST /eval/rag，调 search_knowledge，返回引用的分块（chunk_id、来源、得分）。
func (s *Server) handleEvalRAG(w http.ResponseWriter, r *http.Request) {
	var in evalRAGInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	q, err := evalQuery(in.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	args := map[string]any{"query": q}
	if in.TopK != nil {
		args["limit"] = *in.TopK
	}
	if in.ProductID != "" {
		args["product_id"] = in.ProductID
	}
	s.callEvalTool(w, r, agent.IntentKnowledge, agent.ToolSearchKnowledge, q, args)
}

type evalProductsInput struct {
	Query    string   `json:"query"`
	Limit    *int     `json:"limit"`
	MaxPrice *float64 `json:"max_price"`
	MinPrice *float64 `json:"min_price"`
	Brands   []string `json:"brands"`
	Exclude  []string `json:"exclude"`
}

// handleEvalProducts：POST /eval/products，调 search_products，返回结果和召回、重排明细。
func (s *Server) handleEvalProducts(w http.ResponseWriter, r *http.Request) {
	var in evalProductsInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, err)
		return
	}
	q, err := evalQuery(in.Query)
	if err != nil {
		writeError(w, err)
		return
	}
	args := map[string]any{"query": q}
	if in.Limit != nil {
		args["limit"] = *in.Limit
	}
	if in.MaxPrice != nil {
		args["max_price"] = *in.MaxPrice
	}
	if in.MinPrice != nil {
		args["min_price"] = *in.MinPrice
	}
	if len(in.Brands) > 0 {
		args["brands"] = toAnySlice(in.Brands)
	}
	if len(in.Exclude) > 0 {
		args["exclude"] = toAnySlice(in.Exclude)
	}
	s.callEvalTool(w, r, agent.IntentProductSearch, agent.ToolSearchProducts, q, args)
}

func toAnySlice(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// handleEvalImageSearch：POST /eval/image-search，用管理员本人上传的图片搜索，附带候选数、不可售被丢弃数和耗时。
func (s *Server) handleEvalImageSearch(w http.ResponseWriter, r *http.Request) {
	acc, _ := accountFromContext(r.Context())
	start := time.Now()
	_, res, ok := s.searchImageFile(w, r, acc.AccountID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": toSearchImageResponse(res), "candidates": res.Candidates, "dropped": res.Dropped,
		"duration_ms": time.Since(start).Milliseconds()})
}
