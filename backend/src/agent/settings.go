package agent

import (
	"context"
	"time"
)

// ModelSettings 是一次运行读取的模型设置（可随动态配置变化）。
type ModelSettings struct {
	// PlannerEnabled 用小模型规划意图和生成追问；AgentEnabled 用大模型做工具循环和最终回答。没有 LLM 时两者都无效。
	PlannerEnabled bool
	AgentEnabled   bool
	PlannerModel   string
	AgentModel     string
	// Timeout 是单次模型调用的上限。
	Timeout time.Duration
	// MaxToolRounds 是一次回答最多的工具循环轮数。
	MaxToolRounds int
	// ToolPolicy 是意图 → 工具白名单的 JSON；空表示内置默认。
	ToolPolicy string
	// RerankEnabled 用小模型对商品候选重排（失败按规则顺序）；SummaryEnabled 用小模型生成会话摘要（失败用规则）。
	RerankEnabled  bool
	SummaryEnabled bool
	// MemoryTurns 是参与历史检索的最近轮数；0 表示默认。
	MemoryTurns int
}

// SettingsSource 返回当前设置。
type SettingsSource func(ctx context.Context) ModelSettings

// DefaultModelSettings 是没有配置来源时的设置：模型都打开（是否真用取决于有没有 LLM）。
func DefaultModelSettings() ModelSettings {
	return ModelSettings{PlannerEnabled: true, AgentEnabled: true, PlannerModel: "deepseek-chat", AgentModel: "deepseek-chat",
		Timeout: 30 * time.Second, MaxToolRounds: 6}
}

func (s ModelSettings) normalized() ModelSettings {
	def := DefaultModelSettings()
	if s.Timeout <= 0 {
		s.Timeout = def.Timeout
	}
	if s.MaxToolRounds <= 0 {
		s.MaxToolRounds = def.MaxToolRounds
	}
	if s.MaxToolRounds > 12 {
		s.MaxToolRounds = 12
	}
	if s.MemoryTurns <= 0 {
		s.MemoryTurns = 10
	}
	if s.PlannerModel == "" {
		s.PlannerModel = def.PlannerModel
	}
	if s.AgentModel == "" {
		s.AgentModel = def.AgentModel
	}
	return s
}
