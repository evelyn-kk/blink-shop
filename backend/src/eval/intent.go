package eval

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
)

// DefaultIntentMinPass 是意图评测的默认门槛：开发集要求全对，留出集（held_out）只用来看规则的泛化，允许少量没覆盖的说法。
const DefaultIntentMinPass = 0.9

// IntentCase 是一条意图用例：一句话（可带图）经规则规划后的意图、动作和关键槽位。
type IntentCase struct {
	ID           string   `json:"id"`
	Query        string   `json:"query"`
	HasImage     bool     `json:"has_image"`
	ExpectIntent string   `json:"expect_intent"`
	ExpectAction string   `json:"expect_action"`
	ExpectBudget string   `json:"expect_budget"`
	ExpectBrands []string `json:"expect_brands"`
	ExpectExcl   []string `json:"expect_exclude"`
	Group        string   `json:"group"`
	HeldOut      bool     `json:"held_out"`
}

// IntentOptions 是意图评测的输入。
type IntentOptions struct {
	// MinPass 是门槛通过率，0 表示 DefaultIntentMinPass。
	MinPass float64
	Cases   string
	Version string
	Config  map[string]string
}

// RunIntent 用规则规划（与线上导购相同的 agent.Classify）跑意图评测。指标：意图准确率、动作准确率、留出集准确率和槽位准确率。
func RunIntent(ctx context.Context, o IntentOptions) (Report, error) {
	cases, err := readJSONL[IntentCase](o.Cases)
	if err != nil {
		return Report{}, err
	}
	start := time.Now()
	rep := Report{Suite: "intent", Version: o.Version, Config: map[string]string{"planner": "rule"}, StartedAt: start.UTC(), Metrics: map[string]float64{}}
	for k, v := range o.Config {
		rep.Config[k] = v
	}
	var intentOK, actionCases, actionOK, heldCases, heldOK, slotCases, slotOK int
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		p := agent.Classify(c.Query, c.HasImage)
		var problems []string
		if string(p.Intent) == c.ExpectIntent {
			intentOK++
		} else {
			problems = append(problems, fmt.Sprintf("意图应为 %s", c.ExpectIntent))
		}
		if c.ExpectAction != "" {
			actionCases++
			if p.Action == c.ExpectAction {
				actionOK++
			} else {
				problems = append(problems, fmt.Sprintf("动作应为 %s", c.ExpectAction))
			}
		}
		if c.ExpectBudget != "" || len(c.ExpectBrands) > 0 || len(c.ExpectExcl) > 0 {
			slotCases++
			before := len(problems)
			if c.ExpectBudget != "" && p.Budget.String() != c.ExpectBudget {
				problems = append(problems, "预算上限应为 "+c.ExpectBudget)
			}
			for _, b := range c.ExpectBrands {
				if !slices.Contains(p.Brands, b) {
					problems = append(problems, "应识别品牌 "+b)
				}
			}
			for _, x := range c.ExpectExcl {
				if !slices.Contains(p.Exclude, x) {
					problems = append(problems, "应排除 "+x)
				}
			}
			if len(problems) == before {
				slotOK++
			}
		}
		if c.HeldOut {
			heldCases++
			if len(problems) == 0 {
				heldOK++
			}
		}
		group := c.Group
		if group == "" {
			group = c.ExpectIntent
		}
		got := []string{string(p.Intent) + "/" + p.Action}
		if p.Budget != 0 {
			got = append(got, "budget="+p.Budget.String())
		}
		rep.record(group, Failure{ID: c.ID, Query: c.Query, Problems: problems, Got: got})
	}
	ratio := func(a, b int) float64 {
		if b == 0 {
			return 1
		}
		return round(float64(a) / float64(b))
	}
	rep.Metrics["intent_accuracy"] = ratio(intentOK, len(cases))
	rep.Metrics["action_accuracy"] = ratio(actionOK, actionCases)
	rep.Metrics["held_out_accuracy"] = ratio(heldOK, heldCases)
	rep.Metrics["slot_accuracy"] = ratio(slotOK, slotCases)
	if o.MinPass == 0 {
		o.MinPass = DefaultIntentMinPass
	}
	rep.finish(start, o.MinPass)
	return rep, nil
}
