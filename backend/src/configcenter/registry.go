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
	KeyUploadMaxBytes         = Key{Name: "http.upload_max_bytes", Env: "UPLOAD_MAX_BYTES", Default: "10485760"} // 单个上传文件的上限
	KeyUploadAllowedTypes     = Key{Name: "files.allowed_mime_types", Env: "UPLOAD_ALLOWED_MIME_TYPES", Default: "image/jpeg,image/png,image/webp,image/gif,application/pdf"}
	KeyRequestTimeout         = Key{Name: "http.request_timeout", Env: "HTTP_REQUEST_TIMEOUT", Default: "30s"}
	KeyRateLimitIPPerMin      = Key{Name: "http.rate_limit.ip_per_minute", Env: "RATE_LIMIT_IP_PER_MINUTE", Default: "120"}
	KeyRateLimitAccountPerMin = Key{Name: "http.rate_limit.account_per_minute", Env: "RATE_LIMIT_ACCOUNT_PER_MINUTE", Default: "120"}
)

// 认证；运行中可由动态配置调整（环境变量未设置时）。
var (
	KeyAuthTokenTTL        = Key{Name: "auth.token_ttl", Env: "AUTH_TOKEN_TTL", Default: "24h"}
	KeyLoginAttemptsPerMin = Key{Name: "auth.login_attempts_per_minute", Env: "LOGIN_ATTEMPTS_PER_MINUTE", Default: "10"}
	KeyAvatarUploadDir     = Key{Name: "uploads.avatar_dir", Env: "AVATAR_UPLOAD_DIR", Default: "uploads/avatar"} // 仅启动时读取
)

// 对象存储；仅启动时读取。MINIO_ENDPOINT 为空表示未配置，文件接口返回 object_storage_unavailable。
var (
	KeyMinIOEndpoint = Key{Name: "minio.endpoint", Env: "MINIO_ENDPOINT"}
	KeyMinIOBucket   = Key{Name: "minio.bucket", Env: "MINIO_BUCKET", Default: "blink-shop"}
	KeyMinIOUseSSL   = Key{Name: "minio.use_ssl", Env: "MINIO_USE_SSL", Default: "false"}
)

// 外部依赖连接与凭据。
var (
	KeyMySQLDSN       = Key{Name: "mysql.dsn", Env: "MYSQL_DSN", Default: "blink:blink_dev_password@tcp(127.0.0.1:3306)/blink_shop?parseTime=true&loc=Local&charset=utf8mb4", Secret: true}
	KeyMinIOAccessKey = Key{Name: "minio.access_key", Env: "MINIO_ACCESS_KEY", Default: "minioadmin", Secret: true}
	KeyMinIOSecretKey = Key{Name: "minio.secret_key", Env: "MINIO_SECRET_KEY", Default: "minioadmin", Secret: true}
	KeyMilvusToken    = Key{Name: "milvus.token", Env: "MILVUS_TOKEN", Secret: true}
	KeyAIAPIKey       = Key{Name: "ai.api_key", Env: "AI_API_KEY", Secret: true}
)

// 风控；运行中可由动态配置调整。导购 Agent 的运行前检查在 7.2 接入后读取它。
var (
	KeyRiskBlockedWords = Key{Name: "risk.blocked_words", Env: "RISK_BLOCKED_WORDS", Default: "违法,违禁,假货,绕过风控"}
)

// 模型与导购 Agent。密钥（ai.api_key）和服务地址只在启动时读取；其余每次运行读取，可在运行中开关和切换模型。
var (
	KeyAIBaseURL        = Key{Name: "ai.base_url", Env: "AI_BASE_URL", Default: "https://api.deepseek.com/v1"}
	KeyAIPlannerModel   = Key{Name: "ai.planner_model", Env: "AI_PLANNER_MODEL", Default: "deepseek-chat"}
	KeyAIAgentModel     = Key{Name: "ai.agent_model", Env: "AI_AGENT_MODEL", Default: "deepseek-chat"}
	KeyAIPlannerEnabled = Key{Name: "ai.planner_enabled", Env: "AI_PLANNER_ENABLED", Default: "true"}
	KeyAIAgentEnabled   = Key{Name: "ai.agent_enabled", Env: "AI_AGENT_ENABLED", Default: "true"}
	KeyAITimeout        = Key{Name: "ai.timeout", Env: "AI_TIMEOUT", Default: "30s"}
	KeyAIMaxRetries     = Key{Name: "ai.max_retries", Env: "AI_MAX_RETRIES", Default: "2"}
	KeyAIMaxToolRounds  = Key{Name: "ai.max_tool_rounds", Env: "AI_MAX_TOOL_ROUNDS", Default: "6"}
	// KeyAgentToolPolicy 是意图 → 可用工具的白名单（JSON 对象，值为工具名数组）；空表示用内置默认。模型只能在白名单内选工具。
	KeyAgentToolPolicy  = Key{Name: "agent.tool_policy", Env: "AGENT_TOOL_POLICY"}
	KeyAIRerankEnabled  = Key{Name: "ai.rerank_enabled", Env: "AI_RERANK_ENABLED", Default: "false"}
	KeyAISummaryEnabled = Key{Name: "ai.summary_enabled", Env: "AI_SUMMARY_ENABLED", Default: "false"}
	KeyMemoryTurns      = Key{Name: "agent.memory_turns", Env: "AGENT_MEMORY_TURNS", Default: "10"}
)

