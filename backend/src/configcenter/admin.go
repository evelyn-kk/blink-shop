package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Setting 是管理端能看到的一个配置项：说明、运行中能否修改，以及修改时的校验。
// 只有 Runtime 的配置会在运行中按动态配置生效；其余只在启动时读取，管理端只读。
type Setting struct {
	Key         Key
	Description string
	Runtime     bool
	validate    func(string) error
}

// Validate 校验要写入的新值；空串表示删除动态配置、退回默认值，总是允许。
func (s Setting) Validate(value string) error {
	if strings.TrimSpace(value) == "" || s.validate == nil {
		return nil
	}
	return s.validate(value)
}

func boolValue(v string) error {
	_, err := parseBool(v, false)
	return err
}

func positiveInt(v string) error {
	_, err := parsePositiveInt(v)
	return err
}

func positiveDuration(v string) error {
	_, err := parsePositiveDuration(v)
	return err
}

func cidrs(v string) error {
	_, err := parseCIDRs(v)
	return err
}

func uploadTypes(v string) error {
	_, err := parseUploadTypes(v)
	return err
}

func origins(v string) error {
	for _, o := range splitList(v) {
		if o != "*" && !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
			return fmt.Errorf("%q 必须是 * 或 http(s):// 开头的来源", o)
		}
	}
	return nil
}

// MaxRiskWords / MaxRiskWordRunes 限制风险词表的规模。
const (
	MaxRiskWords     = 200
	MaxRiskWordRunes = 20
)

func riskWords(v string) error {
	words := splitList(v)
	if len(words) > MaxRiskWords {
		return fmt.Errorf("最多 %d 个风险词", MaxRiskWords)
	}
	for _, w := range words {
		if utf8.RuneCountInString(w) > MaxRiskWordRunes {
			return fmt.Errorf("风险词 %q 超过 %d 个字", w, MaxRiskWordRunes)
		}
	}
	return nil
}

func nonEmpty(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("不能为空")
	}
	return nil
}

func retries(v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > 5 {
		return errors.New("必须是 0 到 5 的整数")
	}
	return nil
}

func memoryTurns(v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > 50 {
		return errors.New("必须是 1 到 50 的整数")
	}
	return nil
}

func toolRounds(v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > 12 {
		return errors.New("必须是 1 到 12 的整数")
	}
	return nil
}

// toolPolicy 只校验形状：JSON 对象，键是意图名，值是工具名数组。工具名是否存在由 Agent 加载时判断（未知的忽略）。
func toolPolicy(v string) error {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var m map[string][]string
	if err := json.Unmarshal([]byte(v), &m); err != nil {
		return errors.New("必须是 JSON 对象，值为工具名数组")
	}
	return nil
}

