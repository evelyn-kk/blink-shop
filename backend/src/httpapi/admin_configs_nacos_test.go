package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// miniNacos 是只实现 v1 cs/configs 的假 Nacos，可以模拟宕机。
type miniNacos struct {
	mu   sync.Mutex
	data map[string]string
	down atomic.Bool
}

func (m *miniNacos) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	key := r.Form.Get("group") + "/" + r.Form.Get("dataId")
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if v, ok := m.data[key]; ok {
			fmt.Fprint(w, v)
			return
		}
		http.Error(w, "config data not exist", http.StatusNotFound)
	case http.MethodPost:
		m.data[key] = r.Form.Get("content")
		fmt.Fprint(w, "true")
	case http.MethodDelete:
		delete(m.data, key)
		fmt.Fprint(w, "true")
	}
}

// newNacosServer 创建动态配置在（假）Nacos 上的服务；env 是环境变量。
func newNacosServer(t *testing.T, env map[string]string) (*testServer, *miniNacos) {
	t.Helper()
	nacos := &miniNacos{data: map[string]string{}}
	nsrv := httptest.NewServer(nacos)
	t.Cleanup(nsrv.Close)
	dynamic := configcenter.OpenDynamic(configcenter.NacosConfig{Addr: nsrv.URL, Group: "BLINK_SHOP", DataIDPrefix: "blink_shop.config.", Refresh: time.Second})
	resolver := configcenter.NewResolver(func(k string) string { return env[k] }, dynamic)
	mem := memstore.New()
	if _, err := mem.ApplySeed(context.Background(), storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	s := NewServer(Options{Logger: logging.New(logs, slog.LevelDebug), Store: mem, PasswordCost: bcrypt.MinCost, AvatarDir: t.TempDir(),
		Now: func() time.Time { return testNow }, Settings: configcenter.NewHTTPSettingsProvider(resolver, false), Configs: configcenter.NewAdmin(resolver, dynamic),
		RiskWords: func(ctx context.Context) []string { return configcenter.RiskBlockedWords(ctx, resolver) }})
	return &testServer{Server: s, handler: s.Handler(), logs: logs, mem: mem}, nacos
}

type configListResponse struct {
	Items   []configView      `json:"items"`
	Backend configBackendView `json:"backend"`
}

func configValue(t *testing.T, ts *testServer, token, key string) configView {
	t.Helper()
	for _, it := range decodeBody[configListResponse](t, ts.call(t, http.MethodGet, "/api/v1/admin/configs", token, nil)).Items {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("config %s not listed", key)
	return configView{}
}

func TestAdminConfigsOnNacos(t *testing.T) {
	ts, nacos := newNacosServer(t, nil)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	list := decodeBody[configListResponse](t, ts.call(t, http.MethodGet, "/api/v1/admin/configs", admin, nil))
	if list.Backend.Kind != "nacos" || !list.Backend.Healthy {
		t.Fatalf("backend: %+v", list.Backend)
	}
	// 写入：存进 Nacos（每项一个 dataId），本实例立即生效
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/risk.blocked_words", admin, map[string]any{"value": "假货,刷单", "reason": "测试"}), http.StatusOK, "")
	if nacos.data["BLINK_SHOP/blink_shop.config.risk.blocked_words"] != "假货,刷单" {
		t.Fatalf("nacos data: %v", nacos.data)
	}
	if c := configValue(t, ts, admin, "risk.blocked_words"); c.Value != "假货,刷单" || c.Source != "dynamic" {
		t.Fatalf("after write: %+v", c)
	}
	// Nacos 宕机：修改返回 503 不生效，读取沿用最后一次的值，列表标出配置中心不健康
	nacos.down.Store(true)
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/risk.blocked_words", admin, map[string]any{"value": "全新", "reason": "测试"}), http.StatusServiceUnavailable, "config_center_unavailable")
	if c := configValue(t, ts, admin, "risk.blocked_words"); c.Value != "假货,刷单" {
		t.Fatalf("value changed by failed write: %+v", c)
	}
	if b := decodeBody[configListResponse](t, ts.call(t, http.MethodGet, "/api/v1/admin/configs", admin, nil)).Backend; b.Healthy {
		t.Fatalf("backend should be unhealthy: %+v", b)
	}
	// 失败的修改不写审计
	if strings.Contains(ts.logs.String(), `"after":"全新"`) {
		t.Fatal("failed write audited")
	}
	nacos.down.Store(false)
	// 恢复默认：删除 Nacos 里的这一项
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/risk.blocked_words", admin, map[string]any{"value": "", "reason": "测试"}), http.StatusOK, "")
	if _, ok := nacos.data["BLINK_SHOP/blink_shop.config.risk.blocked_words"]; ok {
		t.Fatal("reset left value in nacos")
	}
}

// 密钥类配置的明文不出现在配置列表、Prompt 接口、运行轨迹和日志里。
func TestSecretsNeverExposed(t *testing.T) {
	secrets := map[string]string{
		"AI_API_KEY": "sk-live-ai-9a8b7c", "EMBEDDING_API_KEY": "emb-live-6f5e4d", "MILVUS_TOKEN": "milvus:live-3c2b1a", "MINIO_SECRET_KEY": "minio-live-0z9y8x",
		"NACOS_PASSWORD": "nacos-live-7w6v5u", "TTS_API_KEY": "tts-live-4t3s2r", "STT_API_SECRET": "stt-live-1q0p9o", "IMAGE_EMBEDDING_API_KEY": "img-live-8n7m6l",
	}
	ts, _ := newNacosServer(t, secrets)
	admin := ts.login(t, seed.AdminUsername, seed.DevPassword).Token
	user := ts.login(t, seed.UserUsername, seed.DevPassword).Token
	var bodies []string
	list := ts.call(t, http.MethodGet, "/api/v1/admin/configs", admin, nil)
	bodies = append(bodies, list.Body.String())
	for _, c := range decodeBody[configListResponse](t, list).Items {
		if c.Secret && c.Value != "" && c.Value != secretMask {
			t.Errorf("%s not masked: %q", c.Key, c.Value)
		}
	}
	bodies = append(bodies, ts.call(t, http.MethodGet, promptsPath, admin, nil).Body.String())
	bodies = append(bodies, ts.call(t, http.MethodGet, promptsPath+"/agent.react", admin, nil).Body.String())
	// 密钥不能通过后台修改（也就不会被写进 Nacos 或审计）
	expectStatus(t, ts.call(t, http.MethodPatch, "/api/v1/admin/configs/ai.api_key", admin, map[string]any{"value": "sk-new"}), http.StatusConflict, "")
	// 一次导购运行的轨迹
	sid := decodeBody[sessionView](t, ts.call(t, http.MethodPost, agentSessionsPath, user, map[string]any{})).SessionID
	rec := ts.call(t, http.MethodPost, agentSessionsPath+"/"+sid+"/messages:stream", user, msgBody("s-1", "推荐一款降噪耳机"))
	evs := parseSSE(t, rec.Body.String())
	runID, _ := evs[0].Data["run_id"].(string)
	bodies = append(bodies, rec.Body.String(), ts.call(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/trace", user, nil).Body.String(), ts.logs.String())
	for env, secret := range secrets {
		for i, b := range bodies {
			if strings.Contains(b, secret) {
				t.Errorf("%s leaked in body %d", env, i)
			}
		}
	}
}
