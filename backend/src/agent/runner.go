package agent

import (
	"context"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// Input 是一次运行的输入。
type Input struct {
	AccountID   string
	SessionID   string
	RunID       string
	TraceID     string
	Content     string
	Attachments []domain.Attachment
}

// Step 是展示给用户的一个思考步骤（同一个 ID 的步骤会被后来的状态覆盖）。
type Step struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // running / done
}

const (
	StepRunning = "running"
	StepDone    = "done"
)

// Output 接收运行产生的内容：一边推给客户端，一边记录用于持久化和轨迹。
// 推送失败（客户端断开）不会返回错误；运行应该通过 ctx 感知取消。
type Output interface {
	Thinking(s Step)
	Text(delta string)
	// Block 输出一个结构化块，至少有 type 字段（product_list、comparison、citation、action 等）。
	Block(b map[string]any)
	Followups(questions []string)
	// Trace 记录一条运行轨迹（不推给客户端）。
	Trace(stage, eventType, status string, duration time.Duration, metadata map[string]any)
}

// Runner 执行一次导购运行。ctx 被取消（用户取消、客户端断开、超时、服务关闭）时应尽快返回 ctx.Err()。
// 返回 nil 表示正常完成；返回的错误不会展示给用户。
type Runner interface {
	Run(ctx context.Context, in Input, out Output) error
}

// RunnerFunc 让普通函数实现 Runner。
type RunnerFunc func(ctx context.Context, in Input, out Output) error

func (f RunnerFunc) Run(ctx context.Context, in Input, out Output) error { return f(ctx, in, out) }
