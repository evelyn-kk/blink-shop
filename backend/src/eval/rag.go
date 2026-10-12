package eval

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/vector"
)

// RAGCase 是一条检索用例：expect 中的文档（语料 key 或种子文档 ID）必须出现在前 k 条；absent 不能出现；expect_none 表示不应返回结果。
type RAGCase struct {
	ID          string   `json:"id"`
	Query       string   `json:"query"`
	Expect      []string `json:"expect"`
	Absent      []string `json:"absent"`
	ExpectNone  bool     `json:"expect_none"`
	MerchantIDs []string `json:"merchant_ids"`
	DocTypes    []string `json:"doc_types"`
	HeldOut     bool     `json:"held_out"`
}

// RAGOptions 是检索评测的输入。Vector 为 nil 表示纯关键词。
type RAGOptions struct {
	// MinPass 是门槛通过率，0 表示要求全部通过。
	MinPass float64
	Cases   string // JSONL 用例
	Corpus  string // 固定语料 JSON
	K       int
	Vector  rag.VectorIndex
	// Recall 是召回方式：keyword / vector / hybrid；空表示有 Vector 时 hybrid、否则 keyword。vector / hybrid 需要 Vector。
	Recall string
	// VectorWeight 是混合打分的向量权重，0 表示线上默认（0.35）。
	VectorWeight float64
	// Usage 返回 Embedding 累计用量（可为 nil）；报告记建索引和检索两段的调用数与 token。
	Usage   func() vector.EmbedUsage
	Version string
	Config  map[string]string
}

// recallMode 校验并补全召回方式。
func recallMode(recall string, hasVector bool) (string, error) {
	switch recall {
	case "":
		if hasVector {
			return rag.RecallHybrid, nil
		}
		return rag.RecallKeyword, nil
	case rag.RecallKeyword:
		return recall, nil
	case rag.RecallVector, rag.RecallHybrid:
		if !hasVector {
			return "", fmt.Errorf("召回方式 %s 需要向量索引（-vector）", recall)
		}
		return recall, nil
	}
	return "", fmt.Errorf("未知的召回方式 %q（keyword / vector / hybrid）", recall)
}

// writeUsage 把 Embedding 用量写进指标：prefix_calls / _requests / _texts / _tokens。
func writeUsage(m map[string]float64, prefix string, u vector.EmbedUsage) {
	m[prefix+"_calls"], m[prefix+"_requests"], m[prefix+"_texts"], m[prefix+"_tokens"] = float64(u.Calls), float64(u.Requests), float64(u.Texts), float64(u.Tokens)
}

