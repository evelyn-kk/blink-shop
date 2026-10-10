// Command vectorindex 把已索引的知识文档和全部在售商品写入 Milvus 向量索引（可重复执行，按主键覆盖）。
// 生产环境不在 API 启动时自动建索引，换了 Embedding 模型或维度后也用它重建（-recreate 先删集合）。
//
//	MYSQL_DSN=... MILVUS_ADDR=127.0.0.1:19530 EMBEDDING_API_KEY=... go run ./cmd/vectorindex [-recreate]
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
	recreate := fs.Bool("recreate", false, "先删除两个集合再重建（换 Embedding 模型或维度后使用）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logger := logging.New(out, slog.LevelInfo)
	resolver := configcenter.NewResolver(getenv, nil)
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
	if !cfg.VectorEnabled() {
		return errors.New("需要配置 MILVUS_ADDR 和 EMBEDDING_API_KEY")
	}
	m := vector.NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 30*time.Second)
	e := vector.NewOpenAIEmbedder(vector.EmbedOptions{BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel, Dim: cfg.EmbeddingDim})
	if *recreate {
		for _, c := range []string{cfg.MilvusTextCollection, cfg.MilvusProductCollection} {
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
	stats, err := vector.Bootstrap(ctx, st, vector.NewKnowledgeIndex(m, e, cfg.MilvusTextCollection), vector.NewProductIndex(m, e, cfg.MilvusProductCollection), logger)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(stats)
	fmt.Fprintln(out, string(b))
	if stats.Failed > 0 {
		return fmt.Errorf("%d 项写入失败，见日志", stats.Failed)
	}
	return nil
}
