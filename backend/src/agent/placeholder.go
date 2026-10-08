package agent

import (
	"context"
	"time"
	"unicode/utf8"
)

// PlaceholderRunner 是 7.1 的占位运行：没有规划和工具（7.2 接入），只如实说明当前能做什么，不假装调用了模型或工具。
// 文本按小段输出，便于客户端验证流式显示和取消。
type PlaceholderRunner struct {
	// ChunkDelay 是两段文本之间的间隔，0 表示不等待。
	ChunkDelay time.Duration
}

const placeholderReply = "你好，我是 Blink 导购助手。商品推荐、对比、加购和订单查询的能力正在接入中，" +
	"现在可以先在首页搜索和筛选商品，在购物车和“我的订单”里完成购买。"

var placeholderFollowups = []string{"怎么筛选商品？", "下单后多久要付款？"}

func (p PlaceholderRunner) Run(ctx context.Context, in Input, out Output) error {
	start := time.Now()
	out.Thinking(Step{ID: "understand", Title: "理解你的问题", Status: StepRunning})
	if err := p.wait(ctx); err != nil {
		return err
	}
	out.Thinking(Step{ID: "understand", Title: "理解你的问题", Status: StepDone})
	out.Trace("planner", "placeholder", "ok", time.Since(start), map[string]any{"content_runes": utf8.RuneCountInString(in.Content)})
	runes := []rune(placeholderReply)
	for i := 0; i < len(runes); i += 12 {
		if err := p.wait(ctx); err != nil {
			return err
		}
		end := i + 12
		if end > len(runes) {
			end = len(runes)
		}
		out.Text(string(runes[i:end]))
	}
	out.Followups(placeholderFollowups)
	return nil
}

func (p PlaceholderRunner) wait(ctx context.Context) error {
	if p.ChunkDelay <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(p.ChunkDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
