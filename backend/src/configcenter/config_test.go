package configcenter

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolverPriority(t *testing.T) {
	ctx := context.Background()
	key := Key{Name: "demo.value", Env: "DEMO_VALUE", Default: "from-default"}
	tests := []struct {
		name       string
		env        map[string]string
		dynamic    map[string]string
		want       string
		wantSource string
	}{
		{"default only", nil, nil, "from-default", "default"},
		{"dynamic over default", nil, map[string]string{"demo.value": "from-dynamic"}, "from-dynamic", "dynamic"},
		{"env over dynamic", map[string]string{"DEMO_VALUE": "from-env"}, map[string]string{"demo.value": "from-dynamic"}, "from-env", "env"},
		{"blank env falls through", map[string]string{"DEMO_VALUE": "  "}, map[string]string{"demo.value": "from-dynamic"}, "from-dynamic", "dynamic"},
		{"blank dynamic falls through", nil, map[string]string{"demo.value": ""}, "from-default", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewResolver(envOf(tt.env), NewMemorySource(tt.dynamic))
			got, source := r.String(ctx, key)
			if got != tt.want || source != tt.wantSource {
				t.Fatalf("got (%q, %q), want (%q, %q)", got, source, tt.want, tt.wantSource)
			}
		})
	}
}

func TestResolverWithoutDynamicSource(t *testing.T) {
	r := NewResolver(envOf(nil), nil)
	if got := r.Get(context.Background(), KeyAPIAddr); got != ":8080" {
		t.Fatalf("API_ADDR default = %q", got)
	}
}

