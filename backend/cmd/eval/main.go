// Command eval 运行意图（intent）、知识检索（rag）、商品搜索（products）、图片搜索（images）和端到端导购（agent）离线评测，报告写到 -out 目录（JSON + Markdown）。
//
//	go run ./cmd/eval                       # 纯关键词 / 规则，图片用本地特征 + 内存索引，报告写到 ../quality/reports/eval-<时间>/
//	go run ./cmd/eval -vector=hash          # 本地演示：散列向量 + Milvus 临时集合（MILVUS_ADDR），跑完删除
//	go run ./cmd/eval -vector=env           # 用 MILVUS_ADDR + EMBEDDING_* 的真实向量（临时集合，跑完删除）
//	go run ./cmd/eval -suite=rag -vector=env -recall=vector       # 召回方式对照：keyword / vector / hybrid
//	go run ./cmd/eval -suite=rag -vector=env -rag-cases=../quality/data/eval/rag_paraphrase_holdout.jsonl
//	go run ./cmd/eval -suite=images -image-index=milvus           # 商品图写进 Milvus 临时集合（跑完删除）
//	go run ./cmd/eval -suite=images -image-embedder=env           # 用 IMAGE_EMBEDDING_* 配置的图片 Embedder（如 dashscope）
//	go run ./cmd/eval -suite=agent -llm=env                       # 用 AI_* 配置的真实模型跑规划和工具循环（会产生费用）
//	go run ./cmd/eval -suite=intent -llm=env -react=false          # 只开模型规划
//	go run ./cmd/eval -suite=agent -agent-cases=../quality/data/eval/agent_adversarial.jsonl -llm=env
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

	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/eval"
	"github.com/evelyn-kk/blink-shop/backend/src/imagevector"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
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
	suite := fs.String("suite", "all", "intent / rag / products / images / agent / all")
	data := fs.String("data", "../quality/data/eval", "JSONL 用例目录")
	corpus := fs.String("corpus", "fixtures/rag/corpus.json", "知识检索固定语料")
	outDir := fs.String("out", "", "报告目录，默认 ../quality/reports/eval-<时间>")
	version := fs.String("version", "dev", "写进报告的版本（如提交 SHA）")
	vec := fs.String("vector", "off", "off / hash / env：是否接 Milvus 向量检索")
	imageIndex := fs.String("image-index", "memory", "memory / milvus：图片评测用的索引")
	imageEmbedder := fs.String("image-embedder", "local", "local / env：图片 Embedder（env 按 IMAGE_EMBEDDING_* 配置）")
	minPass := fs.Float64("min-pass", 1, "通过率下限（图片评测用 -image-min-pass）")
	intentMinPass := fs.Float64("intent-min-pass", eval.DefaultIntentMinPass, "意图评测通过率下限（留出集允许少量没覆盖的说法）")
	imageMinPass := fs.Float64("image-min-pass", eval.DefaultImageMinPass, "图片评测通过率下限（本地特征对外形相近的商品会混淆，不要求全对）")
	llmMode := fs.String("llm", "off", "off / env：env 按 AI_BASE_URL、AI_API_KEY 创建模型客户端（与线上同一运行器，会产生费用）")
	planner := fs.Bool("planner", true, "-llm=env 时是否用模型规划")
	react := fs.Bool("react", true, "-llm=env 时是否用模型做工具循环和最终回答")
	rerank := fs.Bool("rerank", false, "-llm=env 时是否用模型重排商品")
	summary := fs.Bool("summary", false, "-llm=env 时是否用模型生成会话摘要")
	plannerModel := fs.String("planner-model", "", "规划 / 重排 / 摘要用的模型，默认 AI_PLANNER_MODEL 或 deepseek-chat")
	agentModel := fs.String("agent-model", "", "工具循环用的模型，默认 AI_AGENT_MODEL 或 deepseek-chat")
	llmTimeout := fs.Duration("llm-timeout", 30*time.Second, "单次模型调用上限")
	llmRetries := fs.Int("llm-retries", 2, "429 / 5xx / 网络错误的重试次数")
	maxRounds := fs.Int("max-tool-rounds", 6, "工具循环最多轮数")
	intentCases := fs.String("intent-cases", "", "意图用例文件，默认 <data>/intent.jsonl")
	productCases := fs.String("product-cases", "", "商品搜索用例文件，默认 <data>/product_search.jsonl")
	agentCases := fs.String("agent-cases", "", "端到端用例文件，默认 <data>/agent.jsonl")
	ragCases := fs.String("rag-cases", "", "知识检索用例文件，默认 <data>/rag.jsonl")
	recall := fs.String("recall", "", "keyword / vector / hybrid：知识检索和商品搜索的召回方式；空表示接了向量时 hybrid、否则 keyword")
	vectorWeight := fs.Float64("vector-weight", 0, "知识检索混合打分的向量权重（0 表示线上默认 0.35），只用于敏感性对照")
	strongSim := fs.Float64("vector-strong-sim", 0, "商品向量相似度达到多少可直接推荐（0 表示线上默认 0.78），只用于敏感性对照")
	imageMatch := fs.Float64("image-match", 0, "覆盖图片 Embedder 的同款阈值（0 表示用 Embedder 自己的），只用于标定")
	imageWeak := fs.Float64("image-weak", 0, "覆盖图片 Embedder 的相近阈值（0 表示用 Embedder 自己的），只用于标定")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join("..", "quality", "reports", "eval-"+time.Now().Format("20060102-150405"))
	}
	cfg := map[string]string{"vector": *vec}
	var knowledge rag.VectorIndex
	var products *seedingProductIndex
	var usage func() vector.EmbedUsage
	if *vec != "off" {
		k, p, e, cleanup, err := openVectors(ctx, *vec, getenv)
		if err != nil {
			return err
		}
		defer cleanup()
		knowledge, products = k, p
		cfg["embedder"] = e.Name()
		if oe, ok := e.(*vector.OpenAIEmbedder); ok {
			usage = oe.Usage
		}
	}
	model, err := modelConfig(ctx, *llmMode, getenv, agent.ModelSettings{PlannerEnabled: *planner, AgentEnabled: *react, RerankEnabled: *rerank,
		SummaryEnabled: *summary, PlannerModel: *plannerModel, AgentModel: *agentModel, Timeout: *llmTimeout, MaxToolRounds: *maxRounds}, *llmRetries)
	if err != nil {
		return err
	}
	pick := func(override, name string) string {
		if override != "" {
			return override
		}
		return filepath.Join(*data, name)
	}
	var reports []eval.Report
	if *suite == "all" || *suite == "intent" {
		r, err := eval.RunIntent(ctx, eval.IntentOptions{Cases: pick(*intentCases, "intent.jsonl"), Version: *version, Config: cfg, MinPass: *intentMinPass, Model: model})
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if *suite == "all" || *suite == "rag" {
		r, err := eval.RunRAG(ctx, eval.RAGOptions{Cases: pick(*ragCases, "rag.jsonl"), Corpus: *corpus, K: 3, Vector: knowledge, Recall: *recall,
			VectorWeight: *vectorWeight, Usage: usage, Version: *version, Config: cfg, MinPass: *minPass})
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if *suite == "all" || *suite == "products" {
		po := eval.ProductOptions{Cases: pick(*productCases, "product_search.jsonl"), Recall: *recall, VectorStrongSim: *strongSim,
			Version: *version, Config: cfg, MinPass: *minPass, Model: model}
		if products != nil {
			// 先把种子商品写进临时集合，报告里的 Embedding 用量只算检索
			if err := products.seed(ctx); err != nil {
				return err
			}
			po.ProductIndex, po.Usage = products, usage
		}
		r, err := eval.RunProducts(ctx, po)
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
		if *imageMatch > 0 || *imageWeak > 0 {
			if opts.Embedder == nil {
				opts.Embedder = imagevector.Local{}
			}
			opts.Embedder = imagevector.WithThresholds(opts.Embedder, *imageMatch, *imageWeak)
		}
		defer cleanup()
		opts.Cases, opts.Version, opts.MinPass = filepath.Join(*data, "image_search.jsonl"), *version, *imageMinPass
		r, err := eval.RunImages(ctx, opts)
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if *suite == "all" || *suite == "agent" {
		ao := eval.AgentOptions{Cases: pick(*agentCases, "agent.jsonl"), Version: *version, Config: cfg, MinPass: *minPass, Model: model}
		if products != nil {
			ao.ProductIndex, ao.Vector = products, knowledge
		}
		r, err := eval.RunAgent(ctx, ao)
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
		fmt.Fprintf(out, "%-8s %-6s %d/%d 通过（%.2f%%，门槛 %.2f%%） 指标 %v 指纹 %s\n", r.Suite, r.Gate, r.Passed, r.Total, r.PassRate*100, r.MinPassRate*100, r.Metrics, r.Fingerprint)
		if r.Gate != eval.GatePassed {
			low = append(low, r.Suite)
		}
	}
	fmt.Fprintln(out, "报告：", *outDir)
	if len(low) > 0 {
		return fmt.Errorf("未过门槛：%v", low)
	}
	return nil
}

// openVectors 建两个临时集合（跑完删除），不碰正式集合。
func openVectors(ctx context.Context, mode string, getenv func(string) string) (rag.VectorIndex, *seedingProductIndex, vector.Embedder, func(), error) {
	resolver := configcenter.NewResolver(getenv, nil)
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	m := vector.NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 30*time.Second)
	if m == nil {
		return nil, nil, nil, nil, errors.New("需要 MILVUS_ADDR")
	}
	var e vector.Embedder
	switch mode {
	case "hash":
		e = vector.HashEmbedder{D: 128}
	case "env":
		oe := vector.NewOpenAIEmbedder(vector.EmbedOptions{BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel, Dim: cfg.EmbeddingDim})
		if oe == nil {
			return nil, nil, nil, nil, errors.New("需要 EMBEDDING_API_KEY")
		}
		e = oe
	default:
		return nil, nil, nil, nil, fmt.Errorf("未知的 -vector %q", mode)
	}
	suffix := fmt.Sprintf("%d_%d", time.Now().Unix(), rand.Intn(1e6))
	kname, pname := "blink_shop_eval_text_"+suffix, "blink_shop_eval_products_"+suffix
	k, p := vector.NewKnowledgeIndex(m, e, kname), vector.NewProductIndex(m, e, pname)
	cleanup := func() {
		_ = m.DropCollection(context.Background(), kname)
		_ = m.DropCollection(context.Background(), pname)
	}
	return k, &seedingProductIndex{ProductIndex: p}, e, cleanup, nil
}

// seedingProductIndex 在第一次检索前把种子商品写进临时集合（评测的 Store 是进程内的）。
type seedingProductIndex struct {
	*vector.ProductIndex
	seeded bool
}

func (s *seedingProductIndex) seed(ctx context.Context) error {
	if s.seeded {
		return nil
	}
	if err := eval.SeedProducts(ctx, s.ProductIndex); err != nil {
		return err
	}
	s.seeded = true
	return nil
}

func (s *seedingProductIndex) SearchProducts(ctx context.Context, text string, topN int) ([]rag.ProductHit, error) {
	if err := s.seed(ctx); err != nil {
		return nil, err
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

// modelConfig 按 -llm 创建模型客户端；off 返回 nil（纯规则）。不加熔断：评测要看到每一次失败。
func modelConfig(ctx context.Context, mode string, getenv func(string) string, st agent.ModelSettings, retries int) (*eval.ModelConfig, error) {
	switch mode {
	case "off":
		return nil, nil
	case "env":
	default:
		return nil, fmt.Errorf("未知的 -llm %q", mode)
	}
	cfg, err := configcenter.Load(ctx, configcenter.NewResolver(getenv, nil))
	if err != nil {
		return nil, err
	}
	client := llm.NewClient(llm.Options{BaseURL: cfg.AIBaseURL, APIKey: cfg.AIAPIKey, Timeout: st.Timeout, MaxRetries: retries})
	if client == nil {
		return nil, errors.New("-llm=env 需要 AI_API_KEY")
	}
	if st.PlannerModel == "" {
		st.PlannerModel = firstNonEmpty(getenv("AI_PLANNER_MODEL"), "deepseek-chat")
	}
	if st.AgentModel == "" {
		st.AgentModel = firstNonEmpty(getenv("AI_AGENT_MODEL"), "deepseek-chat")
	}
	return &eval.ModelConfig{LLM: client, Settings: st}, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
