package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// 商品搜索：关键词召回 + 向量召回（可选）→ 结构化过滤（价格区间、品牌、排除词）→ 规则打分（关键词相关度、向量相似度、
// 用途与“适合”匹配、品类命中、库存）→ 与“不适合 / 注意事项”冲突的降为不推荐 → 可选模型重排 → 截断。

const (
	vectorRecallTopN = 20
	rerankTopN       = 10
	vectorStrongSim  = 0.78
	conflictPenalty  = 4.0
)

type candidate struct {
	card     ProductCard
	keyword  float64
	strong   bool
	vector   float64
	score    float64
	conflict bool
	reasons  []string
	order    int
}

// searchProducts 执行一次商品搜索。
func (r *Registry) searchProducts(ctx context.Context, args map[string]any) (ProductSearchResult, error) {
	query := argString(args, "query")
	limit := argInt(args, "limit", defaultSearchLimit)
	out := ProductSearchResult{Query: query, Terms: QueryTerms(query), Products: []ProductCard{}, Relevance: RelevanceNone,
		Rerank: SearchRerank{Mode: "rule", Scores: []ScoredEntry{}}}
	if f, ok := argFloat(args, "max_price"); ok && f > 0 {
		m := domain.Money(f*100 + 0.5)
		out.MaxPrice = &m
	}
	if f, ok := argFloat(args, "min_price"); ok && f > 0 {
		m := domain.Money(f*100 + 0.5)
		out.MinPrice = &m
	}
	for _, b := range argStrings(args, "brands") {
		if b = strings.TrimSpace(b); b != "" && !containsStr(out.Brands, b) {
			out.Brands = append(out.Brands, b)
		}
	}
	for _, ex := range argStrings(args, "exclude") {
		if ex = strings.ToLower(ex); ex != "" {
			out.Excluded = append(out.Excluded, ex)
		}
	}
	names, err := r.categoryNames(ctx)
	if err != nil {
		return out, err
	}
	// 显式商品 ID：直接取这件商品（不存在或不可见就是没有结果）。
	if pid := argString(args, "product_id"); pid != "" {
		p, err := r.deps.Store.GetVisibleProduct(ctx, pid)
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out.Products, out.Total, out.Relevance = []ProductCard{toProductCard(p, names[p.CategoryID])}, 1, RelevanceOK
		return out, nil
	}

	// 1. 召回
	keyword := strings.Join(out.Terms, " ")
	if keyword == "" {
		keyword = query
	}
	items, _, err := r.deps.Store.SearchVisibleProducts(ctx, store.ProductSearch{Keyword: keyword, CategoryID: argString(args, "category_id"),
		Page: store.Page{Page: 1, PageSize: searchRecall}})
	if err != nil {
		return out, err
	}
	pool := map[string]*candidate{}
	var order []string
	for i, p := range items {
		pool[p.ProductID] = &candidate{card: toProductCard(p, names[p.CategoryID]), order: i}
		order = append(order, p.ProductID)
	}
	out.Recall.Keyword = len(items)
	if r.deps.ProductIndex != nil && strings.TrimSpace(query) != "" {
		hits, verr := r.deps.ProductIndex.SearchProducts(ctx, query, vectorRecallTopN)
		if verr != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Recall.VectorError = "向量检索暂不可用，已使用关键词检索"
			r.deps.Logger.WarnContext(ctx, "product vector search failed", "error", verr)
		}
		for _, h := range hits {
			if h.Score <= 0 {
				continue
			}
			c := pool[h.ProductID]
			if c == nil {
				// 向量结果只是候选：按可见性重新取回，下架、删除、风控的商品在这里被丢弃
				p, gerr := r.deps.Store.GetVisibleProduct(ctx, h.ProductID)
				if gerr != nil {
					continue
				}
				if cat := argString(args, "category_id"); cat != "" && p.CategoryID != cat {
					continue
				}
				c = &candidate{card: toProductCard(p, names[p.CategoryID]), order: len(order)}
				pool[h.ProductID] = c
				order = append(order, h.ProductID)
				out.Recall.Vector++
			}
			c.vector = math.Min(h.Score, 1)
		}
	}

	// 2. 过滤与打分。指定了品牌时，品牌词只用于过滤，不计入相关度（否则“Blink Home 的耳机”会因为品牌词命中而推荐台灯）。
	scoreTerms := withoutBrandTerms(out.Terms, out.Brands)
	var cands []*candidate
	for _, id := range order {
		c := pool[id]
		text := c.card.text()
		switch {
		case out.MaxPrice != nil && c.card.Price > *out.MaxPrice,
			out.MinPrice != nil && c.card.Price < *out.MinPrice,
			len(out.Brands) > 0 && !brandMatches(c.card.Brand, out.Brands),
			excludedBy(text, out.Excluded):
			out.Filtered++
			continue
		}
		c.keyword, c.strong = relevance(scoreTerms, strings.ToLower(c.card.Name), text)
		if c.keyword <= 0 && c.vector <= 0 {
			continue
		}
		scoreCandidate(c, scoreTerms)
		if c.conflict {
			out.Conflicts = append(out.Conflicts, c.card.Name)
		}
		cands = append(cands, c)
	}
	sortCandidates(cands)

	// 3. 可选模型重排（只能在规则候选里调整顺序，不能引入新商品）
	if st := r.settings(ctx); st.RerankEnabled && r.deps.LLM != nil && len(cands) > 1 {
		if reordered, rerr := r.modelRerank(ctx, st, query, cands); rerr != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Rerank.Error = errorClass(rerr)
		} else {
			cands = reordered
			out.Rerank.Mode = "model"
		}
	}

	// 4. 输出
	for i, c := range cands {
		if i < rerankTopN {
			out.Rerank.Scores = append(out.Rerank.Scores, ScoredEntry{ProductID: c.card.ProductID, Score: math.Round(c.score*100) / 100, Reasons: c.reasons})
		}
	}
	var ok, weak []*candidate
	for _, c := range cands {
		if !c.conflict && (c.strong || c.vector >= vectorStrongSim) {
			ok = append(ok, c)
		} else {
			weak = append(weak, c)
		}
	}
	chosen := ok
	switch {
	case len(ok) > 0:
		out.Relevance = RelevanceOK
	case len(weak) > 0:
		out.Relevance, chosen = RelevanceWeak, weak
	}
	out.Total = len(chosen)
	for i, c := range chosen {
		if i >= limit {
			break
		}
		out.Products = append(out.Products, c.card)
	}
	return out, nil
}

