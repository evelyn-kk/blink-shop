package configcenter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Config 是进程启动时读取一次的配置。
type Config struct {
	AppEnv               string
	APIAddr              string
	MySQLDSN             string
	RunMigrations        bool
	BootstrapVectorIndex bool
	MinIOAccessKey       string
	MinIOSecretKey       string
	MilvusToken          string
	AIAPIKey             string
	AvatarUploadDir      string
}

// IsProduction 判断是否生产环境（production / prod）。
func (c Config) IsProduction() bool {
	return isProductionEnv(c.AppEnv)
}

func isProductionEnv(env string) bool {
	env = strings.ToLower(strings.TrimSpace(env))
	return env == "production" || env == "prod"
}

// Load 读取启动配置，并校验所有 HTTP 配置都能被解析；任何格式错误都会返回。
func Load(ctx context.Context, r *Resolver) (Config, error) {
	cfg := Config{
		AppEnv:          strings.ToLower(r.Get(ctx, KeyAppEnv)),
		APIAddr:         r.Get(ctx, KeyAPIAddr),
		MySQLDSN:        r.Get(ctx, KeyMySQLDSN),
		MinIOAccessKey:  r.Get(ctx, KeyMinIOAccessKey),
		MinIOSecretKey:  r.Get(ctx, KeyMinIOSecretKey),
		MilvusToken:     r.Get(ctx, KeyMilvusToken),
		AIAPIKey:        r.Get(ctx, KeyAIAPIKey),
		AvatarUploadDir: r.Get(ctx, KeyAvatarUploadDir),
	}
	switch cfg.AppEnv {
	case "development", "test", "production", "prod":
	default:
		return Config{}, fmt.Errorf("APP_ENV=%q 无效，可选 development / test / production", cfg.AppEnv)
	}

	var errs []error
	// 迁移和向量索引初始化默认只在非生产环境自动执行。
	cfg.RunMigrations = collect(&errs, KeyRunMigrations, func() (bool, error) {
		return parseBool(r.Get(ctx, KeyRunMigrations), !cfg.IsProduction())
	})
	cfg.BootstrapVectorIndex = collect(&errs, KeyBootstrapVectorIndex, func() (bool, error) {
		return parseBool(r.Get(ctx, KeyBootstrapVectorIndex), !cfg.IsProduction())
	})
	if _, err := parseHTTPSettings(ctx, r); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func collect[T any](errs *[]error, key Key, parse func() (T, error)) T {
	v, err := parse()
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key.Env, err))
	}
	return v
}

// 已知的开发/示例默认值，生产环境一律拒绝。
var (
	devDBPasswords  = []string{"", "root", "password", "123456", "blink_dev_password", "blink_dev_root"}
	devMinIOValues  = []string{"", "minioadmin"}
	devMilvusTokens = []string{"", "root:Milvus"}
	devAIKeys       = []string{"", "changeme", "your-api-key", "sk-xxx"}
)

// ValidateProduction 检查生产环境的危险配置；非生产环境直接通过。返回所有问题，而不是只报第一个。
func ValidateProduction(ctx context.Context, cfg Config, r *Resolver) error {
	if !cfg.IsProduction() {
		return nil
	}
	var errs []error
	if dsn, err := mysql.ParseDSN(cfg.MySQLDSN); err != nil {
		errs = append(errs, errors.New("MYSQL_DSN 无法解析"))
	} else if contains(devDBPasswords, dsn.Passwd) {
		errs = append(errs, errors.New("MYSQL_DSN 不能使用空密码或开发默认密码"))
	}
	if origins := splitList(r.Get(ctx, KeyCORSAllowedOrigins)); len(origins) == 0 || contains(origins, "*") {
		errs = append(errs, errors.New("CORS_ALLOWED_ORIGINS 必须是明确的域名列表，不能为空或 *"))
	}
	if contains(devMinIOValues, cfg.MinIOAccessKey) || contains(devMinIOValues, cfg.MinIOSecretKey) {
		errs = append(errs, errors.New("MINIO_ACCESS_KEY / MINIO_SECRET_KEY 不能为空或使用默认凭证"))
	}
	if contains(devMilvusTokens, cfg.MilvusToken) {
		errs = append(errs, errors.New("MILVUS_TOKEN 不能为空或使用默认凭证"))
	}
	if contains(devAIKeys, strings.ToLower(cfg.AIAPIKey)) {
		errs = append(errs, errors.New("AI_API_KEY 不能为空或使用示例值"))
	}
	if trustAll, _ := parseBool(r.Get(ctx, KeyTrustAllProxies), false); trustAll {
		errs = append(errs, errors.New("TRUST_ALL_PROXIES 不能在生产环境开启"))
	}
	return errors.Join(errs...)
}

