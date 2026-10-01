package configcenter

import (
	"context"
	"os"
	"strings"
	"sync"
)

// Source 是动态配置来源（Nacos 或内存）。查不到或来源不可用时返回 ok=false。
type Source interface {
	Lookup(ctx context.Context, name string) (value string, ok bool)
}

// Resolver 按固定优先级读取配置：环境变量 → 动态配置（Nacos/内存）→ 内置默认值。
// 空字符串视为“未设置”，会继续向下一层查找。
type Resolver struct {
	getenv  func(string) string
	dynamic Source
}

// NewResolver 创建 Resolver；getenv 为 nil 时使用 os.Getenv，dynamic 为 nil 时只读环境变量和默认值。
func NewResolver(getenv func(string) string, dynamic Source) *Resolver {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &Resolver{getenv: getenv, dynamic: dynamic}
}

// String 返回配置值和它的来源（env / dynamic / default）。
func (r *Resolver) String(ctx context.Context, key Key) (string, string) {
	if key.Env != "" {
		if v := strings.TrimSpace(r.getenv(key.Env)); v != "" {
			return v, "env"
		}
	}
	if r.dynamic != nil {
		if v, ok := r.dynamic.Lookup(ctx, key.Name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), "dynamic"
		}
	}
	return key.Default, "default"
}

// Get 只返回配置值。
func (r *Resolver) Get(ctx context.Context, key Key) string {
	v, _ := r.String(ctx, key)
	return v
}

// MemorySource 是线程安全的内存动态配置，用作 Nacos 不可用时的兜底和测试替身。
type MemorySource struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewMemorySource(values map[string]string) *MemorySource {
	copied := make(map[string]string, len(values))
	for k, v := range values {
		copied[k] = v
	}
	return &MemorySource{values: copied}
}

func (m *MemorySource) Lookup(_ context.Context, name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.values[name]
	return v, ok
}

func (m *MemorySource) Set(name, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[name] = value
}
