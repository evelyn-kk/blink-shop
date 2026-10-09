package agent

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Schema 是工具参数的 JSON Schema 子集：object / string / integer / number / boolean / array，
// 支持 required、additionalProperties=false、enum、minimum/maximum、minLength/maxLength、maxItems、items。
// 工具在执行前用它校验参数；Export 输出标准 JSON Schema 供文档和客户端使用。
type Schema struct {
	Type        string
	Description string
	Properties  map[string]*Schema
	Required    []string
	Enum        []string
	Minimum     *float64
	Maximum     *float64
	MinLength   *int
	MaxLength   *int
	MaxItems    *int
	Items       *Schema
	Default     any
}

// ArgError 是参数校验错误；Field 是出错字段（嵌套用点号连接）。
type ArgError struct {
	Field   string
	Message string
}

func (e *ArgError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
func str(desc string) *Schema     { return &Schema{Type: "string", Description: desc} }
func boolean(desc string) *Schema { return &Schema{Type: "boolean", Description: desc} }
func strLen(desc string, lo, hi int) *Schema {
	return &Schema{Type: "string", Description: desc, MinLength: intPtr(lo), MaxLength: intPtr(hi)}
}
func integer(desc string, lo, hi int, def any) *Schema {
	return &Schema{Type: "integer", Description: desc, Minimum: floatPtr(float64(lo)), Maximum: floatPtr(float64(hi)), Default: def}
}
func number(desc string, lo float64) *Schema {
	return &Schema{Type: "number", Description: desc, Minimum: floatPtr(lo)}
}
func enum(desc string, values ...string) *Schema {
	return &Schema{Type: "string", Description: desc, Enum: values}
}
func strList(desc string, maxItems, maxLen int) *Schema {
	return &Schema{Type: "array", Description: desc, MaxItems: intPtr(maxItems), Items: strLen("", 1, maxLen)}
}
func object(required []string, props map[string]*Schema) *Schema {
	return &Schema{Type: "object", Properties: props, Required: required}
}

// Validate 校验 v（json 解码后的值：map[string]any / string / float64 / bool / []any）。
func (s *Schema) Validate(v any) error { return s.validate("", v) }

func (s *Schema) validate(path string, v any) error {
	name := path
	if name == "" {
		name = "(root)"
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return &ArgError{Field: name, Message: "必须是对象"}
		}
		for _, req := range s.Required {
			if _, ok := m[req]; !ok {
				return &ArgError{Field: join(path, req), Message: "必填"}
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			prop, ok := s.Properties[k]
			if !ok {
				return &ArgError{Field: join(path, k), Message: "未知参数"}
			}
			if m[k] == nil {
				continue // null 等同于未提供
			}
			if err := prop.validate(join(path, k), m[k]); err != nil {
				return err
			}
		}
	case "string":
		sv, ok := v.(string)
		if !ok {
			return &ArgError{Field: name, Message: "必须是字符串"}
		}
		n := utf8.RuneCountInString(strings.TrimSpace(sv))
		if s.MinLength != nil && n < *s.MinLength {
			return &ArgError{Field: name, Message: fmt.Sprintf("至少 %d 个字符", *s.MinLength)}
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return &ArgError{Field: name, Message: fmt.Sprintf("最多 %d 个字符", *s.MaxLength)}
		}
		if len(s.Enum) > 0 {
			found := false
			for _, e := range s.Enum {
				if e == sv {
					found = true
				}
			}
			if !found {
				return &ArgError{Field: name, Message: "只能是 " + strings.Join(s.Enum, "、")}
			}
		}
	case "integer", "number":
		f, ok := toFloat(v)
		if !ok || (s.Type == "integer" && f != math.Trunc(f)) {
			if s.Type == "integer" {
				return &ArgError{Field: name, Message: "必须是整数"}
			}
			return &ArgError{Field: name, Message: "必须是数字"}
		}
		if s.Minimum != nil && f < *s.Minimum {
			return &ArgError{Field: name, Message: fmt.Sprintf("不能小于 %v", *s.Minimum)}
		}
		if s.Maximum != nil && f > *s.Maximum {
			return &ArgError{Field: name, Message: fmt.Sprintf("不能大于 %v", *s.Maximum)}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return &ArgError{Field: name, Message: "必须是 true 或 false"}
		}
	case "array":
		items, ok := toSlice(v)
		if !ok {
			return &ArgError{Field: name, Message: "必须是数组"}
		}
		if s.MaxItems != nil && len(items) > *s.MaxItems {
			return &ArgError{Field: name, Message: fmt.Sprintf("最多 %d 项", *s.MaxItems)}
		}
		if s.Items != nil {
			for i, it := range items {
				if err := s.Items.validate(fmt.Sprintf("%s[%d]", path, i), it); err != nil {
					return err
				}
			}
		}
	default:
		return &ArgError{Field: name, Message: "schema 类型不支持：" + s.Type}
	}
	return nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}

func toSlice(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case []string:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out, true
	}
	return nil, false
}

// Export 输出标准 JSON Schema（map 形式，可直接序列化）。
func (s *Schema) Export() map[string]any {
	out := map[string]any{"type": s.Type}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if s.Type == "object" {
		props := map[string]any{}
		for k, p := range s.Properties {
			props[k] = p.Export()
		}
		out["properties"] = props
		out["required"] = append([]string{}, s.Required...)
		out["additionalProperties"] = false
	}
	if len(s.Enum) > 0 {
		out["enum"] = append([]string{}, s.Enum...)
	}
	if s.Minimum != nil {
		out["minimum"] = *s.Minimum
	}
	if s.Maximum != nil {
		out["maximum"] = *s.Maximum
	}
	if s.MinLength != nil {
		out["minLength"] = *s.MinLength
	}
	if s.MaxLength != nil {
		out["maxLength"] = *s.MaxLength
	}
	if s.MaxItems != nil {
		out["maxItems"] = *s.MaxItems
	}
	if s.Items != nil {
		out["items"] = s.Items.Export()
	}
	if s.Default != nil {
		out["default"] = s.Default
	}
	return out
}

// ---------- 参数读取（校验通过后使用） ----------

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	if f, ok := toFloat(args[key]); ok {
		return int(f)
	}
	return def
}

func argIntPtr(args map[string]any, key string) *int {
	if f, ok := toFloat(args[key]); ok {
		return intPtr(int(f))
	}
	return nil
}

func argBoolPtr(args map[string]any, key string) *bool {
	if b, ok := args[key].(bool); ok {
		return &b
	}
	return nil
}

func argFloat(args map[string]any, key string) (float64, bool) { return toFloat(args[key]) }

func argStrings(args map[string]any, key string) []string {
	items, ok := toSlice(args[key])
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
