package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Milvus 是 Milvus RESTful API v2（/v2/vectordb/...）的最小客户端：只用到建集合、写入、按条件删除、检索和删集合。
type Milvus struct {
	base  string
	token string
	http  *http.Client
}

// NewMilvus 创建客户端；addr 为空时返回 nil（未配置）。addr 可以带或不带 http(s):// 前缀。
func NewMilvus(addr, token string, timeout time.Duration) *Milvus {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Milvus{base: strings.TrimRight(addr, "/"), token: token, http: &http.Client{Timeout: timeout}}
}

// MilvusError 是 Milvus 返回的非 0 业务码。
type MilvusError struct {
	Code    int
	Message string
}

func (e *MilvusError) Error() string { return fmt.Sprintf("milvus: code %d: %s", e.Code, e.Message) }

func (m *Milvus) call(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("milvus: http %d", resp.StatusCode)
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("milvus: bad response: %w", err)
	}
	if env.Code != 0 {
		return &MilvusError{Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// Field 是集合里的一个标量字段（VarChar）。
type Field struct {
	Name   string
	MaxLen int
}

// EnsureCollection 确保集合存在：不存在时按“id 主键 + vector（COSINE、AUTOINDEX）+ 标量字段”创建（创建即加载）；
// 已存在时检查向量维度，不一致返回 ErrDimMismatch。
func (m *Milvus) EnsureCollection(ctx context.Context, name string, dim int, fields []Field) error {
	var has struct {
		Has bool `json:"has"`
	}
	if err := m.call(ctx, "/v2/vectordb/collections/has", map[string]any{"collectionName": name}, &has); err != nil {
		return err
	}
	if has.Has {
		got, err := m.dim(ctx, name)
		if err != nil {
			return err
		}
		if got != dim {
			return fmt.Errorf("%w: %s has dim %d, embedder dim %d", ErrDimMismatch, name, got, dim)
		}
		return nil
	}
	schema := []map[string]any{
		{"fieldName": "id", "dataType": "VarChar", "isPrimary": true, "elementTypeParams": map[string]any{"max_length": 128}},
		{"fieldName": "vector", "dataType": "FloatVector", "elementTypeParams": map[string]any{"dim": strconv.Itoa(dim)}},
	}
	for _, f := range fields {
		schema = append(schema, map[string]any{"fieldName": f.Name, "dataType": "VarChar", "elementTypeParams": map[string]any{"max_length": f.MaxLen}})
	}
	return m.call(ctx, "/v2/vectordb/collections/create", map[string]any{
		"collectionName": name,
		"schema":         map[string]any{"autoId": false, "enableDynamicField": false, "fields": schema},
		"indexParams":    []map[string]any{{"fieldName": "vector", "metricType": "COSINE", "indexName": "vector", "indexType": "AUTOINDEX"}},
	}, nil)
}

func (m *Milvus) dim(ctx context.Context, name string) (int, error) {
	var desc struct {
		Fields []struct {
			Name   string `json:"name"`
			Params []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"params"`
		} `json:"fields"`
	}
	if err := m.call(ctx, "/v2/vectordb/collections/describe", map[string]any{"collectionName": name}, &desc); err != nil {
		return 0, err
	}
	for _, f := range desc.Fields {
		if f.Name != "vector" {
			continue
		}
		for _, p := range f.Params {
			if p.Key == "dim" {
				return strconv.Atoi(p.Value)
			}
		}
	}
	return 0, fmt.Errorf("milvus: %s has no vector field", name)
}

// DropCollection 删除集合（不存在时不报错）。
func (m *Milvus) DropCollection(ctx context.Context, name string) error {
	return m.call(ctx, "/v2/vectordb/collections/drop", map[string]any{"collectionName": name}, nil)
}

// Upsert 写入或覆盖若干行。
func (m *Milvus) Upsert(ctx context.Context, name string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	return m.call(ctx, "/v2/vectordb/entities/upsert", map[string]any{"collectionName": name, "data": rows}, nil)
}

// Delete 按过滤条件删除。
func (m *Milvus) Delete(ctx context.Context, name, filter string) error {
	return m.call(ctx, "/v2/vectordb/entities/delete", map[string]any{"collectionName": name, "filter": filter}, nil)
}

// Hit 是检索命中：主键和余弦相似度（-1–1）。ImageURL 只在商品图集合里有（SearchFields 请求了它时）。
type Hit struct {
	ID       string  `json:"id"`
	Distance float64 `json:"distance"`
	ImageURL string  `json:"image_url,omitempty"`
}

// Search 用一个向量检索（强一致，刚写入的数据也能查到）。filter 为空表示不过滤。
func (m *Milvus) Search(ctx context.Context, name string, vec []float32, limit int, filter string) ([]Hit, error) {
	return m.SearchFields(ctx, name, vec, limit, filter)
}

// SearchFields 同 Search，另外返回 fields 里的标量字段。
func (m *Milvus) SearchFields(ctx context.Context, name string, vec []float32, limit int, filter string, fields ...string) ([]Hit, error) {
	body := map[string]any{"collectionName": name, "data": [][]float32{vec}, "annsField": "vector", "limit": limit,
		"outputFields": append([]string{"id"}, fields...), "consistencyLevel": "Strong"}
	if filter != "" {
		body["filter"] = filter
	}
	var hits []Hit
	if err := m.call(ctx, "/v2/vectordb/entities/search", body, &hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// quote 把字符串转成 Milvus 过滤表达式里的字面量。
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// inList 生成 `field in ["a","b"]`；values 为空时返回空串（不过滤）。
func inList(field string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = quote(v)
	}
	return field + " in [" + strings.Join(parts, ",") + "]"
}

func and(clauses ...string) string {
	var out []string
	for _, c := range clauses {
		if c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, " and ")
}
