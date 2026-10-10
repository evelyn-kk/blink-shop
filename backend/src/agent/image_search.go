package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
)

// 图片找商品：用本轮附带的图片检索相似商品，文字里的品类 / 预算 / 品牌 / 排除词作为附加条件——
// 价格、品牌、排除词直接过滤；品类等检索词用来加分，图片很像但和文字条件对不上的只算“相近”，不当作推荐。

// imageTextBonus 是文字条件相关度（0–1）对排序的加分；只用来在相似度接近的候选里优先符合文字的。
const imageTextBonus = 0.1

// ImageMatch 是一件商品的图片相似度和等级。
type ImageMatch struct {
	ProductID string  `json:"product_id"`
	Score     float64 `json:"score"`
	// Level：match（同款 / 很像）、similar（外观相近）、text_mismatch（图片像但和文字条件对不上）。
	Level string `json:"level"`
}

// ImageSearchResult 是 search_image_products 的结果。Relevance 与商品搜索一致：只有 ok 能当作推荐。
type ImageSearchResult struct {
	FileID    string        `json:"file_id"`
	Query     string        `json:"query"`
	Terms     []string      `json:"terms"`
	Status    string        `json:"status"`
	Relevance string        `json:"relevance"`
	Products  []ProductCard `json:"products"`
	Matches   []ImageMatch  `json:"matches"`
	Total     int           `json:"total"`
	MinPrice  *domain.Money `json:"min_price,omitempty"`
	MaxPrice  *domain.Money `json:"max_price,omitempty"`
	Brands    []string      `json:"brands,omitempty"`
	Excluded  []string      `json:"excluded,omitempty"`
	Filtered  int           `json:"filtered"`
	// 以下写进轨迹，不给模型看。
	Embedder   string `json:"-"`
	Candidates int    `json:"-"`
	Dropped    int    `json:"-"`
}

func (r *Registry) imageTool() *Tool {
	return &Tool{Name: ToolSearchImage, Description: "用用户本轮上传的图片找相似的在售商品；file_id 必须是本轮消息附带的图片。可以带文字条件：query（品类等）用于加分，价格、品牌、排除词直接过滤。只有 relevance=ok 的结果能当作推荐。",
		Schema: object([]string{"file_id"}, map[string]*Schema{
			"file_id":   strLen("本轮消息附带的图片 file_id", 1, 80),
			"query":     strLen("文字条件（品类、用途等），可以为空", 0, 100),
			"limit":     integer("返回数量", 1, maxSearchLimit, defaultSearchLimit),
			"max_price": number("价格上限（元）", 0),
			"min_price": number("价格下限（元）", 0),
			"brands":    strList("只要这些品牌", 5, 30),
			"exclude":   strList("排除词", 10, 30),
		}),
		Run: func(ctx context.Context, tc *ToolContext, args map[string]any) (any, error) {
			return r.searchImage(ctx, tc, args)
		}}
}

type imageCandidate struct {
	card    ProductCard
	image   float64
	score   float64
	level   string
	keyword float64
}