// settings 按管理端展示的顺序列出全部配置项。
var settings = []Setting{
	{Key: KeyAppEnv, Description: "运行环境（development / test / production）"},
	{Key: KeyAPIAddr, Description: "监听地址"},
	{Key: KeyRunMigrations, Description: "启动时执行数据库迁移"},
	{Key: KeyBootstrapVectorIndex, Description: "启动时初始化向量索引"},
	{Key: KeyCORSAllowedOrigins, Description: "允许跨域的来源，逗号分隔；生产环境忽略 *", Runtime: true, validate: origins},
	{Key: KeyTrustedProxyCIDRs, Description: "可信反向代理的 CIDR 或 IP，逗号分隔", Runtime: true, validate: cidrs},
	{Key: KeyTrustAllProxies, Description: "信任所有代理（仅本地调试；生产环境始终为 false）", Runtime: true, validate: boolValue},
	{Key: KeyMaxBodyBytes, Description: "JSON 请求体上限（字节）", Runtime: true, validate: positiveInt},
	{Key: KeyUploadMaxBytes, Description: "单个上传文件上限（字节）", Runtime: true, validate: positiveInt},
	{Key: KeyUploadAllowedTypes, Description: "允许上传的文件类型，只能从 JPG/PNG/WebP/GIF/BMP/PDF 中选", Runtime: true, validate: uploadTypes},
	{Key: KeyRequestTimeout, Description: "非流式请求超时，例如 30s", Runtime: true, validate: positiveDuration},
	{Key: KeyRateLimitIPPerMin, Description: "未登录请求每个 IP 每分钟上限", Runtime: true, validate: positiveInt},
	{Key: KeyRateLimitAccountPerMin, Description: "已登录请求每个账号每分钟上限", Runtime: true, validate: positiveInt},
	{Key: KeyAuthTokenTTL, Description: "登录有效期，例如 24h", Runtime: true, validate: positiveDuration},
	{Key: KeyLoginAttemptsPerMin, Description: "同一账号每分钟登录尝试上限", Runtime: true, validate: positiveInt},
	{Key: KeyRiskBlockedWords, Description: "导购对话的风险词，逗号分隔，命中即拦截", Runtime: true, validate: riskWords},
	{Key: KeyAIPlannerEnabled, Description: "导购用小模型规划意图和生成追问（未配置 API Key 时始终走规则）", Runtime: true, validate: boolValue},
	{Key: KeyAIAgentEnabled, Description: "导购用大模型做工具循环和最终回答（关闭时回到规则回答）", Runtime: true, validate: boolValue},
	{Key: KeyAIPlannerModel, Description: "规划 / 追问用的小模型名", Runtime: true, validate: nonEmpty},
	{Key: KeyAIAgentModel, Description: "工具循环 / 最终回答用的大模型名", Runtime: true, validate: nonEmpty},
	{Key: KeyAITimeout, Description: "单次模型调用超时，例如 30s", Runtime: true, validate: positiveDuration},
	{Key: KeyAIMaxRetries, Description: "模型调用失败（限流、5xx、网络）的重试次数，0–5", Runtime: true, validate: retries},
	{Key: KeyAIMaxToolRounds, Description: "一次回答最多的工具循环轮数，1–12", Runtime: true, validate: toolRounds},
	{Key: KeyAgentToolPolicy, Description: "意图 → 可用工具白名单（JSON，如 {\"cart\":[\"get_cart\"]}），空表示内置默认；未知工具会被忽略，模型不能绕过", Runtime: true, validate: toolPolicy},
	{Key: KeyAIRerankEnabled, Description: "商品搜索结果用小模型重排（失败按规则顺序）", Runtime: true, validate: boolValue},
	{Key: KeyAISummaryEnabled, Description: "会话摘要用小模型生成（失败用规则）", Runtime: true, validate: boolValue},
	{Key: KeyMemoryTurns, Description: "导购记忆参与检索的最近轮数，1–50", Runtime: true, validate: memoryTurns},
	{Key: KeyAIBaseURL, Description: "模型服务地址（OpenAI 兼容）"},
	{Key: KeyMilvusAddr, Description: "Milvus 地址（REST，如 127.0.0.1:19530）；为空表示不用向量检索"},
	{Key: KeyMilvusTextCollection, Description: "知识分块向量集合名"},
	{Key: KeyMilvusProductCollection, Description: "商品向量集合名"},
	{Key: KeyEmbeddingBaseURL, Description: "Embedding 服务地址（OpenAI 兼容 /embeddings）"},
	{Key: KeyEmbeddingModel, Description: "Embedding 模型名"},
	{Key: KeyEmbeddingDim, Description: "Embedding 向量维度（换模型或维度要重建集合）"},
	{Key: KeyAvatarUploadDir, Description: "头像文件目录"},
	{Key: KeyMinIOEndpoint, Description: "对象存储地址"},
	{Key: KeyMinIOBucket, Description: "对象存储桶名"},
	{Key: KeyMinIOUseSSL, Description: "对象存储使用 HTTPS"},
	{Key: KeyMySQLDSN, Description: "MySQL 连接串"},
	{Key: KeyMinIOAccessKey, Description: "对象存储 Access Key"},
	{Key: KeyMinIOSecretKey, Description: "对象存储 Secret Key"},
	{Key: KeyMilvusToken, Description: "Milvus 访问令牌"},
	{Key: KeyAIAPIKey, Description: "模型服务 API Key"},
	{Key: KeyEmbeddingAPIKey, Description: "Embedding 服务 API Key"},
}

// Settings 返回全部配置项（副本）。
func Settings() []Setting {
	return append([]Setting(nil), settings...)
}

// Entry 是配置项的当前值和来源（env / dynamic / default）。Value 是明文，由调用方决定是否掩码。
type Entry struct {
	Setting
	Value  string
	Source string
}

var (
	ErrUnknownKey      = errors.New("配置项不存在")
	ErrNotRuntime      = errors.New("该配置只在启动时读取，不能在运行中修改")
	ErrOverriddenByEnv = errors.New("该配置由环境变量指定，动态修改不会生效")
)

// Admin 读取全部配置的当前值，并把运行中可调的配置写入动态配置（目前是内存，9.1 接入 Nacos）。
type Admin struct {
	resolver *Resolver
	dynamic  *MemorySource
}

func NewAdmin(r *Resolver, dynamic *MemorySource) *Admin {
	return &Admin{resolver: r, dynamic: dynamic}
}

func (a *Admin) entry(ctx context.Context, s Setting) Entry {
	v, source := a.resolver.String(ctx, s.Key)
	return Entry{Setting: s, Value: v, Source: source}
}

func (a *Admin) List(ctx context.Context) []Entry {
	out := make([]Entry, 0, len(settings))
	for _, s := range settings {
		out = append(out, a.entry(ctx, s))
	}
	return out
}

func (a *Admin) Get(ctx context.Context, name string) (Entry, error) {
	for _, s := range settings {
		if s.Key.Name == name {
			return a.entry(ctx, s), nil
		}
	}
	return Entry{}, ErrUnknownKey
}

// Set 校验并写入动态配置，返回修改前后的值；value 为空表示删除动态配置、退回默认值。
func (a *Admin) Set(ctx context.Context, name, value string) (before, after Entry, err error) {
	before, err = a.Get(ctx, name)
	switch {
	case err != nil:
		return Entry{}, Entry{}, err
	case !before.Runtime || before.Key.Secret:
		return Entry{}, Entry{}, ErrNotRuntime
	case before.Source == "env":
		return Entry{}, Entry{}, ErrOverriddenByEnv
	}
	value = strings.TrimSpace(value)
	if err := before.Validate(value); err != nil {
		return Entry{}, Entry{}, err
	}
	a.dynamic.Set(name, value)
	return before, a.entry(ctx, before.Setting), nil
}
