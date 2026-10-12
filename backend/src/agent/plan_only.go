package agent

import (
	"context"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// PlanOnly 只做一次规划（与 Run 里同一段代码：规则基线 + 可选小模型，失败回退规则），不读会话记忆、不调用工具。
// 供离线评测单独衡量规划质量；planner.model 轨迹写到 out。
func (r *RuleRunner) PlanOnly(ctx context.Context, content string, withImage bool, out Output) Plan {
	in := Input{Content: content}
	if withImage {
		in.Attachments = []domain.Attachment{{FileID: "eval-image", MimeType: "image/png"}}
	}
	return r.plan(ctx, in, r.modelsFor(ctx), out, history{})
}
