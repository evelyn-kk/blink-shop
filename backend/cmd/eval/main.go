// Command eval 运行知识检索（rag）和商品搜索（products）离线评测，报告写到 -out 目录（JSON + Markdown）。
//
//	go run ./cmd/eval                       # 纯关键词 / 规则，报告写到 ../quality/reports/eval-<时间>/
//	go run ./cmd/eval -vector=hash          # 本地演示：散列向量 + Milvus 临时集合（MILVUS_ADDR），跑完删除
//	go run ./cmd/eval -vector=env           # 用 MILVUS_ADDR + EMBEDDING_* 的真实向量（临时集合，跑完删除）
//
// 任何一个评测集通过率低于 -min-pass 时退出码为 1。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/eval"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/vector"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	suite := fs.String("suite", "all", "rag / products / all")
	data := fs.String("data", "../quality/data/eval", "JSONL 用例目录")
	corpus := fs.String("corpus", "fixtures/rag/corpus.json", "知识检索固定语料")
	outDir := fs.String("out", "", "报告目录，默认 ../quality/reports/eval-<时间>")
	version := fs.String("version", "dev", "写进报告的版本（如提交 SHA）")
	vec := fs.String("vector", "off", "off / hash / env：是否接 Milvus 向量检索")
	minPass := fs.Float64("min-pass", 1, "通过率下限")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join("..", "quality", "reports", "eval-"+time.Now().Format("20060102-150405"))
	}
	cfg := map[string]string{"vector": *vec}
	var knowledge rag.VectorIndex
	var products rag.ProductIndex
	if *vec != "off" {
		k, p, name, cleanup, err := openVectors(ctx, *vec, getenv)
		if err != nil {
			return err
		}
		defer cleanup()
		knowledge, products = k, p
		cfg["embedder"] = name
	}
	var reports []eval.Report
	if *suite == "all" || *suite == "rag" {
		r, err := eval.RunRAG(ctx, eval.RAGOptions{Cases: filepath.Join(*data, "rag.jsonl"), Corpus: *corpus, K: 3, Vector: knowledge, Version: *version, Config: cfg})
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if *suite == "all" || *suite == "products" {
		r, err := eval.RunProducts(ctx, eval.ProductOptions{Cases: filepath.Join(*data, "product_search.jsonl"), ProductIndex: products, Version: *version, Config: cfg})
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if len(reports) == 0 {
		return fmt.Errorf("未知的 -suite %q", *suite)
	}
	var low []string
	for _, r := range reports {
		if err := eval.WriteReport(*outDir, r); err != nil {
			return err
		}
		fmt.Fprintf(out, "%-8s %d/%d 通过（%.2f%%） 指标 %v 指纹 %s\n", r.Suite, r.Passed, r.Total, r.PassRate*100, r.Metrics, r.Fingerprint)
		if r.PassRate < *minPass {
			low = append(low, r.Suite)
		}
	}
	fmt.Fprintln(out, "报告：", *outDir)
	if len(low) > 0 {
		return fmt.Errorf("通过率低于 %.2f：%v", *minPass, low)
	}
	return nil
}

// openVectors 建两个临时集合（跑完删除），不碰正式集合。
func openVectors(ctx context.Context, mode string, getenv func(string) string) (rag.VectorIndex, rag.ProductIndex, string, func(), error) {
	resolver := configcenter.NewResolver(getenv, nil)
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return nil, nil, "", nil, err
	}
	m := vector.NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 30*time.Second)
	if m == nil {
		return nil, nil, "", nil, errors.New("需要 MILVUS_ADDR")
	}
	var e vector.Embedder
	switch mode {
	case "hash":
		e = vector.HashEmbedder{D: 128}
	case "env":
		oe := vector.NewOpenAIEmbedder(vector.EmbedOptions{BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel, Dim: cfg.EmbeddingDim})
		if oe == nil {
			return nil, nil, "", nil, errors.New("需要 EMBEDDING_API_KEY")
		}
		e = oe
	default:
		return nil, nil, "", nil, fmt.Errorf("未知的 -vector %q", mode)
	}
	suffix := fmt.Sprintf("%d_%d", time.Now().Unix(), rand.Intn(1e6))
	kname, pname := "blink_shop_eval_text_"+suffix, "blink_shop_eval_products_"+suffix
	k, p := vector.NewKnowledgeIndex(m, e, kname), vector.NewProductIndex(m, e, pname)
	cleanup := func() {
		_ = m.DropCollection(context.Background(), kname)
		_ = m.DropCollection(context.Background(), pname)
	}
	return k, &seedingProductIndex{ProductIndex: p}, e.Name(), cleanup, nil
}

// seedingProductIndex 在第一次检索前把种子商品写进临时集合（评测的 Store 是进程内的）。
type seedingProductIndex struct {
	*vector.ProductIndex
	seeded bool
}

func (s *seedingProductIndex) SearchProducts(ctx context.Context, text string, topN int) ([]rag.ProductHit, error) {
	if !s.seeded {
		if err := eval.SeedProducts(ctx, s.ProductIndex); err != nil {
			return nil, err
		}
		s.seeded = true
	}
	return s.ProductIndex.SearchProducts(ctx, text, topN)
}