// RunRAG 在“开发种子 + 固定语料”上跑检索用例，指标：recall@k、MRR、held-out 召回、空结果正确率。
// 每条 citation 的 chunk_id 都必须能在 Store 里按 ID 取回（可回查）。
func RunRAG(ctx context.Context, o RAGOptions) (Report, error) {
	start := time.Now()
	rep := Report{Suite: "rag", Version: o.Version, StartedAt: start.UTC(), Config: map[string]string{"k": strconv.Itoa(o.K), "mode": "keyword"}, Metrics: map[string]float64{}}
	for k, v := range o.Config {
		rep.Config[k] = v
	}
	mode, err := recallMode(o.Recall, o.Vector != nil)
	if err != nil {
		return rep, err
	}
	rep.Config["mode"] = mode
	if mode == rag.RecallHybrid && o.VectorWeight > 0 {
		rep.Config["vector_weight"] = strconv.FormatFloat(o.VectorWeight, 'f', -1, 64)
	}
	cases, err := readJSONL[RAGCase](o.Cases)
	if err != nil {
		return rep, err
	}
	st, err := newSeededStore(ctx)
	if err != nil {
		return rep, err
	}
	ids, err := loadCorpus(ctx, st, o.Corpus)
	if err != nil {
		return rep, err
	}
	usage := func() vector.EmbedUsage {
		if o.Usage == nil {
			return vector.EmbedUsage{}
		}
		return o.Usage()
	}
	u0 := usage()
	if o.Vector != nil && mode != rag.RecallKeyword {
		// 语料是用不带向量的入库流程写入的，评测前补写向量
		if err := indexCorpus(ctx, st, o.Vector); err != nil {
			return rep, err
		}
	}
	u1 := usage()
	keyOf := map[string]string{}
	for k, id := range ids {
		keyOf[id] = k
	}
	vec := o.Vector
	if mode == rag.RecallKeyword {
		vec = nil
	}
	r := rag.NewRetriever(st, vec, nil).WithScoring(rag.Scoring{Mode: mode, VectorWeight: o.VectorWeight})
	expected, found, heldExp, heldFound, noneCases, noneOK, vectorErrors := 0, 0, 0, 0, 0, 0, 0
	var rr float64
	var lat []time.Duration
	for _, c := range cases {
		t0 := time.Now()
		res, err := r.Search(ctx, rag.Query{Text: c.Query, MerchantIDs: c.MerchantIDs, DocTypes: c.DocTypes, TopK: o.K})
		if err != nil {
			return rep, err
		}
		took := time.Since(t0)
		lat = append(lat, took)
		if res.VectorError != "" {
			vectorErrors++
		}
		var got, chunkIDs []string
		for _, cit := range res.Citations {
			got = append(got, keyOf[cit.DocumentID])
			chunkIDs = append(chunkIDs, cit.ChunkID)
		}
		var problems []string
		if c.ExpectNone {
			noneCases++
			if len(got) == 0 {
				noneOK++
			} else {
				problems = append(problems, "应无结果")
			}
		}
		for _, want := range c.Expect {
			expected++
			if c.HeldOut {
				heldExp++
			}
			if i := slices.Index(got, want); i >= 0 {
				found++
				rr += 1 / float64(i+1)
				if c.HeldOut {
					heldFound++
				}
			} else {
				problems = append(problems, fmt.Sprintf("%s 不在前 %d", want, o.K))
			}
		}
		for _, bad := range c.Absent {
			if slices.Contains(got, bad) {
				problems = append(problems, bad+" 不应出现")
			}
		}
		if len(chunkIDs) > 0 {
			back, err := st.SearchKnowledge(ctx, storeChunkQuery(chunkIDs))
			if err != nil {
				return rep, err
			}
			if len(back) != len(chunkIDs) {
				problems = append(problems, "citation 的 chunk_id 不能全部回查")
			}
		}
		if res.VectorError != "" {
			problems = append(problems, "向量检索失败："+res.VectorError)
		}
		var scores []float64
		for _, cit := range res.Citations {
			scores = append(scores, cit.Score)
		}
		rep.recordDetail(ragGroup(c), Failure{ID: c.ID, Query: c.Query, Problems: problems, Got: got},
			map[string]any{"search_ms": float64(took.Microseconds()) / 1000, "got": got, "scores": scores})
	}
	rep.Metrics["search_p50_ms"] = float64(percentile(lat, 0.50).Microseconds()) / 1000
	rep.Metrics["search_p95_ms"] = float64(percentile(lat, 0.95).Microseconds()) / 1000
	if o.Usage != nil {
		writeUsage(rep.Metrics, "embed_index", u1.Sub(u0))
		writeUsage(rep.Metrics, "embed_query", usage().Sub(u1))
	}
	if mode != rag.RecallKeyword {
		rep.Metrics["vector_errors"] = float64(vectorErrors)
	}
	if expected > 0 {
		rep.Metrics["recall_at_k"] = round(float64(found) / float64(expected))
		rep.Metrics["mrr"] = round(rr / float64(expected))
	}
	if heldExp > 0 {
		rep.Metrics["held_out_recall_at_k"] = round(float64(heldFound) / float64(heldExp))
	}
	if noneCases > 0 {
		rep.Metrics["no_result_accuracy"] = round(float64(noneOK) / float64(noneCases))
	}
	rep.finish(start, o.MinPass)
	return rep, nil
}

// ragGroup：留出集 / 无结果 / 普通召回。
func ragGroup(c RAGCase) string {
	switch {
	case c.HeldOut:
		return "held_out"
	case c.ExpectNone:
		return "no_result"
	}
	return "recall"
}