// HTTPSettings 是每个请求读取的 HTTP 行为配置，可随动态配置变化。
type HTTPSettings struct {
	CORSAllowedOrigins     []string
	TrustedProxies         []*net.IPNet
	TrustAllProxies        bool
	MaxBodyBytes           int64
	UploadMaxBytes         int64
	RequestTimeout         time.Duration
	RateLimitIPPerMin      int
	RateLimitAccountPerMin int
	AuthTokenTTL           time.Duration
	LoginAttemptsPerMin    int // 同一用户名每分钟最多尝试登录次数
}

// HTTPSettingsProvider 每次调用都按当前配置返回 HTTPSettings。
type HTTPSettingsProvider struct {
	resolver   *Resolver
	production bool
}

func NewHTTPSettingsProvider(r *Resolver, production bool) *HTTPSettingsProvider {
	return &HTTPSettingsProvider{resolver: r, production: production}
}

// Current 返回当前生效的 HTTP 配置。动态配置被改成非法值时，对应项退回内置默认值，不影响服务。
func (p *HTTPSettingsProvider) Current(ctx context.Context) HTTPSettings {
	s, _ := parseHTTPSettings(ctx, p.resolver)
	if p.production {
		// 防止运行中把生产 CORS 改成 *：生产环境忽略通配符和“信任所有代理”。
		s.CORSAllowedOrigins = without(s.CORSAllowedOrigins, "*")
		s.TrustAllProxies = false
	}
	return s
}

// parseHTTPSettings 逐项解析；某项非法时该项使用内置默认值，并在返回的 error 中报告。
func parseHTTPSettings(ctx context.Context, r *Resolver) (HTTPSettings, error) {
	var errs []error
	get := func(key Key) (string, string) { return r.Get(ctx, key), key.Default }
	s := HTTPSettings{CORSAllowedOrigins: splitList(r.Get(ctx, KeyCORSAllowedOrigins))}
	s.TrustedProxies = parseOr(&errs, KeyTrustedProxyCIDRs, get, parseCIDRs)
	s.TrustAllProxies = parseOr(&errs, KeyTrustAllProxies, get, func(v string) (bool, error) { return parseBool(v, false) })
	s.MaxBodyBytes = parseOr(&errs, KeyMaxBodyBytes, get, parsePositiveInt)
	s.UploadMaxBytes = parseOr(&errs, KeyUploadMaxBytes, get, parsePositiveInt)
	s.RequestTimeout = parseOr(&errs, KeyRequestTimeout, get, parsePositiveDuration)
	s.RateLimitIPPerMin = int(parseOr(&errs, KeyRateLimitIPPerMin, get, parsePositiveInt))
	s.RateLimitAccountPerMin = int(parseOr(&errs, KeyRateLimitAccountPerMin, get, parsePositiveInt))
	s.AuthTokenTTL = parseOr(&errs, KeyAuthTokenTTL, get, parsePositiveDuration)
	s.LoginAttemptsPerMin = int(parseOr(&errs, KeyLoginAttemptsPerMin, get, parsePositiveInt))
	return s, errors.Join(errs...)
}

// parseOr 解析当前值；失败时记录错误并改用 key 的默认值。
func parseOr[T any](errs *[]error, key Key, get func(Key) (string, string), parse func(string) (T, error)) T {
	value, fallback := get(key)
	v, err := parse(value)
	if err == nil {
		return v
	}
	*errs = append(*errs, fmt.Errorf("%s: %w", key.Env, err))
	v, _ = parse(fallback)
	return v
}

func parseBool(value string, fallback bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return fallback, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return fallback, fmt.Errorf("%q 不是合法的布尔值", value)
}

func parsePositiveInt(value string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q 必须是正整数", value)
	}
	return n, nil
}

func parsePositiveDuration(value string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q 必须是正的时长，例如 30s", value)
	}
	return d, nil
}

// parseCIDRs 解析逗号分隔的 CIDR 或单个 IP。
func parseCIDRs(value string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, item := range splitList(value) {
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				return nil, fmt.Errorf("%q 不是合法的 IP 或 CIDR", item)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("%q 不是合法的 IP 或 CIDR", item)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func without(list []string, v string) []string {
	out := list[:0:0]
	for _, item := range list {
		if item != v {
			out = append(out, item)
		}
	}
	return out
}
