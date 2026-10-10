package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
)

// 多轮“刚才第一个”：中间隔了一轮不展示商品的问答，仍按最近一次展示的商品卡解析。
func TestMemoryOrdinalAcrossTurns(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	e.run(t, seed.User2ID, sid, "推荐一款手机")
	e.run(t, seed.User2ID, sid, "有什么优惠券")
	e.run(t, seed.User2ID, sid, "七天无理由怎么退")
	r := e.run(t, seed.User2ID, sid, "把刚才推荐的第二个加入购物车")
	c := e.cart(t, seed.User2ID)
	if len(c.Items) != 1 || c.Items[0].ProductID != "p_seed_vista" {
		t.Fatalf("ordinal across turns: %+v %q", c.Items, r.text.String())
	}
	if m := r.traceMeta("memory", "retrieval"); m["turns"] != 3 || m["visible_products"] != 2 {
		t.Fatalf("memory meta: %v", m)
	}
}

// 追问继承：没有品类锚点、只补了条件时沿用最近的商品需求；有自己的品类或只是闲聊时不继承。
func TestMemoryFollowUpInheritsNeed(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	e.run(t, seed.User2ID, sid, "推荐一款拍照好的手机")
	r := e.run(t, seed.User2ID, sid, "那 3000 以内的呢")
	pl := r.block(BlockProductList)
	if pl == nil || strings.Join(productIDs(pl), ",") != "p_seed_nova" {
		t.Fatalf("inherited search: %v %q", r.blockTypes(), r.text.String())
	}
	if m := r.traceMeta("memory", "retrieval"); m["inherited"] != true || !strings.Contains(fmt.Sprint(m["query"]), "手机") {
		t.Fatalf("memory meta: %v", m)
	}
	if pm := r.traceMeta("planner", "rule"); pm["budget"] != "3000.00" || pm["inherited"] != true {
		t.Fatalf("plan: %v", pm)
	}
	r = e.run(t, seed.User2ID, sid, "还有别的吗，不要 Nova")
	if pl := r.block(BlockProductList); pl == nil || strings.Join(productIDs(pl), ",") != "p_seed_vista" {
		t.Fatalf("follow-up with exclusion: %v %q", r.blockTypes(), r.text.String())
	}
	// 有自己的品类：不继承
	r = e.run(t, seed.User2ID, sid, "那耳机呢")
	if m := r.traceMeta("memory", "retrieval"); m["inherited"] != false || productIDs(r.block(BlockProductList))[0] != "p_seed_earbuds" {
		t.Fatalf("own anchor: %v", m)
	}
	// 闲聊不继承
	r = e.run(t, seed.User2ID, sid, "嗯嗯好的呢")
	if m := r.traceMeta("memory", "retrieval"); m["inherited"] != false || r.block(BlockProductList) != nil {
		t.Fatalf("chit-chat: %v %v", m, r.blockTypes())
	}
	// 没有上文的商品需求：不继承（新会话）
	sid2 := e.newSession(t, seed.User2ID)
	r = e.run(t, seed.User2ID, sid2, "那 3000 以内的呢")
	if m := r.traceMeta("memory", "retrieval"); m["inherited"] != false {
		t.Fatalf("no history: %v", m)
	}
}