// scoreCandidate 规则打分：关键词相关度 + 向量相似度 + 用途命中“适合 / 标签 / 卖点” + 品类命中 − 冲突 − 缺货。
func scoreCandidate(c *candidate, terms []string) {
	c.score = c.keyword + 3*c.vector
	if c.keyword > 0 {
		c.reasons = append(c.reasons, fmt.Sprintf("keyword=%.2f", c.keyword))
	}
	if c.vector > 0 {
		c.reasons = append(c.reasons, fmt.Sprintf("vector=%.2f", c.vector))
	}
	suit := strings.ToLower(strings.Join(append(append(append([]string{}, c.card.SuitableFor...), c.card.Tags...), c.card.SellingPoints...), " "))
	avoid := strings.ToLower(strings.Join(append(append([]string{}, c.card.NotSuitableFor...), c.card.RiskNotes...), " "))
	category := strings.ToLower(c.card.CategoryName)
	for _, t := range terms {
		t = strings.ToLower(t)
		if utf8.RuneCountInString(t) < 2 {
			continue
		}
		if coverage(t, avoid) >= 0.5 && coverage(t, suit) < 1 {
			c.conflict = true
			c.score -= conflictPenalty
			c.reasons = append(c.reasons, "conflict:"+t)
			continue
		}
		if strings.Contains(suit, t) {
			c.score += 1.5
			c.reasons = append(c.reasons, "use:"+t)
		}
		if category != "" && (strings.Contains(category, t) || strings.Contains(t, category)) {
			c.score += 1
			c.reasons = append(c.reasons, "category:"+t)
		}
	}
	if c.card.StockStatus == domain.StockOutOfStock {
		c.score -= 1
		c.reasons = append(c.reasons, "out_of_stock")
	}
}

// coverage 是 term 的二元组（不足两字时整体）在 text 里出现的比例。
func coverage(term, text string) float64 {
	if text == "" {
		return 0
	}
	if strings.Contains(text, term) {
		return 1
	}
	grams := bigrams(term)
	if len(grams) == 0 {
		return 0
	}
	hit := 0
	for _, g := range grams {
		if strings.Contains(text, g) {
			hit++
		}
	}
	return float64(hit) / float64(len(grams))
}

func sortCandidates(cands []*candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.conflict != b.conflict {
			return !a.conflict
		}
		sa, sb := a.strong || a.vector >= vectorStrongSim, b.strong || b.vector >= vectorStrongSim
		if sa != sb {
			return sa
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if a.card.Price != b.card.Price {
			return a.card.Price < b.card.Price
		}
		return a.order < b.order
	})
}

func brandMatches(brand string, want []string) bool {
	b := strings.ToLower(strings.TrimSpace(brand))
	for _, w := range want {
		if strings.ToLower(strings.TrimSpace(w)) == b {
			return true
		}
	}
	return false
}

// ---------- 模型重排 ----------

const rerankPrompt = `你是商品重排器。根据用户需求给候选商品排序，只输出 JSON：{"ranking":["product_id", ...]}，按最符合需求到最不符合排列。
只能使用候选里出现的 product_id，不能新增；不确定的放在后面。不要输出解释。`