// 向量检索：Milvus（REST v2）+ OpenAI 兼容 Embedding。两者都配置了才启用，否则只用关键词检索。仅启动时读取。
var (
	KeyMilvusAddr              = Key{Name: "milvus.addr", Env: "MILVUS_ADDR"}
	KeyMilvusTextCollection    = Key{Name: "milvus.text_collection", Env: "MILVUS_TEXT_COLLECTION", Default: "blink_shop_text_chunks"}
	KeyMilvusProductCollection = Key{Name: "milvus.product_collection", Env: "MILVUS_PRODUCT_COLLECTION", Default: "blink_shop_products"}
	KeyEmbeddingBaseURL        = Key{Name: "embedding.base_url", Env: "EMBEDDING_BASE_URL", Default: "https://dashscope.aliyuncs.com/compatible-mode/v1"}
	KeyEmbeddingAPIKey         = Key{Name: "embedding.api_key", Env: "EMBEDDING_API_KEY", Secret: true}
	KeyEmbeddingModel          = Key{Name: "embedding.model", Env: "EMBEDDING_MODEL", Default: "text-embedding-v3"}
	KeyEmbeddingDim            = Key{Name: "embedding.dim", Env: "EMBEDDING_DIM", Default: "1024"}
)

// 图片搜索：Milvus 配置了就启用。图片 Embedding 默认用本地特征（不调外部服务）；dashscope 需要密钥。仅启动时读取。
var (
	KeyMilvusImageCollection  = Key{Name: "milvus.image_collection", Env: "MILVUS_IMAGE_COLLECTION", Default: "blink_shop_product_images"}
	KeyImageEmbeddingProvider = Key{Name: "image_embedding.provider", Env: "IMAGE_EMBEDDING_PROVIDER", Default: "local"}
	KeyImageEmbeddingBaseURL  = Key{Name: "image_embedding.base_url", Env: "IMAGE_EMBEDDING_BASE_URL", Default: "https://dashscope.aliyuncs.com"}
	KeyImageEmbeddingAPIKey   = Key{Name: "image_embedding.api_key", Env: "IMAGE_EMBEDDING_API_KEY", Secret: true}
	KeyImageEmbeddingModel    = Key{Name: "image_embedding.model", Env: "IMAGE_EMBEDDING_MODEL", Default: "qwen3-vl-embedding"}
	KeyImageEmbeddingDim      = Key{Name: "image_embedding.dim", Env: "IMAGE_EMBEDDING_DIM", Default: "512"}
	// KeyImageFetchAllowedHosts 是服务端可以下载商品图的 https 域名（逗号分隔，精确匹配）；为空表示只读平台内图片。
	KeyImageFetchAllowedHosts = Key{Name: "image_search.fetch_allowed_hosts", Env: "IMAGE_FETCH_ALLOWED_HOSTS"}
)

// 语音：识别和合成各选一个供应商，off 表示不提供（接口返回 *_not_enabled）。凭据只在服务端，客户端经后端代理使用。
// mock 不调外部服务（演示 / 测试），生产环境不允许。仅启动时读取。
var (
	KeySTTProvider   = Key{Name: "speech.stt.provider", Env: "STT_PROVIDER", Default: "off"}
	KeySTTAppID      = Key{Name: "speech.stt.app_id", Env: "STT_APP_ID"}
	KeySTTAPIKey     = Key{Name: "speech.stt.api_key", Env: "STT_API_KEY", Secret: true}
	KeySTTAPISecret  = Key{Name: "speech.stt.api_secret", Env: "STT_API_SECRET", Secret: true}
	KeySTTEndpoint   = Key{Name: "speech.stt.endpoint", Env: "STT_ENDPOINT", Default: "wss://office-api-ast-dx.iflyaisol.com/ast/communicate/v1"}
	KeySTTLang       = Key{Name: "speech.stt.lang", Env: "STT_LANG", Default: "autodialect"}
	KeySTTMaxSeconds = Key{Name: "speech.stt.max_seconds", Env: "STT_MAX_SECONDS", Default: "60"}

	KeyTTSProvider     = Key{Name: "speech.tts.provider", Env: "TTS_PROVIDER", Default: "off"}
	KeyTTSAppID        = Key{Name: "speech.tts.app_id", Env: "TTS_APP_ID"}
	KeyTTSAPIKey       = Key{Name: "speech.tts.api_key", Env: "TTS_API_KEY", Secret: true}
	KeyTTSAPISecret    = Key{Name: "speech.tts.api_secret", Env: "TTS_API_SECRET", Secret: true}
	KeyTTSEndpoint     = Key{Name: "speech.tts.endpoint", Env: "TTS_ENDPOINT"}
	KeyTTSDefaultVoice = Key{Name: "speech.tts.default_voice", Env: "TTS_DEFAULT_VOICE"}
	KeyTTSCluster      = Key{Name: "speech.tts.cluster", Env: "TTS_CLUSTER", Default: "volcano_tts"}
	KeyTTSMaxRunes     = Key{Name: "speech.tts.max_runes", Env: "TTS_MAX_RUNES", Default: "800"}
)
