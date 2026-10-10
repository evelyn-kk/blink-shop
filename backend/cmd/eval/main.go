// Command eval 运行知识检索（rag）、商品搜索（products）和图片搜索（images）离线评测，报告写到 -out 目录（JSON + Markdown）。
//
//	go run ./cmd/eval                       # 纯关键词 / 规则，图片用本地特征 + 内存索引，报告写到 ../quality/reports/eval-<时间>/
//	go run ./cmd/eval -vector=hash          # 本地演示：散列向量 + Milvus 临时集合（MILVUS_ADDR），跑完删除
//	go run ./cmd/eval -vector=env           # 用 MILVUS_ADDR + EMBEDDING_* 的真实向量（临时集合，跑完删除）
//	go run ./cmd/eval -suite=images -image-index=milvus           # 商品图写进 Milvus 临时集合（跑完删除）
//	go run ./cmd/eval -suite=images -image-embedder=env           # 用 IMAGE_EMBEDDING_* 配置的图片 Embedder（如 dashscope）
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
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
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
	suite := fs.String("suite", "all", "rag / products / images / all")
	data := fs.String("data", "../quality/data/eval", "JSONL 用例目录")
	corpus := fs.String("corpus", "fixtures/rag/corpus.json", "知识检索固定语料")
	outDir := fs.String("out", "", "报告目录，默认 ../quality/reports/eval-<时间>")
	version := fs.String("version", "dev", "写进报告的版本（如提交 SHA）")
	vec := fs.String("vector", "off", "off / hash / env：是否接 Milvus 向量检索")
	imageIndex := fs.String("image-index", "memory", "memory / milvus：图片评测用的索引")
	imageEmbedder := fs.String("image-embedder", "local", "local / env：图片 Embedder（env 按 IMAGE_EMBEDDING_* 配置）")
	minPass := fs.Float64("min-pass", 1, "通过率下限（图片评测用 -image-min-pass）")
	imageMinPass := fs.Float64("image-min-pass", 0.98, "图片评测通过率下限（本地特征对外形相近的商品会混淆，不要求全对）")
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
	if *suite == "all" || *suite == "images" {
		opts, cleanup, err := imageOptions(ctx, *imageIndex, *imageEmbedder, getenv)
		if err != nil {
			return err
		}
		defer cleanup()
		opts.Cases, opts.Version = filepath.Join(*data, "image_search.jsonl"), *version
		r, err := eval.RunImages(ctx, opts)
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
		threshold := *minPass
		if r.Suite == "images" {
			threshold = *imageMinPass
		}
		if r.PassRate < threshold {
			low = append(low, r.Suite)
		}
	}
	fmt.Fprintln(out, "报告：", *outDir)
	if len(low) > 0 {
		return fmt.Errorf("通过率低于下限：%v", low)
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

// imageOptions 按参数选图片 Embedder 和索引；milvus 用临时集合（跑完删除），不碰正式集合。
func imageOptions(ctx context.Context, index, embedder string, getenv func(string) string) (eval.ImageOptions, func(), error) {
	o := eval.ImageOptions{Config: map[string]string{}}
	noop := func() {}
	if index == "memory" && embedder == "local" {
		return o, noop, nil
	}
	cfg, err := configcenter.Load(ctx, configcenter.NewResolver(getenv, nil))
	if err != nil {
		return o, nil, err
	}
	switch embedder {
	case "local":
		o.Embedder = imagevector.Local{}
	case "env":
		o.Embedder = vector.ImageEmbedder(cfg)
	default:
		return o, nil, fmt.Errorf("未知的 -image-embedder %q", embedder)
	}
	switch index {
	case "memory":
		return o, noop, nil
	case "milvus":
		m := vector.NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 30*time.Second)
		if m == nil {
			return o, nil, errors.New("需要 MILVUS_ADDR")
		}
		name := fmt.Sprintf("blink_shop_eval_images_%d_%d", time.Now().Unix(), rand.Intn(1e6))
		o.Index = vector.NewImageIndex(m, o.Embedder.Dim(), name)
		return o, func() { _ = m.DropCollection(context.Background(), name) }, nil
	}
	return o, nil, fmt.Errorf("未知的 -image-index %q", index)
}