func TestLoad(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string
		check   func(t *testing.T, cfg Config)
	}{
		{
			name: "development defaults",
			check: func(t *testing.T, cfg Config) {
				if cfg.AppEnv != "development" || cfg.APIAddr != ":8080" || !cfg.RunMigrations || !cfg.BootstrapVectorIndex {
					t.Fatalf("unexpected defaults: %+v", cfg)
				}
				if !strings.Contains(cfg.MySQLDSN, "blink:blink_dev_password@tcp(127.0.0.1:3306)/blink_shop") {
					t.Fatalf("dev DSN should match compose defaults: %s", cfg.MySQLDSN)
				}
				// 对象存储默认不配置：必须显式给出 MINIO_ENDPOINT。
				if cfg.MinIOEndpoint != "" || cfg.MinIOBucket != "blink-shop" || cfg.MinIOUseSSL {
					t.Fatalf("unexpected object storage defaults: %+v", cfg)
				}
			},
		},
		{
			name: "object storage",
			env:  map[string]string{"MINIO_ENDPOINT": "minio.internal:9000", "MINIO_BUCKET": "blink-files.v2", "MINIO_USE_SSL": "true"},
			check: func(t *testing.T, cfg Config) {
				if cfg.MinIOEndpoint != "minio.internal:9000" || cfg.MinIOBucket != "blink-files.v2" || !cfg.MinIOUseSSL {
					t.Fatalf("object storage config: %+v", cfg)
				}
			},
		},
		{name: "invalid bucket name", env: map[string]string{"MINIO_ENDPOINT": "m:9000", "MINIO_BUCKET": "Blink_Shop"}, wantErr: []string{"MINIO_BUCKET"}},
		{name: "bucket too short", env: map[string]string{"MINIO_ENDPOINT": "m:9000", "MINIO_BUCKET": "ab"}, wantErr: []string{"MINIO_BUCKET"}},
		{name: "bucket ends with dash", env: map[string]string{"MINIO_ENDPOINT": "m:9000", "MINIO_BUCKET": "blink-"}, wantErr: []string{"MINIO_BUCKET"}},
		{
			name: "production turns off auto migration by default",
			env:  map[string]string{"APP_ENV": "Production"},
			check: func(t *testing.T, cfg Config) {
				if !cfg.IsProduction() || cfg.RunMigrations || cfg.BootstrapVectorIndex {
					t.Fatalf("production defaults wrong: %+v", cfg)
				}
			},
		},
		{
			name: "explicit flags win",
			env:  map[string]string{"APP_ENV": "production", "RUN_MIGRATIONS": "yes", "BOOTSTRAP_VECTOR_INDEX": "0"},
			check: func(t *testing.T, cfg Config) {
				if !cfg.RunMigrations || cfg.BootstrapVectorIndex {
					t.Fatalf("explicit flags ignored: %+v", cfg)
				}
			},
		},
		{name: "unknown APP_ENV", env: map[string]string{"APP_ENV": "staging"}, wantErr: []string{"APP_ENV"}},
		{
			name: "all invalid values reported together",
			env: map[string]string{
				"RUN_MIGRATIONS":            "maybe",
				"HTTP_MAX_BODY_BYTES":       "-1",
				"HTTP_REQUEST_TIMEOUT":      "soon",
				"RATE_LIMIT_IP_PER_MINUTE":  "0",
				"TRUSTED_PROXY_CIDRS":       "10.0.0.0/33",
				"MINIO_USE_SSL":             "sometimes",
				"UPLOAD_ALLOWED_MIME_TYPES": "image/png,text/html",
			},
			wantErr: []string{"RUN_MIGRATIONS", "HTTP_MAX_BODY_BYTES", "HTTP_REQUEST_TIMEOUT", "RATE_LIMIT_IP_PER_MINUTE", "TRUSTED_PROXY_CIDRS", "MINIO_USE_SSL", "UPLOAD_ALLOWED_MIME_TYPES"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(ctx, NewResolver(envOf(tt.env), nil))
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatal("expected error")
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q should mention %s", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

func safeProductionEnv() map[string]string {
	return map[string]string{
		"APP_ENV":              "production",
		"MYSQL_DSN":            "blink_app:S3cure-Long-Pass@tcp(db.internal:3306)/blink_shop?parseTime=true",
		"CORS_ALLOWED_ORIGINS": "https://admin.blink.example",
		"MINIO_ACCESS_KEY":     "blink-prod-access",
		"MINIO_SECRET_KEY":     "blink-prod-secret-value",
		"MILVUS_TOKEN":         "blink:prod-token",
		"AI_API_KEY":           "sk-live-real-key",
	}
}

func TestValidateProduction(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		override map[string]string
		wantErr  []string
	}{
		{name: "safe production config passes"},
		{name: "development skips checks", override: map[string]string{"APP_ENV": "development", "MYSQL_DSN": "root:root@tcp(127.0.0.1:3306)/x", "CORS_ALLOWED_ORIGINS": "*", "AI_API_KEY": ""}},
		{name: "dev db password", override: map[string]string{"MYSQL_DSN": "blink:blink_dev_password@tcp(127.0.0.1:3306)/blink_shop"}, wantErr: []string{"MYSQL_DSN"}},
		{name: "root:root", override: map[string]string{"MYSQL_DSN": "root:root@tcp(127.0.0.1:3306)/blink_shop"}, wantErr: []string{"MYSQL_DSN"}},
		{name: "empty db password", override: map[string]string{"MYSQL_DSN": "root@tcp(127.0.0.1:3306)/blink_shop"}, wantErr: []string{"MYSQL_DSN"}},
		{name: "wildcard cors", override: map[string]string{"CORS_ALLOWED_ORIGINS": "https://admin.blink.example,*"}, wantErr: []string{"CORS_ALLOWED_ORIGINS"}},
		{name: "default minio", override: map[string]string{"MINIO_SECRET_KEY": "minioadmin"}, wantErr: []string{"MINIO"}},
		{name: "default milvus", override: map[string]string{"MILVUS_TOKEN": "root:Milvus"}, wantErr: []string{"MILVUS_TOKEN"}},
		{name: "no ai key is allowed (rules only)", override: map[string]string{"AI_API_KEY": ""}},
		{name: "blank ai key is allowed", override: map[string]string{"AI_API_KEY": "   "}},
		{name: "placeholder ai key", override: map[string]string{"AI_API_KEY": "ChangeMe"}, wantErr: []string{"AI_API_KEY"}},
		{name: "placeholder ai key 2", override: map[string]string{"AI_API_KEY": "your-api-key"}, wantErr: []string{"AI_API_KEY"}},
		{name: "placeholder ai key 3", override: map[string]string{"AI_API_KEY": "sk-xxx"}, wantErr: []string{"AI_API_KEY"}},
		{name: "trust all proxies", override: map[string]string{"TRUST_ALL_PROXIES": "true"}, wantErr: []string{"TRUST_ALL_PROXIES"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := safeProductionEnv()
			for k, v := range tt.override {
				env[k] = v
			}
			r := NewResolver(envOf(env), nil)
			cfg, err := Load(ctx, r)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			err = ValidateProduction(ctx, cfg, r)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %s", err, want)
				}
			}
		})
	}
}