func (r *Registry) searchImage(ctx context.Context, tc *ToolContext, args map[string]any) (ImageSearchResult, error) {
	query := argString(args, "query")
	out := ImageSearchResult{FileID: argString(args, "file_id"), Query: query, Terms: QueryTerms(query), Products: []ProductCard{}, Matches: []ImageMatch{},
		Relevance: RelevanceNone, Status: imagesearch.StatusNoMatch}
	if r.deps.ImageSearch == nil {
		return out, &ToolError{Code: CodeImageUnavailable, Message: "图片搜索暂不可用"}
	}
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
	res, err := r.deps.ImageSearch.SearchFile(ctx, tc.AccountID, out.FileID, imagesearch.MaxTopK)
	switch {
	case err == nil:
	case errors.Is(err, imagesearch.ErrFileNotFound), errors.Is(err, imagesearch.ErrNotImage):
		return out, &ToolError{Code: CodeImageNotAttached, Field: "file_id", Message: "找不到这张图片，请重新上传"}
	case errors.Is(err, imagesearch.ErrInvalidImage):
		return out, &ToolError{Code: CodeInvalidImage, Field: "file_id", Message: "图片无法识别"}
	case errors.Is(err, imagesearch.ErrUnavailable):
		return out, &ToolError{Code: CodeImageUnavailable, Message: "图片搜索暂不可用"}
	case errors.Is(err, imagesearch.ErrIndex), errors.Is(err, imagesearch.ErrStorage):
		r.deps.Logger.WarnContext(ctx, "image search unavailable", "run_id", tc.RunID, "error", err)
		return out, &ToolError{Code: CodeImageUnavailable, Message: "图片搜索暂不可用"}
	default:
		return out, err
	}
	out.Embedder, out.Candidates, out.Dropped = res.Embedder, res.Candidates, res.Dropped
	names, err := r.categoryNames(ctx)
	if err != nil {
		return out, err
	}
	scoreTerms := withoutBrandTerms(out.Terms, out.Brands)
	var cands []imageCandidate
	for _, m := range res.Items {
		c := imageCandidate{card: toProductCard(m.Product, names[m.Product.CategoryID]), image: m.Score, level: m.Level}
		text := c.card.text()
		switch {
		case out.MaxPrice != nil && c.card.Price > *out.MaxPrice,
			out.MinPrice != nil && c.card.Price < *out.MinPrice,
			len(out.Brands) > 0 && !brandMatches(c.card.Brand, out.Brands),
			excludedBy(text, out.Excluded):
			out.Filtered++
			continue
		}
		c.score = c.image
		if len(scoreTerms) > 0 {
			c.keyword, _ = relevance(scoreTerms, strings.ToLower(c.card.Name), text)
			c.score += imageTextBonus * math.Min(c.keyword, 1)
			if c.keyword <= 0 {
				c.level = "text_mismatch"
			}
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	for _, c := range cands {
		out.Matches = append(out.Matches, ImageMatch{ProductID: c.card.ProductID, Score: math.Round(c.image*10000) / 10000, Level: c.level})
	}
	var ok, weak []imageCandidate
	for _, c := range cands {
		if c.level == imagesearch.LevelMatch {
			ok = append(ok, c)
		} else {
			weak = append(weak, c)
		}
	}
	chosen := ok
	switch {
	case len(ok) > 0:
		out.Relevance, out.Status = RelevanceOK, imagesearch.StatusMatched
	case len(weak) > 0:
		out.Relevance, out.Status, chosen = RelevanceWeak, imagesearch.StatusSimilar, weak
	}
	out.Total = len(chosen)
	limit := argInt(args, "limit", defaultSearchLimit)
	for i, c := range chosen {
		if i >= limit {
			break
		}
		out.Products = append(out.Products, c.card)
	}
	return out, nil
}

// firstImage 返回本轮第一张图片附件的 file_id。
func firstImage(atts []domain.Attachment) string {
	for _, a := range atts {
		if strings.HasPrefix(a.MimeType, "image/") {
			return a.FileID
		}
	}
	return ""
}

// imagePlan 让附图优先走图搜：模型把带图的问题判成商品搜索 / 导购 / 对比时改回图搜（保留模型给的槽位）；
// 没有图却判成图搜时改为请用户上传。其他意图（加购、订单、导航等）照模型的判断。
func imagePlan(p Plan, rule Plan, hasImg bool) Plan {
	switch {
	case hasImg && (p.Intent == IntentProductSearch || p.Intent == IntentGuide || p.Intent == IntentProductCompare || p.Intent == IntentImageSearch):
		p.Intent, p.Action = IntentImageSearch, ""
		if rule.Intent == IntentImageSearch {
			p.Query = rule.Query
		}
	case !hasImg && p.Intent == IntentImageSearch:
		p.Action = "ask_upload"
	}
	return p
}

// ---------- 规则回答 ----------

func (s *session) handleImage(ctx context.Context) {
	fid := firstImage(s.in.Attachments)
	if fid == "" {
		if len(s.in.Attachments) > 0 {
			s.ask("你发的附件不是图片，没法按图找商品。请上传一张 JPG、PNG 或 WebP 格式的商品图片，或者直接用文字描述想找的商品。")
		} else {
			s.ask("想按图片找商品的话，请先上传一张商品图片：点输入框旁边的图片按钮，从相册选择或直接拍照。也可以用文字告诉我品类、品牌或颜色。")
		}
		s.followups("推荐一款降噪耳机", "3000 以内的手机", "看看我的购物车")
		return
	}
	spec := s.planSpec(ctx)
	args := map[string]any{"file_id": fid, "limit": defaultSearchLimit}
	if s.plan.Query != "" {
		args["query"] = s.plan.Query
	}
	if spec.max > 0 {
		args["max_price"] = float64(spec.max) / 100
	}
	if spec.min > 0 {
		args["min_price"] = float64(spec.min) / 100
	}
	if len(spec.brands) > 0 {
		args["brands"] = spec.brands
	}
	if len(spec.exclude) > 0 {
		args["exclude"] = spec.exclude
	}
	obs := s.call(ctx, "按图片找商品", ToolSearchImage, args)
	if !obs.OK {
		switch obs.Code {
		case CodeImageUnavailable:
			if s.plan.Query != "" {
				s.say("图片搜索暂时不可用，先按你的文字描述帮你找。")
				if res, ok := s.searchWith(ctx, s.plan.Query, spec, defaultSearchLimit); ok {
					s.presentProducts(res)
				}
				return
			}
			s.say("图片搜索暂时不可用，我现在没法识别图片里的商品。你可以用文字描述想找的商品（品类、品牌、颜色或用途），我按文字帮你找。")
			s.followups("推荐一款降噪耳机", "3000 以内的手机", "看看我的购物车")
		case CodeInvalidImage:
			s.ask("这张图片无法识别（可能已损坏、格式不支持或尺寸太小），请换一张清晰的商品图片再试。")
			s.followups("推荐一款降噪耳机", "3000 以内的手机", "看看我的购物车")
		default:
			s.say("按图片查找时出了点问题：" + obs.Message)
		}
		return
	}
	res := decode[ImageSearchResult](obs.Data)
	for _, p := range res.Products {
		s.tc.Evidence[p.ProductID] = true
	}
	s.presentImageResults(ctx, res, spec)
}

func (s *session) presentImageResults(ctx context.Context, res ImageSearchResult, spec searchSpec) {
	cond := ""
	if res.Query != "" {
		cond = "（并参考了你说的“" + res.Query + "”）"
	}
	switch {
	case len(res.Products) == 0 && res.Filtered > 0:
		s.sayf("和图片相似的在售商品有 %d 件，但都因为价格、品牌或排除条件没有列出。可以放宽条件再试。", res.Filtered)
		s.followups("放宽预算再推荐", "推荐一款降噪耳机", "看看我的购物车")
		return
	case len(res.Products) == 0:
		if res.Query != "" {
			s.sayf("没有找到和这张图片相似的在售商品，下面按文字“%s”帮你找。", res.Query)
			if r, ok := s.searchWith(ctx, res.Query, spec, defaultSearchLimit); ok {
				s.presentProducts(r)
			}
			return
		}
		s.say("没有找到和这张图片相似的在售商品。可以换一张更清晰、主体更完整的图片，或者用文字告诉我品类、品牌和预算。")
		s.followups("推荐一款降噪耳机", "3000 以内的手机", "有什么优惠活动")
		return
	case res.Relevance == RelevanceWeak:
		s.sayf("没有找到和图片一样的商品%s，下面是外观相近的在售商品，仅供参考：", cond)
		s.block(blockProducts("外观相近的商品", res.Products))
		s.followups("换一张图片再找", "推荐一款降噪耳机", "看看我的购物车")
		return
	}
	first := res.Products[0]
	intro := fmt.Sprintf("根据你上传的图片%s，最像的是 %s（%s）", cond, first.Name, yuan(first.Price))
	if first.RecommendReason != "" {
		intro += "：" + strings.TrimSuffix(first.RecommendReason, "。")
	}
	s.say(intro + "。")
	if len(first.RiskNotes) > 0 {
		s.say("注意：" + strings.Join(first.RiskNotes, "；") + "。")
	}
	if len(res.Products) > 1 {
		names := make([]string, 0, len(res.Products)-1)
		for _, p := range res.Products[1:] {
			names = append(names, fmt.Sprintf("%s（%s）", p.Name, yuan(p.Price)))
		}
		s.say("外观也比较接近的：" + strings.Join(names, "、") + "。")
	}
	s.say("图片相似只看外形和配色，价格、库存和规格以商品库为准，点卡片可以看详情。想加购的话告诉我“把第一个加入购物车”。")
	s.block(blockProducts("按图片找到的商品", res.Products))
	s.followups("把第一个加入购物车", first.Name+" 的评价怎么样", "有什么优惠券")
}
