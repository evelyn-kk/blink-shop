package configcenter

// Key 描述一个配置项：动态配置键、对应的环境变量、默认值和是否为密钥。
type Key struct {
	Name    string // 动态配置中心（Nacos/内存）使用的键，例如 http.cors.allowed_origins
	Env     string // 对应的环境变量名，例如 CORS_ALLOWED_ORIGINS
	Default string
	Secret  bool // 密钥类配置：日志、列表和 trace 中永远不出现明文
}

// 运行环境与启动行为。
var (
	KeyAppEnv               = Key{Name: "app.env", Env: "APP_ENV", Default: "development"}
	KeyAPIAddr              = Key{Name: "http.addr", Env: "API_ADDR", Default: ":8080"}
	KeyRunMigrations        = Key{Name: "app.run_migrations", Env: "RUN_MIGRATIONS"}                 // 默认值随环境决定
	KeyBootstrapVectorIndex = Key{Name: "app.bootstrap_vector_index", Env: "BOOTSTRAP_VECTOR_INDEX"} // 默认值随环境决定
)

// HTTP 行为；运行中可由动态配置调整（环境变量未设置时）。
var (
	KeyCORSAllowedOrigins     = Key{Name: "http.cors.allowed_origins", Env: "CORS_ALLOWED_ORIGINS", Default: "*"}
	KeyTrustedProxyCIDRs      = Key{Name: "http.trusted_proxy_cidrs", Env: "TRUSTED_PROXY_CIDRS"}
	KeyTrustAllProxies        = Key{Name: "http.trust_all_proxies", Env: "TRUST_ALL_PROXIES", Default: "false"}
	KeyMaxBodyBytes           = Key{Name: "http.max_body_bytes", Env: "HTTP_MAX_BODY_BYTES", Default: "1048576"}
	KeyUploadMaxBytes         = Key{Name: "http.upload_max_bytes", Env: "UPLOAD_MAX_BYTES", Default: "10485760"}
	KeyRequestTimeout         = Key{Name: "http.request_timeout", Env: "HTTP_REQUEST_TIMEOUT", Default: "30s"}
	KeyRateLimitIPPerMin      = Key{Name: "http.rate_limit.ip_per_minute", Env: "RATE_LIMIT_IP_PER_MINUTE", Default: "120"}
	KeyRateLimitAccountPerMin = Key{Name: "http.rate_limit.account_per_minute", Env: "RATE_LIMIT_ACCOUNT_PER_MINUTE", Default: "120"}
)

// 外部依赖连接与凭据。
var (
	KeyMySQLDSN       = Key{Name: "mysql.dsn", Env: "MYSQL_DSN", Default: "blink:blink_dev_password@tcp(127.0.0.1:3306)/blink_shop?parseTime=true&loc=Local&charset=utf8mb4", Secret: true}
	KeyMinIOAccessKey = Key{Name: "minio.access_key", Env: "MINIO_ACCESS_KEY", Default: "minioadmin", Secret: true}
	KeyMinIOSecretKey = Key{Name: "minio.secret_key", Env: "MINIO_SECRET_KEY", Default: "minioadmin", Secret: true}
	KeyMilvusToken    = Key{Name: "milvus.token", Env: "MILVUS_TOKEN", Secret: true}
	KeyAIAPIKey       = Key{Name: "ai.api_key", Env: "AI_API_KEY", Secret: true}
)
