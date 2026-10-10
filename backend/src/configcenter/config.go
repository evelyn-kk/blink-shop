package configcenter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
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
	MinIOEndpoint        string // 为空表示未配置对象存储
	MinIOAccessKey       string
	MinIOSecretKey       string
	MinIOBucket          string
	MinIOUseSSL          bool
	MilvusToken          string
	AIAPIKey             string
	AIBaseURL            string
	AvatarUploadDir      string
	// 向量检索（都配置了才启用）
	MilvusAddr              string
	MilvusTextCollection    string
	MilvusProductCollection string
	EmbeddingBaseURL        string
	EmbeddingAPIKey         string
	EmbeddingModel          string
	EmbeddingDim            int
	// 图片搜索（配置了 Milvus 就启用）
	MilvusImageCollection  string
	ImageEmbeddingProvider string
	ImageEmbeddingBaseURL  string
	ImageEmbeddingAPIKey   string
	ImageEmbeddingModel    string
	ImageEmbeddingDim      int
	ImageFetchAllowedHosts []string
	Speech                 SpeechConfig
}

// SpeechConfig 是语音识别与合成的供应商配置（Provider 为 off 表示不提供）。
type SpeechConfig struct {
	STTProvider   string
	STTAppID      string
	STTAPIKey     string
	STTAPISecret  string
	STTEndpoint   string
	STTLang       string
	STTMaxSeconds int

	TTSProvider     string
	TTSAppID        string
	TTSAPIKey       string
	TTSAPISecret    string
	TTSEndpoint     string
	TTSDefaultVoice string
	TTSCluster      string
	TTSMaxRunes     int
}

// 各供应商的默认合成地址和音色（TTS_ENDPOINT / TTS_DEFAULT_VOICE 为空时）。
var ttsDefaults = map[string][2]string{
	"xunfei": {"wss://tts-api.xfyun.cn/v2/tts", "xiaoyan"},
	"doubao": {"https://openspeech.bytedance.com/api/v1/tts", "BV700_streaming"},
	"mock":   {"", "mock"},
}

// ImageSearchEnabled 表示可以做图片搜索：配置了 Milvus（本地图片特征不需要外部服务；dashscope 的密钥在 Load 时已校验）。
func (c Config) ImageSearchEnabled() bool {
	return c.MilvusAddr != ""
}