func TestValidateProductionReportsEveryProblem(t *testing.T) {
	ctx := context.Background()
	r := NewResolver(envOf(map[string]string{"APP_ENV": "production"}), nil)
	cfg, err := Load(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateProduction(ctx, cfg, r)
	if err == nil {
		t.Fatal("production with all defaults must fail")
	}
	for _, want := range []string{"MYSQL_DSN", "CORS_ALLOWED_ORIGINS", "MINIO", "MILVUS_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
	// 没有 AI_API_KEY 不是问题：模型可选，导购走规则
	if strings.Contains(err.Error(), "AI_API_KEY") {
		t.Errorf("empty AI_API_KEY must not be reported: %v", err)
	}
	// 错误信息不能带出密钥原文
	if strings.Contains(err.Error(), "blink_dev_password") || strings.Contains(err.Error(), "minioadmin") {
		t.Errorf("error leaked secret: %v", err)
	}
}

func TestHTTPSettingsProvider(t *testing.T) {
	ctx := context.Background()
	dynamic := NewMemorySource(nil)
	p := NewHTTPSettingsProvider(NewResolver(envOf(map[string]string{"HTTP_MAX_BODY_BYTES": "2048"}), dynamic), false)

	s := p.Current(ctx)
	if s.MaxBodyBytes != 2048 || s.UploadMaxBytes != 10485760 || s.RequestTimeout != 30*time.Second ||
		s.RateLimitIPPerMin != 120 || s.RateLimitAccountPerMin != 120 || len(s.CORSAllowedOrigins) != 1 {
		t.Fatalf("unexpected settings: %+v", s)
	}

	// 动态配置即时生效；环境变量已设置的项不被动态配置覆盖。
	dynamic.Set(KeyRateLimitIPPerMin.Name, "5")
	dynamic.Set(KeyMaxBodyBytes.Name, "9999")
	s = p.Current(ctx)
	if s.RateLimitIPPerMin != 5 || s.MaxBodyBytes != 2048 {
		t.Fatalf("dynamic precedence wrong: %+v", s)
	}

	// 动态配置被写成非法值时退回默认值，而不是 0 或报错停服。
	dynamic.Set(KeyRateLimitIPPerMin.Name, "lots")
	dynamic.Set(KeyRequestTimeout.Name, "-5s")
	s = p.Current(ctx)
	if s.RateLimitIPPerMin != 120 || s.RequestTimeout != 30*time.Second {
		t.Fatalf("invalid dynamic value should fall back to default: %+v", s)
	}
}

func TestUploadAllowedTypes(t *testing.T) {
	ctx := context.Background()
	dynamic := NewMemorySource(nil)
	p := NewHTTPSettingsProvider(NewResolver(envOf(nil), dynamic), false)
	want := []string{"image/jpeg", "image/png", "image/webp", "image/gif", "application/pdf"}
	if got := p.Current(ctx).UploadAllowedTypes; !reflect.DeepEqual(got, want) {
		t.Fatalf("default allowed types = %v", got)
	}
	// 大小写和空白规范化、去重。
	dynamic.Set(KeyUploadAllowedTypes.Name, " IMAGE/PNG , image/png,application/pdf ")
	if got := p.Current(ctx).UploadAllowedTypes; !reflect.DeepEqual(got, []string{"image/png", "application/pdf"}) {
		t.Fatalf("normalized allowed types = %v", got)
	}
	// 任何一项超出可上传范围（或为空）整项退回默认值，不会部分放开。
	for _, bad := range []string{"text/html", "image/png,image/svg+xml", "application/octet-stream", " , "} {
		dynamic.Set(KeyUploadAllowedTypes.Name, bad)
		if got := p.Current(ctx).UploadAllowedTypes; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: allowed types = %v, want default", bad, got)
		}
	}
}

func TestHTTPSettingsProviderProductionGuards(t *testing.T) {
	dynamic := NewMemorySource(map[string]string{
		KeyCORSAllowedOrigins.Name: "https://admin.blink.example,*",
		KeyTrustAllProxies.Name:    "true",
	})
	s := NewHTTPSettingsProvider(NewResolver(envOf(nil), dynamic), true).Current(context.Background())
	if len(s.CORSAllowedOrigins) != 1 || s.CORSAllowedOrigins[0] != "https://admin.blink.example" {
		t.Fatalf("production must drop wildcard origin: %v", s.CORSAllowedOrigins)
	}
	if s.TrustAllProxies {
		t.Fatal("production must not trust all proxies")
	}
}

func TestParseCIDRs(t *testing.T) {
	nets, err := parseCIDRs("10.0.0.0/8, 192.168.1.1, ::1")
	if err != nil || len(nets) != 3 {
		t.Fatalf("parseCIDRs = %v, %v", nets, err)
	}
	if ones, bits := nets[1].Mask.Size(); ones != 32 || bits != 32 {
		t.Fatalf("single IPv4 should be /32, got /%d of %d", ones, bits)
	}
	if _, err := parseCIDRs("10.0.0.0/8,nope"); err == nil {
		t.Fatal("expected error for invalid entry")
	}
}
