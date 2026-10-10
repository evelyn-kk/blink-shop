// Command vectorindex 把已索引的知识文档、全部在售商品和商品图写入 Milvus 向量索引（可重复执行，按主键覆盖）。
// 生产环境不在 API 启动时自动建索引，换了 Embedding 模型或维度后也用它重建（-recreate 先删集合）。
// 知识 / 商品文本需要 EMBEDDING_API_KEY；商品图只需要 MILVUS_ADDR（默认本地图片特征）。-only 只建其中一部分。
//
//	MYSQL_DSN=... MILVUS_ADDR=127.0.0.1:19530 EMBEDDING_API_KEY=... go run ./cmd/vectorindex [-recreate] [-only text|images]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/imagesearch"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
	"github.com/evelyn-kk/blink-shop/backend/src/vector"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "vectorindex:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("vectorindex", flag.ContinueOnError)
	recreate := fs.Bool("recreate", false, "先删除集合再重建（换 Embedding 模型或维度后使用）")
	only := fs.String("only", "", "只建 text（知识 + 商品文本）或 images（商品图）；为空表示全部已配置的")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *only != "" && *only != "text" && *only != "images" {
		return fmt.Errorf("-only 只能是 text 或 images")
	}
	logger := logging.New(out, slog.LevelInfo)
	resolver := configcenter.NewResolver(getenv, nil)
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
	doText := cfg.VectorEnabled() && *only != "images"
	doImages := cfg.ImageSearchEnabled() && *only != "text"
	switch {
	case *only == "text" && !cfg.VectorEnabled():
		return errors.New("需要配置 MILVUS_ADDR 和 EMBEDDING_API_KEY")
	case !doText && !doImages:
		return errors.New("需要配置 MILVUS_ADDR（商品图）和 EMBEDDING_API_KEY（知识与商品文本）")
	}
	m := vector.NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 30*time.Second)
	if *recreate {
		var drop []string
		if doText {
			drop = append(drop, cfg.MilvusTextCollection, cfg.MilvusProductCollection)
		}
		if doImages {
			drop = append(drop, cfg.MilvusImageCollection)
		}
		for _, c := range drop {
			if err := m.DropCollection(ctx, c); err != nil {
				return fmt.Errorf("删除集合 %s 失败: %w", c, err)
			}
			logger.Info("collection dropped", "collection", c)
		}
	}
	st, err := mysqlstore.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	type result struct {
		Text   *vector.Stats           `json:"text,omitempty"`
		Images *imagesearch.IndexStats `json:"images,omitempty"`
	}
	var res result
	failed := 0
	if doText {
		e := vector.NewOpenAIEmbedder(vector.EmbedOptions{BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel, Dim: cfg.EmbeddingDim})
		stats, err := vector.Bootstrap(ctx, st, vector.NewKnowledgeIndex(m, e, cfg.MilvusTextCollection), vector.NewProductIndex(m, e, cfg.MilvusProductCollection), logger)
		if err != nil {
			return err
		}
		res.Text, failed = &stats, failed+stats.Failed
	}
	if doImages {
		// 建索引不需要读用户上传的文件，不接对象存储
		svc := vector.OpenImageSearch(cfg, st, nil, logger)
		stats, err := svc.Bootstrap(ctx)
		if err != nil {
			return err
		}
		res.Images, failed = &stats, failed+stats.Failed
	}
	b, _ := json.Marshal(res)
	fmt.Fprintln(out, string(b))
	if failed > 0 {
		return fmt.Errorf("%d 项写入失败，见日志", failed)
	}
	return nil
}