// VectorEnabled 表示 Milvus 和 Embedding 都配置了。
func (c Config) VectorEnabled() bool {
	return c.MilvusAddr != "" && strings.TrimSpace(c.EmbeddingAPIKey) != ""
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
		MinIOEndpoint:   r.Get(ctx, KeyMinIOEndpoint),
		MinIOAccessKey:  r.Get(ctx, KeyMinIOAccessKey),
		MinIOSecretKey:  r.Get(ctx, KeyMinIOSecretKey),
		MinIOBucket:     r.Get(ctx, KeyMinIOBucket),
		MilvusToken:     r.Get(ctx, KeyMilvusToken),
		AIAPIKey:        r.Get(ctx, KeyAIAPIKey),
		AIBaseURL:       strings.TrimSpace(r.Get(ctx, KeyAIBaseURL)),
		AvatarUploadDir: r.Get(ctx, KeyAvatarUploadDir),

		MilvusAddr:              strings.TrimSpace(r.Get(ctx, KeyMilvusAddr)),
		MilvusTextCollection:    strings.TrimSpace(r.Get(ctx, KeyMilvusTextCollection)),
		MilvusProductCollection: strings.TrimSpace(r.Get(ctx, KeyMilvusProductCollection)),
		EmbeddingBaseURL:        strings.TrimSpace(r.Get(ctx, KeyEmbeddingBaseURL)),
		EmbeddingAPIKey:         r.Get(ctx, KeyEmbeddingAPIKey),
		EmbeddingModel:          strings.TrimSpace(r.Get(ctx, KeyEmbeddingModel)),

		MilvusImageCollection:  strings.TrimSpace(r.Get(ctx, KeyMilvusImageCollection)),
		ImageEmbeddingProvider: strings.ToLower(strings.TrimSpace(r.Get(ctx, KeyImageEmbeddingProvider))),
		ImageEmbeddingBaseURL:  strings.TrimSpace(r.Get(ctx, KeyImageEmbeddingBaseURL)),
		ImageEmbeddingAPIKey:   r.Get(ctx, KeyImageEmbeddingAPIKey),
		ImageEmbeddingModel:    strings.TrimSpace(r.Get(ctx, KeyImageEmbeddingModel)),
		ImageFetchAllowedHosts: splitHosts(r.Get(ctx, KeyImageFetchAllowedHosts)),
		Speech: SpeechConfig{
			STTProvider: strings.ToLower(strings.TrimSpace(r.Get(ctx, KeySTTProvider))), STTAppID: strings.TrimSpace(r.Get(ctx, KeySTTAppID)),
			STTAPIKey: strings.TrimSpace(r.Get(ctx, KeySTTAPIKey)), STTAPISecret: strings.TrimSpace(r.Get(ctx, KeySTTAPISecret)),
			STTEndpoint: strings.TrimSpace(r.Get(ctx, KeySTTEndpoint)), STTLang: strings.TrimSpace(r.Get(ctx, KeySTTLang)),
			TTSProvider: strings.ToLower(strings.TrimSpace(r.Get(ctx, KeyTTSProvider))), TTSAppID: strings.TrimSpace(r.Get(ctx, KeyTTSAppID)),
			TTSAPIKey: strings.TrimSpace(r.Get(ctx, KeyTTSAPIKey)), TTSAPISecret: strings.TrimSpace(r.Get(ctx, KeyTTSAPISecret)),
			TTSEndpoint: strings.TrimSpace(r.Get(ctx, KeyTTSEndpoint)), TTSDefaultVoice: strings.TrimSpace(r.Get(ctx, KeyTTSDefaultVoice)),
			TTSCluster: strings.TrimSpace(r.Get(ctx, KeyTTSCluster)),
		},
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
	cfg.MinIOUseSSL = collect(&errs, KeyMinIOUseSSL, func() (bool, error) {
		return parseBool(r.Get(ctx, KeyMinIOUseSSL), false)
	})
	if cfg.MinIOEndpoint != "" && !validBucketName(cfg.MinIOBucket) {
		errs = append(errs, fmt.Errorf("%s: %q 不是合法的桶名（3–63 位小写字母、数字、点或连字符）", KeyMinIOBucket.Env, cfg.MinIOBucket))
	}
	if _, err := parseHTTPSettings(ctx, r); err != nil {
		errs = append(errs, err)
	}
	cfg.EmbeddingDim = int(collect(&errs, KeyEmbeddingDim, func() (int64, error) {
		n, err := strconv.ParseInt(strings.TrimSpace(r.Get(ctx, KeyEmbeddingDim)), 10, 64)
		if err != nil || n < 8 || n > 8192 {
			return 0, errors.New("必须是 8 到 8192 的整数")
		}
		return n, nil
	}))
	for _, name := range []struct {
		key Key
		v   string
	}{{KeyMilvusTextCollection, cfg.MilvusTextCollection}, {KeyMilvusProductCollection, cfg.MilvusProductCollection}, {KeyMilvusImageCollection, cfg.MilvusImageCollection}} {
		if !validCollectionName(name.v) {
			errs = append(errs, fmt.Errorf("%s: %q 不是合法的集合名（字母开头，字母、数字、下划线，最多 255 位）", name.key.Env, name.v))
		}
	}
	if !strings.HasPrefix(cfg.EmbeddingBaseURL, "http://") && !strings.HasPrefix(cfg.EmbeddingBaseURL, "https://") {
		errs = append(errs, fmt.Errorf("%s: %q 必须是 http(s) 地址", KeyEmbeddingBaseURL.Env, cfg.EmbeddingBaseURL))
	}
	cfg.ImageEmbeddingDim = int(collect(&errs, KeyImageEmbeddingDim, func() (int64, error) {
		n, err := strconv.ParseInt(strings.TrimSpace(r.Get(ctx, KeyImageEmbeddingDim)), 10, 64)
		if err != nil || n < 8 || n > 8192 {
			return 0, errors.New("必须是 8 到 8192 的整数")
		}
		return n, nil
	}))
	switch cfg.ImageEmbeddingProvider {
	case "local":
	case "dashscope":
		if strings.TrimSpace(cfg.ImageEmbeddingAPIKey) == "" {
			errs = append(errs, fmt.Errorf("%s=dashscope 时必须配置 %s", KeyImageEmbeddingProvider.Env, KeyImageEmbeddingAPIKey.Env))
		}
		if !strings.HasPrefix(cfg.ImageEmbeddingBaseURL, "https://") && !strings.HasPrefix(cfg.ImageEmbeddingBaseURL, "http://") {
			errs = append(errs, fmt.Errorf("%s: %q 必须是 http(s) 地址", KeyImageEmbeddingBaseURL.Env, cfg.ImageEmbeddingBaseURL))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: %q 无效，可选 local / dashscope", KeyImageEmbeddingProvider.Env, cfg.ImageEmbeddingProvider))
	}
	errs = append(errs, loadSpeech(ctx, r, &cfg.Speech)...)
	for _, h := range cfg.ImageFetchAllowedHosts {
		if !validHost(h) {
			errs = append(errs, fmt.Errorf("%s: %q 不是合法的域名（只写主机名，不带协议、端口或路径）", KeyImageFetchAllowedHosts.Env, h))
		}
	}
	if !strings.HasPrefix(cfg.AIBaseURL, "http://") && !strings.HasPrefix(cfg.AIBaseURL, "https://") {
		errs = append(errs, fmt.Errorf("%s: %q 必须是 http(s) 地址", KeyAIBaseURL.Env, cfg.AIBaseURL))
	}
	if _, err := AgentSettingsOf(ctx, r); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// AgentSettings 是导购 Agent 每次运行读取的模型相关配置（可随动态配置变化）。
type AgentSettings struct {
	PlannerEnabled bool
	AgentEnabled   bool
	PlannerModel   string
	AgentModel     string
	Timeout        time.Duration
	MaxRetries     int
	MaxToolRounds  int
	// ToolPolicy 是意图 → 工具白名单的 JSON；空表示内置默认。
	ToolPolicy     string
	RerankEnabled  bool
	SummaryEnabled bool
	MemoryTurns    int
}

// AgentSettingsOf 读取并解析 Agent 配置；任一项不合法时返回错误（启动时校验用）。
func AgentSettingsOf(ctx context.Context, r *Resolver) (AgentSettings, error) {
	var errs []error
	s := AgentSettings{PlannerModel: strings.TrimSpace(r.Get(ctx, KeyAIPlannerModel)), AgentModel: strings.TrimSpace(r.Get(ctx, KeyAIAgentModel)),
		ToolPolicy: strings.TrimSpace(r.Get(ctx, KeyAgentToolPolicy))}
	s.PlannerEnabled = collect(&errs, KeyAIPlannerEnabled, func() (bool, error) { return parseBool(r.Get(ctx, KeyAIPlannerEnabled), true) })
	s.AgentEnabled = collect(&errs, KeyAIAgentEnabled, func() (bool, error) { return parseBool(r.Get(ctx, KeyAIAgentEnabled), true) })
	s.RerankEnabled = collect(&errs, KeyAIRerankEnabled, func() (bool, error) { return parseBool(r.Get(ctx, KeyAIRerankEnabled), false) })
	s.SummaryEnabled = collect(&errs, KeyAISummaryEnabled, func() (bool, error) { return parseBool(r.Get(ctx, KeyAISummaryEnabled), false) })
	s.MemoryTurns = int(collect(&errs, KeyMemoryTurns, func() (int64, error) {
		n, err := strconv.ParseInt(strings.TrimSpace(r.Get(ctx, KeyMemoryTurns)), 10, 64)
		if err != nil || n < 1 || n > 50 {
			return 0, errors.New("必须是 1 到 50 的整数")
		}
		return n, nil
	}))
	s.Timeout = collect(&errs, KeyAITimeout, func() (time.Duration, error) { return parsePositiveDuration(r.Get(ctx, KeyAITimeout)) })
	s.MaxRetries = int(collect(&errs, KeyAIMaxRetries, func() (int64, error) {
		n, err := strconv.ParseInt(strings.TrimSpace(r.Get(ctx, KeyAIMaxRetries)), 10, 64)
		if err != nil || n < 0 || n > 5 {
			return 0, errors.New("必须是 0 到 5 的整数")
		}
		return n, nil
	}))
	s.MaxToolRounds = int(collect(&errs, KeyAIMaxToolRounds, func() (int64, error) {
		n, err := strconv.ParseInt(strings.TrimSpace(r.Get(ctx, KeyAIMaxToolRounds)), 10, 64)
		if err != nil || n < 1 || n > 12 {
			return 0, errors.New("必须是 1 到 12 的整数")
		}
		return n, nil
	}))
	if s.PlannerModel == "" || s.AgentModel == "" {
		errs = append(errs, errors.New("AI_PLANNER_MODEL / AI_AGENT_MODEL 不能为空"))
	}
	if err := toolPolicy(s.ToolPolicy); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", KeyAgentToolPolicy.Env, err))
	}
	return s, errors.Join(errs...)
}

// AgentSettingsNow 读取当前配置；某项不合法时该项回到默认值（运行中改坏一项不影响服务）。
func AgentSettingsNow(ctx context.Context, r *Resolver) AgentSettings {
	s, err := AgentSettingsOf(ctx, r)
	if err == nil {
		return s
	}
	def := AgentSettings{PlannerEnabled: true, AgentEnabled: true, PlannerModel: KeyAIPlannerModel.Default, AgentModel: KeyAIAgentModel.Default,
		Timeout: 30 * time.Second, MaxRetries: 2, MaxToolRounds: 6}
	if s.Timeout <= 0 {
		s.Timeout = def.Timeout
	}
	if s.MaxToolRounds <= 0 {
		s.MaxToolRounds = def.MaxToolRounds
	}
	if s.PlannerModel == "" {
		s.PlannerModel = def.PlannerModel
	}
	if s.AgentModel == "" {
		s.AgentModel = def.AgentModel
	}
	if toolPolicy(s.ToolPolicy) != nil {
		s.ToolPolicy = ""
	}
	return s
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
	// 模型是可选的：AI_API_KEY 为空表示不接模型（导购走规则），生产允许；非空时不能是示例值。
	devAIKeys = []string{"changeme", "your-api-key", "sk-xxx"}
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
	// 向量检索是可选的：没配 MILVUS_ADDR 时不检查令牌
	if cfg.MilvusAddr != "" && contains(devMilvusTokens, cfg.MilvusToken) {
		errs = append(errs, errors.New("MILVUS_TOKEN 不能为空或使用默认凭证（使用 Milvus 时）"))
	}
	if key := strings.ToLower(strings.TrimSpace(cfg.EmbeddingAPIKey)); key != "" && contains(devAIKeys, key) {
		errs = append(errs, errors.New("EMBEDDING_API_KEY 不能使用示例值"))
	}
	if key := strings.ToLower(strings.TrimSpace(cfg.ImageEmbeddingAPIKey)); key != "" && contains(devAIKeys, key) {
		errs = append(errs, errors.New("IMAGE_EMBEDDING_API_KEY 不能使用示例值"))
	}
	// 语音：mock 只用于演示和测试；凭据不能是示例值
	if cfg.Speech.STTProvider == "mock" || cfg.Speech.TTSProvider == "mock" {
		errs = append(errs, errors.New("STT_PROVIDER / TTS_PROVIDER 不能是 mock（生产环境请配置真实供应商或 off）"))
	}
	for env, v := range map[string]string{"STT_API_KEY": cfg.Speech.STTAPIKey, "STT_API_SECRET": cfg.Speech.STTAPISecret,
		"TTS_API_KEY": cfg.Speech.TTSAPIKey, "TTS_API_SECRET": cfg.Speech.TTSAPISecret} {
		if key := strings.ToLower(v); key != "" && contains(devAIKeys, key) {
			errs = append(errs, fmt.Errorf("%s 不能使用示例值", env))
		}
	}
	if key := strings.ToLower(strings.TrimSpace(cfg.AIAPIKey)); key != "" && contains(devAIKeys, key) {
		errs = append(errs, errors.New("AI_API_KEY 不能使用示例值（为空表示不接模型，导购走规则）"))
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
	UploadMaxBytes         int64    // 单个上传文件的上限；multipart 请求体另留少量余量给表单分隔符
	UploadAllowedTypes     []string // 上传文件允许的 MIME（按内容嗅探结果比较），是 UploadableTypes 的子集
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
	s.UploadAllowedTypes = parseOr(&errs, KeyUploadAllowedTypes, get, parseUploadTypes)
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

// UploadableTypes 是服务端能够嗅探并安全回传的文件类型。配置只能从中挑选，不能放开 HTML、SVG 等可执行内容。
var UploadableTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/bmp", "application/pdf"}

func parseUploadTypes(value string) ([]string, error) {
	var out []string
	for _, item := range splitList(value) {
		item = strings.ToLower(item)
		if !contains(UploadableTypes, item) {
			return nil, fmt.Errorf("%q 不在可上传类型 %s 中", item, strings.Join(UploadableTypes, ","))
		}
		if !contains(out, item) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("至少允许一种文件类型")
	}
	return out, nil
}

// validBucketName 按 S3 桶命名规则的常用子集检查：3–63 位，小写字母/数字开头结尾，中间可有点和连字符。
func validBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || i == len(name)-1 || (c != '.' && c != '-')) {
			return false
		}
	}
	return true
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

// RiskBlockedWords 返回当前生效的风险词表（环境变量 > 动态配置 > 默认值）。
func RiskBlockedWords(ctx context.Context, r *Resolver) []string {
	return splitList(r.Get(ctx, KeyRiskBlockedWords))
}

// loadSpeech 校验语音配置：供应商名、所需凭据、地址协议和数值范围；补上合成的默认地址和音色。
func loadSpeech(ctx context.Context, r *Resolver, sp *SpeechConfig) []error {
	var errs []error
	intIn := func(k Key, lo, hi int64) int {
		return int(collect(&errs, k, func() (int64, error) {
			n, err := strconv.ParseInt(strings.TrimSpace(r.Get(ctx, k)), 10, 64)
			if err != nil || n < lo || n > hi {
				return 0, fmt.Errorf("必须是 %d 到 %d 的整数", lo, hi)
			}
			return n, nil
		}))
	}
	sp.STTMaxSeconds = intIn(KeySTTMaxSeconds, 5, 300)
	sp.TTSMaxRunes = intIn(KeyTTSMaxRunes, 1, 2000)
	wsURL := func(k Key, v string) {
		if !strings.HasPrefix(v, "wss://") && !strings.HasPrefix(v, "ws://") {
			errs = append(errs, fmt.Errorf("%s: %q 必须是 ws(s) 地址", k.Env, v))
		}
	}
	switch sp.STTProvider {
	case "off", "mock":
	case "xunfei":
		if sp.STTAppID == "" || sp.STTAPIKey == "" || sp.STTAPISecret == "" {
			errs = append(errs, fmt.Errorf("%s=xunfei 时必须配置 STT_APP_ID、STT_API_KEY、STT_API_SECRET", KeySTTProvider.Env))
		}
		wsURL(KeySTTEndpoint, sp.STTEndpoint)
	default:
		errs = append(errs, fmt.Errorf("%s: %q 无效，可选 off / mock / xunfei", KeySTTProvider.Env, sp.STTProvider))
	}
	d, known := ttsDefaults[sp.TTSProvider]
	if sp.TTSEndpoint == "" && known {
		sp.TTSEndpoint = d[0]
	}
	if sp.TTSDefaultVoice == "" && known {
		sp.TTSDefaultVoice = d[1]
	}
	switch sp.TTSProvider {
	case "off", "mock":
	case "xunfei":
		if sp.TTSAppID == "" || sp.TTSAPIKey == "" || sp.TTSAPISecret == "" {
			errs = append(errs, fmt.Errorf("%s=xunfei 时必须配置 TTS_APP_ID、TTS_API_KEY、TTS_API_SECRET", KeyTTSProvider.Env))
		}
		wsURL(KeyTTSEndpoint, sp.TTSEndpoint)
	case "doubao":
		if sp.TTSAppID == "" || sp.TTSAPIKey == "" {
			errs = append(errs, fmt.Errorf("%s=doubao 时必须配置 TTS_APP_ID、TTS_API_KEY（token）", KeyTTSProvider.Env))
		}
		if !strings.HasPrefix(sp.TTSEndpoint, "https://") && !strings.HasPrefix(sp.TTSEndpoint, "http://") {
			errs = append(errs, fmt.Errorf("%s: %q 必须是 http(s) 地址", KeyTTSEndpoint.Env, sp.TTSEndpoint))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: %q 无效，可选 off / mock / xunfei / doubao", KeyTTSProvider.Env, sp.TTSProvider))
	}
	if !voicePattern.MatchString(sp.TTSDefaultVoice) && sp.TTSDefaultVoice != "" {
		errs = append(errs, fmt.Errorf("%s: %q 只能是字母、数字、下划线和短横线", KeyTTSDefaultVoice.Env, sp.TTSDefaultVoice))
	}
	return errs
}

// voicePattern 是音色名的格式（也用于校验客户端传来的 voice）。
var voicePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidVoice 判断音色名格式是否合法。
func ValidVoice(v string) bool { return voicePattern.MatchString(v) }

// splitHosts 按逗号拆分域名列表，去空白、转小写、去掉空项。
func splitHosts(raw string) []string {
	var out []string
	for _, h := range strings.Split(raw, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// validHost：只允许域名（至少两段），不接受 IP、端口、协议或路径——IP 白名单绕过了“不连内网”的意图。
func validHost(h string) bool {
	return len(h) <= 253 && hostPattern.MatchString(h) && net.ParseIP(h) == nil && !strings.HasSuffix(h, ".localhost")
}

// validCollectionName 是 Milvus 集合名规则：字母或下划线开头，只含字母、数字、下划线，最多 255 位。
func validCollectionName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
