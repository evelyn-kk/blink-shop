package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 故意构造会失败的用例：报告的门槛为 failed，失败样本带分组和原因，逐条结果标出失败，分组通过率准确，Markdown 标红；
// 写出的 JSON 符合 quality/schemas/eval-report.schema.json。
func TestReportMarksDeliberateFailures(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	intents := write("intent.jsonl",
		`{"id":"ok_1","query":"推荐一款降噪耳机","expect_intent":"product_search"}`,
		`{"id":"bad_1","query":"推荐一款降噪耳机","expect_intent":"cart","expect_action":"add"}`,
		`{"id":"ok_2","query":"今天天气怎么样","expect_intent":"non_guide"}`)
	rep, err := RunIntent(context.Background(), IntentOptions{Cases: intents, Version: "test", MinPass: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gate != GateFailed || rep.Passed != 2 || rep.Total != 3 || len(rep.Failures) != 1 {
		t.Fatalf("gate=%s passed=%d/%d failures=%v", rep.Gate, rep.Passed, rep.Total, rep.Failures)
	}
	f := rep.Failures[0]
	if f.ID != "bad_1" || f.Group != "cart" || len(f.Problems) != 2 || f.Got[0] != "product_search/" {
		t.Fatalf("failure: %+v", f)
	}
	if g := rep.Groups["cart"]; g.Total != 1 || g.Passed != 0 || g.PassRate != 0 {
		t.Fatalf("group: %+v", rep.Groups)
	}
	for _, c := range rep.Cases {
		if (c.ID == "bad_1") == c.Passed {
			t.Fatalf("case %s passed=%v", c.ID, c.Passed)
		}
	}

	agentCases := write("agent.jsonl",
		`{"id":"a_bad","group":"no_inventory","query":"推荐一款通勤降噪耳机","expect_no_products":true}`)
	arep, err := RunAgent(context.Background(), AgentOptions{Cases: agentCases, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if arep.Gate != GateFailed || len(arep.Failures) != 1 || arep.Failures[0].Problems[0] != "不应给出商品" || arep.Metrics["no_product_accuracy"] != 0 {
		t.Fatalf("agent report: gate=%s failures=%+v metrics=%v", arep.Gate, arep.Failures, arep.Metrics)
	}

	out := filepath.Join(dir, "out")
	for _, r := range []Report{rep, arep} {
		if err := WriteReport(out, r); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(out, "report-"+r.Suite+".json"))
		if errs := validateSchema(t, raw); len(errs) > 0 {
			t.Fatalf("%s report does not match schema: %v", r.Suite, errs)
		}
	}
	md, _ := os.ReadFile(filepath.Join(out, "report-intent.md"))
	for _, want := range []string{"结论：❌ 未通过（门槛 90.00%）", "- cart：0/1 ❌", "- ❌ `bad_1`"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	// 空数据集不能算通过
	empty := write("empty.jsonl", "# 只有注释")
	if r, err := RunIntent(context.Background(), IntentOptions{Cases: empty}); err != nil || r.Gate != GateFailed {
		t.Fatalf("empty dataset gate=%s err=%v", r.Gate, err)
	}
}

// validateSchema 按 quality/schemas/eval-report.schema.json 校验报告：required、type、enum、const、minimum / maximum、minItems，
// 递归到 properties、additionalProperties 和 items（报告 schema 只用到这些关键字）。
func validateSchema(t *testing.T, doc []byte) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../quality/schemas/eval-report.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	var errs []string
	check(schema, v, "$", &errs)
	return errs
}

func check(s map[string]any, v any, path string, errs *[]string) {
	fail := func(format string, a ...any) { *errs = append(*errs, path+": "+fmt.Sprintf(format, a...)) }
	if c, ok := s["const"]; ok && c != v {
		fail("want const %v, got %v", c, v)
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || e == v
		}
		if !found {
			fail("%v not in enum %v", v, enum)
		}
	}
	switch s["type"] {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			fail("want object")
			return
		}
		for _, r := range asSlice(s["required"]) {
			if _, ok := obj[r.(string)]; !ok {
				fail("missing %s", r)
			}
		}
		props, _ := s["properties"].(map[string]any)
		extra, _ := s["additionalProperties"].(map[string]any)
		for k, val := range obj {
			if ps, ok := props[k].(map[string]any); ok {
				check(ps, val, path+"."+k, errs)
			} else if extra != nil {
				check(extra, val, path+"."+k, errs)
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			fail("want array")
			return
		}
		if m, ok := s["minItems"].(float64); ok && float64(len(arr)) < m {
			fail("want at least %v items", m)
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, it := range arr {
				check(items, it, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			fail("want string")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			fail("want boolean")
		}
	case "integer", "number":
		n, ok := v.(float64)
		if !ok || (s["type"] == "integer" && n != math.Trunc(n)) {
			fail("want %s, got %v", s["type"], v)
			return
		}
		if m, ok := s["minimum"].(float64); ok && n < m {
			fail("%v < minimum %v", n, m)
		}
		if m, ok := s["maximum"].(float64); ok && n > m {
			fail("%v > maximum %v", n, m)
		}
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
