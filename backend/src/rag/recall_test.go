package rag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/ingest"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/memstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

type corpusDoc struct {
	Key        string `json:"key"`
	MerchantID string `json:"merchant_id"`
	ProductID  string `json:"product_id"`
	DocType    string `json:"doc_type"`
	SourceType string `json:"source_type"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	HTML       string `json:"html"`
	JSONText   string `json:"json_text"`
}

type recallCase struct {
	Query       string   `json:"query"`
	Expect      []string `json:"expect"`
	Absent      []string `json:"absent"`
	ExpectNone  bool     `json:"expect_none"`
	MerchantIDs []string `json:"merchant_ids"`
	DocTypes    []string `json:"doc_types"`
	Citation    *struct {
		DocumentKey     string  `json:"document_key"`
		Title           string  `json:"title"`
		ProductID       string  `json:"product_id"`
		MerchantID      *string `json:"merchant_id"`
		Source          string  `json:"source"`
		SnippetContains string  `json:"snippet_contains"`
	} `json:"citation"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// loadCorpus 用真实入库流程写入语料，返回 key → document_id（种子文档的 key 就是其 ID）。
func loadCorpus(t *testing.T, st store.Store) map[string]string {
	t.Helper()
	ctx := context.Background()
	if _, err := st.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Documents []corpusDoc `json:"documents"`
	}
	readJSON(t, "../../fixtures/rag/corpus.json", &corpus)
	svc := ingest.NewService(st, nil, nil, nil, nil)
	ids := map[string]string{"doc_seed_nova": "doc_seed_nova", "doc_seed_after_sales": "doc_seed_after_sales", "doc_seed_mouse": "doc_seed_mouse"}
	for _, d := range corpus.Documents {
		res, err := svc.Ingest(ctx, ingest.Request{
			MerchantID: d.MerchantID, ProductID: d.ProductID, DocType: d.DocType,
			Source: ingest.Source{Title: d.Title, SourceType: d.SourceType, Content: d.Content, HTML: d.HTML, JSONText: d.JSONText},
		})
		if err != nil {
			t.Fatalf("ingest %s: %v", d.Key, err)
		}
		ids[d.Key] = res.Document.DocumentID
	}
	return ids
}

func TestRecallOnFixedCorpus(t *testing.T) {
	t.Run("memstore", func(t *testing.T) { runRecall(t, memstore.New()) })
	t.Run("mysql", func(t *testing.T) {
		st, err := mysqlstore.Open(mysqltest.FreshDSN(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if _, err := st.Migrate(context.Background(), migrations.FS); err != nil {
			t.Fatal(err)
		}
		runRecall(t, st)
	})
}

func runRecall(t *testing.T, st store.Store) {
	ids := loadCorpus(t, st)
	keyOf := map[string]string{}
	for k, id := range ids {
		keyOf[id] = k
	}
	var spec struct {
		K         int          `json:"k"`
		MinRecall float64      `json:"min_recall"`
		Queries   []recallCase `json:"queries"`
	}
	readJSON(t, "../../fixtures/rag/queries.json", &spec)
	r := rag.NewRetriever(st, nil, nil)

	expected, found := 0, 0
	var rr float64
	for _, c := range spec.Queries {
		res, err := r.Search(context.Background(), rag.Query{Text: c.Query, MerchantIDs: c.MerchantIDs, DocTypes: c.DocTypes, TopK: spec.K})
		if err != nil {
			t.Fatal(err)
		}
		if res.Mode != "keyword" {
			t.Fatalf("mode = %s", res.Mode)
		}
		var got []string
		for _, cit := range res.Citations {
			got = append(got, keyOf[cit.DocumentID])
		}
		if c.ExpectNone && len(got) != 0 {
			t.Errorf("%q: want no results, got %v", c.Query, got)
		}
		for _, want := range c.Expect {
			expected++
			if i := slices.Index(got, want); i >= 0 {
				found++
				rr += 1 / float64(i+1)
			} else {
				t.Errorf("%q: %s not in top %d: %v", c.Query, want, spec.K, describe(res.Citations, keyOf))
			}
		}
		for _, bad := range c.Absent {
			if slices.Contains(got, bad) {
				t.Errorf("%q: %s should not be returned: %v", c.Query, bad, got)
			}
		}
		if c.Citation != nil && len(res.Citations) > 0 {
			top := res.Citations[0]
			want := c.Citation
			problems := []string{}
			if keyOf[top.DocumentID] != want.DocumentKey {
				problems = append(problems, "document "+keyOf[top.DocumentID])
			}
			if want.Title != "" && top.Title != want.Title {
				problems = append(problems, "title "+top.Title)
			}
			if want.ProductID != "" && top.ProductID != want.ProductID {
				problems = append(problems, "product "+top.ProductID)
			}
			if want.MerchantID != nil && top.MerchantID != *want.MerchantID {
				problems = append(problems, "merchant "+top.MerchantID)
			}
			if want.Source != "" && top.Source != want.Source {
				problems = append(problems, "source "+top.Source)
			}
			if !strings.Contains(top.Snippet, want.SnippetContains) {
				problems = append(problems, "snippet "+top.Snippet)
			}
			if top.ChunkID == "" || top.DocumentTitle == "" || top.Score <= 0 || !slices.Equal(top.MatchedBy, []string{"keyword"}) {
				problems = append(problems, fmt.Sprintf("fields %+v", top))
			}
			if len(problems) > 0 {
				t.Errorf("%q: top citation mismatch: %v", c.Query, problems)
			}
		}
	}
	recall := float64(found) / float64(expected)
	t.Logf("recall@%d = %.3f (%d/%d), MRR = %.3f", spec.K, recall, found, expected, rr/float64(expected))
	if recall < spec.MinRecall {
		t.Fatalf("recall@%d = %.3f < %.3f", spec.K, recall, spec.MinRecall)
	}
}

func describe(cs []rag.Citation, keyOf map[string]string) []string {
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s(%.3f %s)", keyOf[c.DocumentID], c.Score, c.Title))
	}
	return out
}
