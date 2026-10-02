package rag

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const (
	DefaultTopK  = 5
	MaxTopK      = 20
	SnippetRunes = 160
	// MinScore 低于此分数的候选不返回：只命中一个两字片段、和问题关系很弱的分块会被过滤。
	MinScore = 0.2
	// 混合检索时关键词分与向量相似度的权重。
	keywordWeight = 0.65
	vectorWeight  = 0.35
	vectorTopN    = 50
)

// Query 是一次检索请求。过滤条件为空表示不限制；MerchantIDs 中的空串表示平台资料。
type Query struct {
	Text        string
	MerchantIDs []string
	ProductIDs  []string
	DocTypes    []string
	TopK        int
}

// Citation 是检索结果，可直接作为 Agent 回复中的引用（citation block）。
type Citation struct {
	ChunkID       string   `json:"chunk_id"`
	DocumentID    string   `json:"document_id"`
	MerchantID    string   `json:"merchant_id"`
	ProductID     string   `json:"product_id,omitempty"`
	Title         string   `json:"title"`
	DocumentTitle string   `json:"document_title"`
	Snippet       string   `json:"snippet"`
	Source        string   `json:"source"`
	SourceURL     string   `json:"source_url,omitempty"`
	Score         float64  `json:"score"`
	MatchedBy     []string `json:"matched_by"` // keyword / vector
}

// Result 中 Mode 为 hybrid（关键词 + 向量）或 keyword；VectorError 非空表示向量检索失败、已回退关键词。
type Result struct {
	Citations   []Citation
	Mode        string
	VectorError string
}

// VectorHit 是向量检索命中的分块和相似度（0–1，越大越相似）。
type VectorHit struct {
	ChunkID string
	Score   float64
}

// VectorFilter 与 Query 的过滤条件含义相同。
type VectorFilter struct {
	MerchantIDs []string
	ProductIDs  []string
	DocTypes    []string
}

// IndexedChunk 是写入向量索引的分块。
type IndexedChunk struct {
	ChunkID, DocumentID, MerchantID, ProductID, DocType, Title, Content string
}

// VectorIndex 是可替换的向量检索后端（Milvus 实现在 8.2 接入）。实现负责生成 embedding。
type VectorIndex interface {
	// Upsert 写入一篇文档的全部分块，先删除该文档已有的向量。
	Upsert(ctx context.Context, documentID string, chunks []IndexedChunk) error
	Search(ctx context.Context, text string, topN int, f VectorFilter) ([]VectorHit, error)
}

// Retriever 组合关键词召回和（可选的）向量召回，统一打分排序后输出引用。
type Retriever struct {
	store  store.Store
	vector VectorIndex
	logger *slog.Logger
}

// NewRetriever 创建检索器；vector 为 nil 表示只用关键词。
func NewRetriever(st store.Store, vector VectorIndex, logger *slog.Logger) *Retriever {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Retriever{store: st, vector: vector, logger: logger}
}

type candidate struct {
	hit       store.KnowledgeHit
	keyword   float64
	vector    float64
	matchedBy []string
}

// Search 检索知识分块：
//  1. 关键词召回：QueryTerms 拆出的检索词任一出现在分块标题、正文或文档标题中（Store 按命中词数优先返回前 200 个）；
//  2. 向量召回（配置了 VectorIndex 时）：取前 50 个相似分块，经 Store 按同样的可见性和过滤条件取回；
//  3. 打分：关键词分见 keywordScore；混合模式下 = 0.65×关键词分 + 0.35×向量相似度；
//  4. 过滤低于 MinScore 的候选，按分数降序、chunk_id 升序取前 TopK，摘要截取命中位置附近 160 字。
//
// 向量检索失败只记日志并回退关键词（Result.VectorError 说明原因），不影响结果返回；Store 出错才返回错误。
func (r *Retriever) Search(ctx context.Context, q Query) (Result, error) {
	topK := q.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	topK = min(topK, MaxTopK)
	terms := QueryTerms(q.Text)
	res := Result{Mode: "keyword", Citations: []Citation{}}
	if len(terms) == 0 {
		return res, nil
	}

	hits, err := r.store.SearchKnowledge(ctx, store.KnowledgeQuery{
		Terms: termTexts(terms), MerchantIDs: q.MerchantIDs, ProductIDs: q.ProductIDs, DocTypes: q.DocTypes,
	})
	if err != nil {
		return Result{}, err
	}
	cands := map[string]*candidate{}
	for _, h := range hits {
		cands[h.ChunkID] = &candidate{hit: h, matchedBy: []string{"keyword"}}
	}

	if r.vector != nil {
		if err := r.addVectorCandidates(ctx, q, terms, cands); err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			r.logger.WarnContext(ctx, "vector search failed, fallback to keyword", "error", err)
			res.VectorError = "向量检索暂不可用，已使用关键词检索"
		} else {
			res.Mode = "hybrid"
		}
	}

	idf := poolIDF(terms, cands)
	for _, c := range cands {
		c.keyword = keywordScore(q.Text, terms, idf, c.hit)
	}
	var ranked []Citation
	for _, c := range cands {
		score := c.keyword
		if res.Mode == "hybrid" {
			score = keywordWeight*c.keyword + vectorWeight*c.vector
		}
		if score < MinScore {
			continue
		}
		ranked = append(ranked, toCitation(c, score, terms))
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].ChunkID < ranked[j].ChunkID
	})
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	if ranked != nil {
		res.Citations = ranked
	}
	return res, nil
}

