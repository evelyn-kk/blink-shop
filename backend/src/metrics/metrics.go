// Package metrics 是进程内的监控指标：计数器、直方图和仪表，按 Prometheus 文本格式输出（GET /metrics）。
// 不引入外部依赖；标签值由调用方保证是有限集合（路由模式、状态类别、依赖名），不得放用户输入、ID 或错误原文。
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBuckets 是耗时直方图的默认分桶（秒）：覆盖 5ms 的接口到 60s 的导购运行。
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// Registry 保存全部指标。零值不可用，用 NewRegistry。
type Registry struct {
	mu      sync.Mutex
	metrics map[string]*family
	order   []string
	gauges  []gaugeFunc
}

type kind int

const (
	counterKind kind = iota
	gaugeKind
	histogramKind
)

type family struct {
	name, help string
	kind       kind
	labels     []string
	buckets    []float64
	series     map[string]*series
}

type series struct {
	values []string
	value  float64  // counter / gauge
	counts []uint64 // histogram：每个桶（含 +Inf）
	sum    float64
	count  uint64
}

func NewRegistry() *Registry { return &Registry{metrics: map[string]*family{}} }

// Default 是进程的默认指标集；各包直接用 Inc / Observe 等函数写入。
var Default = NewRegistry()

func (r *Registry) family(name, help string, k kind, labels []string, buckets []float64) *family {
	f, ok := r.metrics[name]
	if !ok {
		f = &family{name: name, help: help, kind: k, labels: labels, buckets: buckets, series: map[string]*series{}}
		r.metrics[name] = f
		r.order = append(r.order, name)
		return f
	}
	if f.kind != k || len(f.labels) != len(labels) {
		panic(fmt.Sprintf("metrics: %s registered with a different type or labels", name))
	}
	return f
}

func (f *family) get(values []string) *series {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("metrics: %s wants %d label values, got %d", f.name, len(f.labels), len(values)))
	}
	key := strings.Join(values, "\x00")
	s, ok := f.series[key]
	if !ok {
		s = &series{values: append([]string(nil), values...)}
		if f.kind == histogramKind {
			s.counts = make([]uint64, len(f.buckets)+1)
		}
		f.series[key] = s
	}
	return s
}

// Counter 是一个计数器指标（只增不减）。
type Counter struct {
	r *Registry
	f *family
}

// Histogram 是一个直方图指标。
type Histogram struct {
	r *Registry
	f *family
}

// Gauge 是一个可增可减的仪表。
type Gauge struct {
	r *Registry
	f *family
}

func (r *Registry) Counter(name, help string, labels ...string) Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Counter{r, r.family(name, help, counterKind, labels, nil)}
}

func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) Histogram {
	if buckets == nil {
		buckets = DefaultBuckets
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return Histogram{r, r.family(name, help, histogramKind, labels, buckets)}
}

func (r *Registry) Gauge(name, help string, labels ...string) Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Gauge{r, r.family(name, help, gaugeKind, labels, nil)}
}

func (c Counter) Add(v float64, values ...string) {
	c.r.mu.Lock()
	c.f.get(values).value += v
	c.r.mu.Unlock()
}

func (c Counter) Inc(values ...string) { c.Add(1, values...) }

// Value 返回当前值（测试用）。
func (c Counter) Value(values ...string) float64 {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	return c.f.get(values).value
}

func (h Histogram) Observe(v float64, values ...string) {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	s := h.f.get(values)
	i := sort.SearchFloat64s(h.f.buckets, v) // 第一个 ≥ v 的桶
	s.counts[i]++
	s.sum += v
	s.count++
}

// Since 记录从 start 到现在的秒数。
func (h Histogram) Since(start time.Time, values ...string) {
	h.Observe(time.Since(start).Seconds(), values...)
}

// Count 返回观测次数（测试用）。
func (h Histogram) Count(values ...string) uint64 {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	return h.f.get(values).count
}

func (g Gauge) Add(v float64, values ...string) {
	g.r.mu.Lock()
	g.f.get(values).value += v
	g.r.mu.Unlock()
}

func (g Gauge) Set(v float64, values ...string) {
	g.r.mu.Lock()
	g.f.get(values).value = v
	g.r.mu.Unlock()
}

func (g Gauge) Value(values ...string) float64 {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()
	return g.f.get(values).value
}

// GaugeFunc 注册一个在输出时才计算的仪表（如数据库连接池状态）；同名再次注册时替换（进程内重建依赖时用新的）。
// fn 不得调用本 Registry 的方法。
func (r *Registry) GaugeFunc(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := gaugeFunc{name: name, emit: func(w func(string, string, []string, []string, float64)) { w(name, help, nil, nil, fn()) }}
	for i := range r.gauges {
		if r.gauges[i].name == name {
			r.gauges[i] = g
			return
		}
	}
	r.gauges = append(r.gauges, g)
}

type gaugeFunc struct {
	name string
	emit func(w func(name, help string, labels []string, values []string, v float64))
}

// WritePrometheus 按 Prometheus 文本格式（0.0.4）输出全部指标，按注册顺序、标签值排序。
func (r *Registry) WritePrometheus(w io.Writer) error {
	r.mu.Lock()
	var b strings.Builder
	for _, name := range r.order {
		f := r.metrics[name]
		typ := map[kind]string{counterKind: "counter", gaugeKind: "gauge", histogramKind: "histogram"}[f.kind]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, escapeHelp(f.help), f.name, typ)
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.series[k]
			if f.kind != histogramKind {
				fmt.Fprintf(&b, "%s%s %s\n", f.name, labelText(f.labels, s.values, "", ""), num(s.value))
				continue
			}
			var cum uint64
			for i, ub := range f.buckets {
				cum += s.counts[i]
				fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelText(f.labels, s.values, "le", num(ub)), cum)
			}
			cum += s.counts[len(f.buckets)]
			fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelText(f.labels, s.values, "le", "+Inf"), cum)
			fmt.Fprintf(&b, "%s_sum%s %s\n%s_count%s %d\n", f.name, labelText(f.labels, s.values, "", ""), num(s.sum),
				f.name, labelText(f.labels, s.values, "", ""), s.count)
		}
	}
	gauges := append([]gaugeFunc{}, r.gauges...)
	r.mu.Unlock()
	for _, g := range gauges {
		g.emit(func(name, help string, labels, values []string, v float64) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s%s %s\n", name, escapeHelp(help), name, name, labelText(labels, values, "", ""), num(v))
		})
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func labelText(names, values []string, extraName, extraValue string) string {
	if len(names) == 0 && extraName == "" {
		return ""
	}
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		parts = append(parts, n+`="`+escapeLabel(values[i])+`"`)
	}
	if extraName != "" {
		parts = append(parts, extraName+`="`+extraValue+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func escapeHelp(s string) string { return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s) }

func num(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
