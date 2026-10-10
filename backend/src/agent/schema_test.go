package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaValidate(t *testing.T) {
	s := object([]string{"product_id"}, map[string]*Schema{
		"product_id": strLen("商品", 1, 8),
		"quantity":   integer("数量", 1, 99, 1),
		"price":      number("价格", 0),
		"selected":   boolean("选中"),
		"status":     enum("状态", "a", "b"),
		"tags":       strList("标签", 2, 3),
	})
	for _, c := range []struct {
		args  map[string]any
		field string
	}{
		{map[string]any{"product_id": "p_1"}, ""},
		{map[string]any{"product_id": "p_1", "quantity": float64(2), "price": 1.5, "selected": true, "status": "a", "tags": []any{"x", "yy"}}, ""},
		{map[string]any{"product_id": "p_1", "quantity": nil}, ""},
		{map[string]any{}, "product_id"},
		{map[string]any{"product_id": ""}, "product_id"},
		{map[string]any{"product_id": "123456789"}, "product_id"},
		{map[string]any{"product_id": 1}, "product_id"},
		{map[string]any{"product_id": "p", "quantity": 1.5}, "quantity"},
		{map[string]any{"product_id": "p", "quantity": float64(0)}, "quantity"},
		{map[string]any{"product_id": "p", "quantity": float64(100)}, "quantity"},
		{map[string]any{"product_id": "p", "price": -1.0}, "price"},
		{map[string]any{"product_id": "p", "selected": "yes"}, "selected"},
		{map[string]any{"product_id": "p", "status": "c"}, "status"},
		{map[string]any{"product_id": "p", "tags": []any{"a", "b", "c"}}, "tags"},
		{map[string]any{"product_id": "p", "tags": []any{"abcd"}}, "tags[0]"},
		{map[string]any{"product_id": "p", "tags": "a"}, "tags"},
		{map[string]any{"product_id": "p", "unknown": 1}, "unknown"},
	} {
		err := s.Validate(c.args)
		var ae *ArgError
		switch {
		case c.field == "" && err != nil:
			t.Errorf("%v: unexpected %v", c.args, err)
		case c.field != "" && (!errors.As(err, &ae) || ae.Field != c.field):
			t.Errorf("%v: got %v, want field %s", c.args, err, c.field)
		}
	}
	if err := s.Validate("x"); err == nil {
		t.Error("non-object should fail")
	}
}

// TestToolCatalogFixture 把工具清单（名字、说明、参数 schema、允许的意图）与 fixtures/agent/tools.json 比对：
// 工具契约的任何变化都要同时更新这份文件（设置 BLINK_UPDATE_FIXTURES=1 重新生成）。
func TestToolCatalogFixture(t *testing.T) {
	e := newEnv(t)
	got, err := json.MarshalIndent(map[string]any{"tools": e.reg.ExportTools(), "policy": e.reg.policy}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("..", "..", "fixtures", "agent", "tools.json")
	if os.Getenv("BLINK_UPDATE_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (set BLINK_UPDATE_FIXTURES=1 to generate)", err)
	}
	if string(want) != string(got) {
		t.Fatalf("tool catalog changed; regenerate with BLINK_UPDATE_FIXTURES=1\n%s", got)
	}
	if len(e.reg.Tools()) != 20 {
		t.Fatalf("expected 20 tools, got %d", len(e.reg.Tools()))
	}
}