func (r *Retriever) addVectorCandidates(ctx context.Context, q Query, terms []Term, cands map[string]*candidate) error {
	vhits, err := r.vector.Search(ctx, q.Text, vectorTopN, VectorFilter{MerchantIDs: q.MerchantIDs, ProductIDs: q.ProductIDs, DocTypes: q.DocTypes})
	if err != nil {
		return err
	}
	var missing []string
	scores := map[string]float64{}
	for _, v := range vhits {
		if v.Score <= 0 {
			continue
		}
		scores[v.ChunkID] = min(v.Score, 1)
		if _, ok := cands[v.ChunkID]; !ok {
			missing = append(missing, v.ChunkID)
		}
	}
	if len(missing) > 0 {
		// 只取回 Store 认为当前可检索的分块：已删除、下架或被过滤掉的向量结果在这里被丢弃。
		extra, err := r.store.SearchKnowledge(ctx, store.KnowledgeQuery{
			ChunkIDs: missing, MerchantIDs: q.MerchantIDs, ProductIDs: q.ProductIDs, DocTypes: q.DocTypes,
		})
		if err != nil {
			return err
		}
		for _, h := range extra {
			cands[h.ChunkID] = &candidate{hit: h}
		}
	}
	for id, s := range scores {
		if c, ok := cands[id]; ok {
			c.vector = s
			c.matchedBy = append(c.matchedBy, "vector")
		}
	}
	return nil
}

// matchText 返回用于匹配的文本：分块正文与标题、所属文档标题（均为小写）。
func matchText(h store.KnowledgeHit) (body, title string) {
	return strings.ToLower(h.Title + "\n" + h.Content), strings.ToLower(h.Title + "\n" + h.DocumentTitle)
}

// absentTermFactor：在候选中都没出现的词多为跨词边界的无意义二元组（如“耳机降噪”中的“机降”），
// 但也可能是语料确实没覆盖的内容词。它们按 0.3 的折扣计入分母：既不让无意义片段拉低所有候选，
// 也不让只命中一个常见词的候选拿到满分。
const absentTermFactor = 0.3

// poolIDF 按候选集合计算每个检索词的区分度：出现过的词 idf = ln(1 + N/df)，所有候选都包含的词权重最低；
// 没出现过的词按 df = 1 计算再乘 absentTermFactor。
func poolIDF(terms []Term, cands map[string]*candidate) map[string]float64 {
	df := map[string]int{}
	for _, c := range cands {
		body, title := matchText(c.hit)
		for _, t := range terms {
			if strings.Contains(body, t.Text) || strings.Contains(title, t.Text) {
				df[t.Text]++
			}
		}
	}
	idf := map[string]float64{}
	n := float64(max(len(cands), 1))
	for _, t := range terms {
		if d := df[t.Text]; d > 0 {
			idf[t.Text] = math.Log(1 + n/float64(d))
		} else {
			idf[t.Text] = absentTermFactor * math.Log(1+n)
		}
	}
	return idf
}

// keywordScore 计算 0–1 的关键词相关度，每个检索词的权重 = 词权重 × 候选集中的区分度（见 poolIDF）：
//   - 覆盖度（0.7）：命中词的权重之和 / 全部检索词权重之和（命中指出现在分块正文、分块标题或文档标题）；
//   - 标题覆盖度（0.15）：分块标题或文档标题中命中的权重占比；
//   - 完整匹配（0.15）：分块正文或标题包含完整的查询文本（去掉首尾空白，至少 2 个字）。
func keywordScore(query string, terms []Term, idf map[string]float64, h store.KnowledgeHit) float64 {
	body, title := matchText(h)
	var total, matched, titleMatched float64
	for _, t := range terms {
		w := t.Weight * idf[t.Text]
		total += w
		inTitle := strings.Contains(title, t.Text)
		if inTitle || strings.Contains(body, t.Text) {
			matched += w
		}
		if inTitle {
			titleMatched += w
		}
	}
	if total == 0 {
		return 0
	}
	score := 0.7*matched/total + 0.15*titleMatched/total
	if q := strings.ToLower(strings.TrimSpace(query)); utf8.RuneCountInString(q) >= 2 && strings.Contains(body, q) {
		score += 0.15
	}
	return min(score, 1)
}

func toCitation(c *candidate, score float64, terms []Term) Citation {
	h := c.hit
	source := h.Source
	if source == "" {
		source = h.DocumentTitle
	}
	return Citation{
		ChunkID: h.ChunkID, DocumentID: h.DocumentID, MerchantID: h.MerchantID, ProductID: h.ProductID,
		Title: h.Title, DocumentTitle: h.DocumentTitle, Snippet: Snippet(h.Content, termTexts(terms), SnippetRunes),
		Source: source, SourceURL: h.SourceURL, Score: roundScore(score), MatchedBy: c.matchedBy,
	}
}

func roundScore(f float64) float64 { return float64(int(f*10000+0.5)) / 10000 }

// Snippet 截取正文中第一个命中检索词（优先长词）附近的 max 个字；没有命中时取开头。截断处加省略号。
func Snippet(content string, terms []string, max int) string {
	r := []rune(content)
	if len(r) <= max {
		return content
	}
	lower := []rune(strings.ToLower(content))
	pos := -1
	best := 0
	for _, t := range terms {
		tr := []rune(t)
		if len(tr) <= best {
			continue
		}
		if i := indexRunes(lower, tr); i >= 0 {
			pos, best = i, len(tr)
		}
	}
	start := 0
	if pos > max/4 {
		start = min(pos-max/4, len(r)-max)
	}
	out := string(r[start : start+max])
	if start > 0 {
		out = "…" + out
	}
	if start+max < len(r) {
		out += "…"
	}
	return out
}

func indexRunes(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