func TestMemorySummary(t *testing.T) {
	e := newEnv(t)
	sid := e.newSession(t, seed.User2ID)
	e.run(t, seed.User2ID, sid, "推荐一款通勤降噪耳机")
	r := e.run(t, seed.User2ID, sid, "看看我的购物车")
	cs, _ := e.mem.GetChatSession(context.Background(), seed.User2ID, sid)
	if cs.Summary != "用户咨询：商品推荐（通勤降噪耳机）；购物车" || r.traceMeta("memory", "summary")["mode"] != "rule" {
		t.Fatalf("summary: %q", cs.Summary)
	}
	// 截断：很多轮长需求后摘要不超过 500 字，保留最近的主题
	for i := 0; i < 40; i++ {
		e.run(t, seed.User2ID, sid, fmt.Sprintf("推荐一款型号 X%03d 的超长需求描述测试用办公无线静音鼠标", i))
	}
	cs, _ = e.mem.GetChatSession(context.Background(), seed.User2ID, sid)
	if n := utf8.RuneCountInString(cs.Summary); n > maxSummaryRunes || !strings.HasPrefix(cs.Summary, summaryPrefix) || !strings.Contains(cs.Summary, "x039") || strings.Contains(cs.Summary, "x000") {
		t.Fatalf("truncated summary (%d runes): %q", n, cs.Summary)
	}
	// 手机号等写进摘要前脱敏
	e.run(t, seed.User2ID, sid, "13800001234 帮我推荐耳机")
	cs, _ = e.mem.GetChatSession(context.Background(), seed.User2ID, sid)
	if strings.Contains(cs.Summary, "13800001234") {
		t.Fatalf("summary leaks phone: %q", cs.Summary)
	}
	// 用户手动改过的摘要不覆盖
	if _, err := e.mem.UpdateChatSession(context.Background(), seed.User2ID, sid, func(c *domain.ChatSession) error { c.Summary = "我自己的备注"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.run(t, seed.User2ID, sid, "推荐一款手机")
	cs, _ = e.mem.GetChatSession(context.Background(), seed.User2ID, sid)
	if cs.Summary != "我自己的备注" {
		t.Fatalf("user summary overwritten: %q", cs.Summary)
	}
	// 模型摘要（开关打开时）；模型失败回到规则
	mock := &llm.Mock{}
	st := settings(false, false)
	st.SummaryEnabled = true
	e.withModel(mock, st)
	sid3 := e.newSession(t, seed.User2ID)
	mock.Then(llm.Text(`{"summary":"想买通勤降噪耳机，电话 13900001111"}`))
	e.run(t, seed.User2ID, sid3, "推荐一款通勤降噪耳机")
	cs, _ = e.mem.GetChatSession(context.Background(), seed.User2ID, sid3)
	if cs.Summary != "用户咨询：想买通勤降噪耳机，电话 [手机号]" {
		t.Fatalf("model summary: %q", cs.Summary)
	}
	mock.Then(llm.Fail(context.DeadlineExceeded))
	e.run(t, seed.User2ID, sid3, "看看我的购物车")
	cs, _ = e.mem.GetChatSession(context.Background(), seed.User2ID, sid3)
	if !strings.Contains(cs.Summary, "购物车") {
		t.Fatalf("rule fallback summary: %q", cs.Summary)
	}
}

// 其他用户的会话读不到任何记忆：没有历史轮次、没有可信商品、不能借别人的商品卡加购。
func TestMemoryIsolation(t *testing.T) {
	e := newEnv(t)
	other := e.newSession(t, seed.UserID)
	e.run(t, seed.UserID, other, "推荐一款手机")
	h := loadMemory(context.Background(), e.mem, seed.User2ID, other, "", 10)
	if len(h.Turns) != 0 || len(h.Evidence) != 0 || len(h.Cards) != 0 || h.Summary != "" {
		t.Fatalf("leaked memory: %+v", h)
	}
	if got := h.recall("手机"); len(got) != 0 {
		t.Fatalf("recall leaked: %+v", got)
	}
	own := loadMemory(context.Background(), e.mem, seed.UserID, other, "", 10)
	if len(own.Turns) != 1 || len(own.Cards) != 2 {
		t.Fatalf("own memory: %+v", own)
	}
}

// 规划和工具循环拿到相关历史作为上下文；轨迹只记消息 ID，不记历史原文和手机号。
func TestMemoryContextForModelAndRedaction(t *testing.T) {
	e := newEnv(t)
	mock := &llm.Mock{}
	e.withModel(mock, settings(true, false))
	sid := e.newSession(t, seed.User2ID)
	mock.Then(llm.Text(`{"intent":"product_search","query":"降噪耳机"}`))
	e.run(t, seed.User2ID, sid, "推荐一款通勤降噪耳机，我电话 13800001234")
	mock.Then(llm.Text(`{"intent":"review","action":"list","query":"降噪耳机"}`))
	r := e.run(t, seed.User2ID, sid, "那款降噪耳机的评价怎么样")
	prompt := mock.Last().Messages[1].Content
	if !strings.Contains(prompt, "相关的历史对话") || !strings.Contains(prompt, "推荐一款通勤降噪耳机") {
		t.Fatalf("planner prompt lacks memory: %q", prompt)
	}
	ids := r.traceMeta("memory", "retrieval")["relevant_message_ids"].([]string)
	if len(ids) != 1 {
		t.Fatalf("relevant ids: %v", ids)
	}
	for _, tr := range r.traces {
		if raw := fmt.Sprint(tr.Meta); strings.Contains(raw, "13800001234") {
			t.Fatalf("phone in trace %s.%s: %v", tr.Stage, tr.Type, tr.Meta)
		}
	}
	// 工具参数里的手机号也脱敏
	san := SanitizeArgs(map[string]any{"query": "耳机 13800001234 a@b.com"})
	if san["query"] != "耳机 [手机号] [邮箱]" {
		t.Fatalf("sanitize: %v", san)
	}
	if RedactPII("身份证 11010119900307123X 卡号 6222020200112233445") != "身份证 [证件号] 卡号 [卡号]" {
		t.Fatalf("redact: %q", RedactPII("身份证 11010119900307123X 卡号 6222020200112233445"))
	}
}

// ruleSummary 超过 500 字时从最早的主题开始丢，开头标“…”；单个主题超长时硬截断。
func TestRuleSummaryTruncation(t *testing.T) {
	var turns []memTurn
	for i := 0; i < 30; i++ {
		turns = append(turns, memTurn{UserText: "x", Plan: Plan{Intent: IntentProductSearch, Query: fmt.Sprintf("q%02d %s", i, strings.Repeat("长", 30))}})
	}
	out := ruleSummary(turns, Plan{Intent: IntentCart}, "看看购物车")
	if n := utf8.RuneCountInString(out); n > maxSummaryRunes || !strings.HasPrefix(out, summaryPrefix+"…；") || strings.Contains(out, "q00") || !strings.HasSuffix(out, "购物车") {
		t.Fatalf("truncation (%d): %q", n, out)
	}
	one := ruleSummary(nil, Plan{Intent: IntentProductSearch, Query: strings.Repeat("超", 600)}, "")
	if n := utf8.RuneCountInString(one); n != maxSummaryRunes || !strings.HasSuffix(one, "…") {
		t.Fatalf("hard cut (%d)", n)
	}
}
