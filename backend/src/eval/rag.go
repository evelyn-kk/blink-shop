package eval

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/rag"
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
	Cases   string // JSONL 用例
	Corpus  string // 固定语料 JSON
	K       int
	Vector  rag.VectorIndex
	Version string
	Config  map[string]string
}

// RunRAG 在“开发种子 + 固定语料”上跑检索用例，指标：recall@k、MRR、held-out 召回、空结果正确率。
// 每条 citation 的 chunk_id 都必须能在 Store 里按 ID 取回（可回查）。
func RunRAG(ctx context.Context, o RAGOptions) (Report, error) {
	start := time.Now()
	rep := Report{Suite: "rag", Version: o.Version, StartedAt: start.UTC(), Config: map[string]string{"k": strconv.Itoa(o.K), "mode": "keyword"}, Metrics: map[string]float64{}}
	for k, v := range o.Config {
		rep.Config[k] = v
	}
	if o.Vector != nil {
		rep.Config["mode"] = "hybrid"
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
	if o.Vector != nil {
		// 语料是用不带向量的入库流程写入的，评测前补写向量
		if err := indexCorpus(ctx, st, o.Vector); err != nil {
			return rep, err
		}
	}
	keyOf := map[string]string{}
	for k, id := range ids {
		keyOf[id] = k
	}
	r := rag.NewRetriever(st, o.Vector, nil)
	expected, found, heldExp, heldFound, noneCases, noneOK := 0, 0, 0, 0, 0, 0
	var rr float64
	for _, c := range cases {
		res, err := r.Search(ctx, rag.Query{Text: c.Query, MerchantIDs: c.MerchantIDs, DocTypes: c.DocTypes, TopK: o.K})
		if err != nil {
			return rep, err
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
		rep.Total++
		if len(problems) == 0 {
			rep.Passed++
		} else {
			rep.Failures = append(rep.Failures, Failure{ID: c.ID, Query: c.Query, Problems: problems, Got: got})
		}
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
	rep.finish(start)
	return rep, nil
}
