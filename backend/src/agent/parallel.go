package agent

import (
	"context"
	"sync"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/llm"
)

// 规划和第一步并行（任务 12.3 的 B2）：开了模型规划时，规划约 0.9s 一直排在工具循环前面。现在按规则规划（加记忆继承）
// 的意图先把工具循环的第一步发出去，和模型规划同时进行；规划回来后：
//   - 意图相同：采用第一步的结果（工具白名单就是这个意图的白名单）。工具在规划回来之后才执行，参数照样按模型规划里的
//     用户约束改写（品牌、预算），所以这一步不会比原来多做任何事，只是不用再等规划；
//   - 意图不同：丢掉第一步，按模型规划的意图从头走工具循环，和原来完全一样（多花一次调用的 token）。
// 第一步的输出在采用前只缓存、不推给用户；它唯一和原来不同的地方是提示里的“规划结果”是规则规划而不是模型规划。

// firstStep 是并行发出的第一步。
type firstStep struct {
	plan     Plan
	messages []llm.Message
	version  int
	start    time.Time
	cancel   context.CancelFunc
	done     chan struct{}

	mu     sync.Mutex
	deltas []string
	resp   llm.Response
	err    error
}

// startFirstStep 按 plan 发出工具循环第一步（后台进行，不写任何输出）。
func (r *RuleRunner) startFirstStep(ctx context.Context, st ModelSettings, in Input, hist history, plan Plan) *firstStep {
	f := &firstStep{plan: plan, start: r.deps.Now(), done: make(chan struct{})}
	f.messages, f.version = r.reactMessages(ctx, in, plan, hist)
	callCtx, cancel := context.WithTimeout(ctx, st.Timeout)
	f.cancel = cancel
	req := r.reactRequest(st, f.messages)
	go func() {
		defer close(f.done)
		resp, err := r.deps.LLM.Stream(callCtx, req, func(d string) error {
			f.mu.Lock()
			f.deltas = append(f.deltas, d)
			f.mu.Unlock()
			return nil
		})
		f.mu.Lock()
		f.resp, f.err = resp, err
		f.mu.Unlock()
	}()
	return f
}

// join 等第一步结束；不采用时先取消它。
func (f *firstStep) join(adopt bool) {
	if !adopt {
		f.cancel()
	}
	<-f.done
}

// replay 把缓存的增量按顺序交给 onDelta（与直接流式调用时一样），返回这一步的结果。
func (f *firstStep) replay(onDelta func(string) error) (llm.Response, error) {
	defer f.cancel()
	f.mu.Lock()
	deltas, resp, err := f.deltas, f.resp, f.err
	f.mu.Unlock()
	for _, d := range deltas {
		if derr := onDelta(d); derr != nil {
			return resp, derr
		}
	}
	return resp, err
}
