package eval

import (
	"context"
	"os"
	"strings"
	"testing"
)

const dataDir = "../../../quality/data/eval/"

// TestRAGSuite：固定集 recall@3 = 1、无结果用例全部正确、每条 citation 可回查。
func TestRAGSuite(t *testing.T) {
	rep, err := RunRAG(context.Background(), RAGOptions{Cases: dataDir + "rag.jsonl", Corpus: "../../fixtures/rag/corpus.json", K: 3, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Failures {
		t.Errorf("%s %q: %v got=%v", f.ID, f.Query, f.Problems, f.Got)
	}
	if rep.Total < 30 || rep.Metrics["recall_at_k"] != 1 || rep.Metrics["held_out_recall_at_k"] != 1 || rep.Metrics["no_result_accuracy"] != 1 || rep.Fingerprint == "" {
		t.Fatalf("report: %+v", rep)
	}
}

// TestProductSuite：商品搜索固定集全部通过（预算、品牌、排除、用途、冲突、无结果、多轮继承）。
func TestProductSuite(t *testing.T) {
	rep, err := RunProducts(context.Background(), ProductOptions{Cases: dataDir + "product_search.jsonl", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Failures {
		t.Errorf("%s %q: %v got=%v", f.ID, f.Query, f.Problems, f.Got)
	}
	if rep.Total < 25 || rep.PassRate != 1 || rep.Metrics["negative_constraint_accuracy"] != 1 || rep.Metrics["no_recommendation_accuracy"] != 1 {
		t.Fatalf("report: pass=%v metrics=%v", rep.PassRate, rep.Metrics)
	}
}

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	rep := Report{Suite: "x", Version: "v", Config: map[string]string{"a": "1"}, Metrics: map[string]float64{"m": 0.5}, Total: 2, Passed: 1,
		Failures: []Failure{{ID: "c1", Query: "q", Problems: []string{"p"}, Got: []string{"g"}}}}
	rep.finish(rep.StartedAt)
	if err := WriteReport(dir, rep); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(dir + "/report-x.md")
	if !strings.Contains(string(md), "通过率 50.00%") || !strings.Contains(string(md), "`c1` q：p（实际：g）") {
		t.Fatalf("md: %s", md)
	}
	if _, err := os.Stat(dir + "/report-x.json"); err != nil {
		t.Fatal(err)
	}
}

func TestImageSuite(t *testing.T) {
	rep, err := RunImages(context.Background(), ImageOptions{Cases: "../../../quality/data/eval/image_search.jsonl", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("images: %d/%d %v", rep.Passed, rep.Total, rep.Metrics)
	for _, f := range rep.Failures {
		t.Logf("failure %s %s: %v got %v", f.ID, f.Query, f.Problems, f.Got)
	}
	// 本地特征的门槛：同款在前 3 的比例、held-out、无关图片不返回、不泄漏不可售商品、标为同款的都对。
	m := rep.Metrics
	if m["recall_at_3"] < 0.98 || m["held_out_recall_at_3"] < 0.98 || m["no_match_accuracy"] < 1 || m["inactive_leaks"] != 0 || m["match_precision"] < 0.98 {
		t.Fatalf("image metrics below threshold: %v", m)
	}
}
