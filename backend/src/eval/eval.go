// Package eval 运行检索和商品搜索的离线评测：读 quality/data/eval 下的 JSONL 用例，在内存 Store（开发种子 + 固定语料）上执行，
// 输出带版本、配置指纹、通过率和失败样本的报告。cmd/eval 是命令行入口，eval_test.go 在 CI 里按阈值把关。
package eval

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/ingest"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
)

// Report 是一个评测集的结果。
type Report struct {
	Suite       string             `json:"suite"`
	Version     string             `json:"version"`
	Config      map[string]string  `json:"config"`
	Fingerprint string             `json:"config_fingerprint"`
	StartedAt   time.Time          `json:"started_at"`
	DurationMS  int64              `json:"duration_ms"`
	Total       int                `json:"total"`
	Passed      int                `json:"passed"`
	PassRate    float64            `json:"pass_rate"`
	Metrics     map[string]float64 `json:"metrics"`
	Failures    []Failure          `json:"failures"`
}

// Failure 是一条失败样本。
type Failure struct {
	ID       string   `json:"id"`
	Query    string   `json:"query"`
	Problems []string `json:"problems"`
	Got      []string `json:"got"`
}

func (r *Report) finish(start time.Time) {
	r.DurationMS = time.Since(start).Milliseconds()
	if r.Total > 0 {
		r.PassRate = round(float64(r.Passed) / float64(r.Total))
	}
	keys := make([]string, 0, len(r.Config))
	for k := range r.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, r.Config[k])
	}
	r.Fingerprint = hex.EncodeToString(h.Sum(nil))[:12]
	if r.Failures == nil {
		r.Failures = []Failure{}
	}
}

func round(f float64) float64 { return float64(int(f*10000+0.5)) / 10000 }

// readJSONL 逐行解析 JSONL（空行和 # 开头的行忽略）。
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// newSeededStore 返回写入开发种子的内存 Store（密码哈希用占位值，评测不登录）。
func newSeededStore(ctx context.Context) (*memstore.Store, error) {
	st := memstore.New()
	data, err := seed.Dev(func(string) (string, error) { return "$2a$04$eval.placeholder.hash.not.for.login", nil })
	if err != nil {
		return nil, err
	}
	if _, err := st.ApplySeed(ctx, data); err != nil {
		return nil, err
	}
	return st, nil
}

type corpusDoc struct {
	Key        string `json:"key"`
	MerchantID string `json:"merchant_id"`
	ProductID  string `json:"product_id"`
	DocType    string `json:"doc_type"`
	SourceType string `json:"source_type"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	HTML       string `json:"html"`
	JSONText   string `json:"json_text"`
}

// loadCorpus 用真实入库流程写入固定语料，返回 key → document_id（种子文档的 key 就是其 ID）。
func loadCorpus(ctx context.Context, st store.Store, path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var corpus struct {
		Documents []corpusDoc `json:"documents"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		return nil, err
	}
	svc := ingest.NewService(st, nil, nil, nil, nil)
	ids := map[string]string{"doc_seed_nova": "doc_seed_nova", "doc_seed_after_sales": "doc_seed_after_sales", "doc_seed_mouse": "doc_seed_mouse"}
	for _, d := range corpus.Documents {
		res, err := svc.Ingest(ctx, ingest.Request{MerchantID: d.MerchantID, ProductID: d.ProductID, DocType: d.DocType,
			Source: ingest.Source{Title: d.Title, SourceType: d.SourceType, Content: d.Content, HTML: d.HTML, JSONText: d.JSONText}})
		if err != nil {
			return nil, fmt.Errorf("ingest %s: %w", d.Key, err)
		}
		ids[d.Key] = res.Document.DocumentID
	}
	return ids, nil
}

// WriteReport 把报告写成 report-<suite>.json 和 .md。
func WriteReport(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report-"+r.Suite+".json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# 评测报告：%s\n\n- 版本：%s\n- 时间：%s\n- 配置指纹：%s\n- 用例：%d，通过 %d，通过率 %.2f%%\n", r.Suite, r.Version,
		r.StartedAt.Format(time.RFC3339), r.Fingerprint, r.Total, r.Passed, r.PassRate*100)
	keys := make([]string, 0, len(r.Metrics))
	for k := range r.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&md, "- %s：%.4f\n", k, r.Metrics[k])
	}
	md.WriteString("\n## 配置\n\n")
	ck := make([]string, 0, len(r.Config))
	for k := range r.Config {
		ck = append(ck, k)
	}
	sort.Strings(ck)
	for _, k := range ck {
		fmt.Fprintf(&md, "- %s = %s\n", k, r.Config[k])
	}
	md.WriteString("\n## 失败样本\n\n")
	if len(r.Failures) == 0 {
		md.WriteString("无\n")
	}
	for _, f := range r.Failures {
		fmt.Fprintf(&md, "- `%s` %s：%s（实际：%s）\n", f.ID, f.Query, strings.Join(f.Problems, "；"), strings.Join(f.Got, ", "))
	}
	return os.WriteFile(filepath.Join(dir, "report-"+r.Suite+".md"), []byte(md.String()), 0o644)
}