// modelRerank 让小模型调整候选顺序（最多前 10 个）。输出必须是候选 ID 的排列（可以只排一部分，没排到的按原顺序接在后面）。
func (r *Registry) modelRerank(ctx context.Context, st ModelSettings, query string, cands []*candidate) ([]*candidate, error) {
	head := cands
	if len(head) > rerankTopN {
		head = head[:rerankTopN]
	}
	type item struct {
		ID       string   `json:"product_id"`
		Name     string   `json:"name"`
		Price    string   `json:"price"`
		Points   []string `json:"selling_points,omitempty"`
		Suitable []string `json:"suitable_for,omitempty"`
		Avoid    []string `json:"not_suitable_for,omitempty"`
	}
	list := make([]item, 0, len(head))
	for _, c := range head {
		list = append(list, item{ID: c.card.ProductID, Name: c.card.Name, Price: c.card.Price.String(), Points: c.card.SellingPoints,
			Suitable: c.card.SuitableFor, Avoid: append(append([]string{}, c.card.NotSuitableFor...), c.card.RiskNotes...)})
	}
	payload, _ := json.Marshal(list)
	cctx, cancel := context.WithTimeout(ctx, st.Timeout)
	defer cancel()
	resp, err := r.deps.LLM.Complete(cctx, llm.Request{Model: st.PlannerModel, JSON: true, Temperature: 0, MaxTokens: 300,
		Messages: []llm.Message{{Role: "system", Content: rerankPrompt}, {Role: "user", Content: "用户需求：" + query + "\n候选：" + string(payload)}}})
	if err != nil {
		return nil, err
	}
	obj, err := parseJSONObject(resp.Content)
	if err != nil {
		return nil, err
	}
	ids := argStrings(obj, "ranking")
	byID := map[string]*candidate{}
	for _, c := range head {
		byID[c.card.ProductID] = c
	}
	var out []*candidate
	used := map[string]bool{}
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("rerank output invalid: unknown product %q", id)
		}
		if !used[id] {
			used[id] = true
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("rerank output invalid: empty ranking")
	}
	for _, c := range cands {
		if !used[c.card.ProductID] {
			out = append(out, c)
		}
	}
	// 冲突的商品无论模型怎么排都不能排到不冲突的前面
	sort.SliceStable(out, func(i, j int) bool { return !out[i].conflict && out[j].conflict })
	return out, nil
}

// settings 返回当前模型设置（没有来源时为默认）。
func (r *Registry) settings(ctx context.Context) ModelSettings {
	if r.deps.Settings == nil {
		return DefaultModelSettings().normalized()
	}
	return r.deps.Settings(ctx).normalized()
}

// ---------- 品牌词表 ----------

// catalogVocab 是在售商品的品牌，按长度从长到短（“Blink Home”先于“Blink”匹配）；缓存 1 分钟。
type catalogVocab struct {
	mu     sync.Mutex
	at     time.Time
	brands []string
}

func (r *Registry) brands(ctx context.Context) []string {
	r.vocab.mu.Lock()
	defer r.vocab.mu.Unlock()
	if r.vocab.brands != nil && r.deps.Now().Sub(r.vocab.at) < time.Minute {
		return r.vocab.brands
	}
	seen := map[string]bool{}
	var out []string
	for page := 1; page <= 10; page++ {
		items, total, err := r.deps.Store.SearchVisibleProducts(ctx, store.ProductSearch{Page: store.Page{Page: page, PageSize: 100}})
		if err != nil {
			return r.vocab.brands
		}
		for _, p := range items {
			if b := strings.TrimSpace(p.Brand); b != "" && !seen[strings.ToLower(b)] {
				seen[strings.ToLower(b)] = true
				out = append(out, b)
			}
		}
		if page*100 >= total {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return utf8.RuneCountInString(out[i]) > utf8.RuneCountInString(out[j]) })
	r.vocab.brands, r.vocab.at = out, r.deps.Now()
	return out
}

// MentionedBrands 找出句子里提到的在售品牌（排除词里的品牌不算）。较长的品牌名优先，命中后从句子里去掉，避免“Blink Home”再命中“Blink”。
func (r *Registry) MentionedBrands(ctx context.Context, text string, exclude []string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, b := range r.brands(ctx) {
		lb := strings.ToLower(b)
		if !strings.Contains(lower, lb) {
			continue
		}
		skip := false
		for _, ex := range exclude {
			if strings.Contains(strings.ToLower(ex), lb) || strings.Contains(lb, strings.ToLower(ex)) {
				skip = true
			}
		}
		lower = strings.ReplaceAll(lower, lb, " ")
		if !skip {
			out = append(out, b)
		}
	}
	return out
}

// withoutBrandTerms 去掉属于指定品牌名的检索词；全部去掉后没有剩下的（只说了品牌）时保留原词。
func withoutBrandTerms(terms, brands []string) []string {
	if len(brands) == 0 {
		return terms
	}
	var out []string
	for _, t := range terms {
		lt := strings.ToLower(t)
		inBrand := false
		for _, b := range brands {
			for _, part := range strings.Fields(strings.ToLower(b)) {
				if lt == part {
					inBrand = true
				}
			}
		}
		if !inBrand {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return terms
	}
	return out
}
