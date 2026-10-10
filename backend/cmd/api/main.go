// Command api 启动 Blink Shop HTTP 服务；这里只负责组装依赖和生命周期。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/agent"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/httpapi"
	"github.com/evelyn-kk/blink-shop/backend/src/llm"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/objectstore"
	"github.com/evelyn-kk/blink-shop/backend/src/rag"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
	"github.com/evelyn-kk/blink-shop/backend/src/vector"
)

const (
	shutdownTimeout = 10 * time.Second
	// agentStopTimeout 是关闭时等待进行中的导购运行写完最终状态的时间。
	agentStopTimeout = 5 * time.Second
	// orderCloseInterval 是检查超时未支付订单的间隔。
	orderCloseInterval = time.Minute
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// run 读取配置、校验、组装依赖并阻塞到 ctx 结束；配置不合法时在监听端口前返回错误。
func run(ctx context.Context, getenv func(string) string, logOut io.Writer) error {
	logger := logging.New(logOut, slog.LevelInfo)
	// MySQL 驱动默认直接往 stderr 写纯文本，这里改走统一的 JSON 脱敏日志。
	_ = mysql.SetLogger(mysqlDriverLogger{logger})

	// 动态配置暂时只有内存实现；Nacos 在 9.1 接入，不可用时同样退回这里。
	dynamic := configcenter.NewMemorySource(nil)
	resolver := configcenter.NewResolver(getenv, dynamic)
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
	agentSettings, _ := configcenter.AgentSettingsOf(ctx, resolver) // Load 已校验
	if err := configcenter.ValidateProduction(ctx, cfg, resolver); err != nil {
		return fmt.Errorf("生产环境配置不安全: %w", err)
	}
	logger.Info("config loaded",
		"app_env", cfg.AppEnv,
		"api_addr", cfg.APIAddr,
		"mysql_dsn", cfg.MySQLDSN, // 键名含 dsn，日志中会被整体掩码
		"run_migrations", cfg.RunMigrations,
		"bootstrap_vector_index", cfg.BootstrapVectorIndex,
	)

	startedAt := time.Now()
	st, err := mysqlstore.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	// MySQL 暂时不可用不阻止启动：/health 仍然 200，/ready 返回 503，恢复后自动变为就绪。
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if err := st.Ping(pingCtx); err != nil {
		logger.Warn("mysql unavailable at startup", "error", err)
	}
	cancel()
	if cfg.RunMigrations {
		go migrateUntilDone(ctx, st, logger)
	}

	objects, err := openObjectStore(cfg, logger)
	if err != nil {
		return err
	}
	// 模型：没有 AI_API_KEY 时为 nil，导购只走规则。超时和重试取启动时的配置（运行中改动对已建客户端不生效）。
	var provider llm.Provider
	if client := llm.NewClient(llm.Options{BaseURL: cfg.AIBaseURL, APIKey: cfg.AIAPIKey, Timeout: agentSettings.Timeout, MaxRetries: agentSettings.MaxRetries}); client != nil {
		provider = client
		logger.Info("llm configured", "base_url", cfg.AIBaseURL, "planner_model", agentSettings.PlannerModel, "agent_model", agentSettings.AgentModel)
	} else {
		logger.Warn("AI_API_KEY not set, agent runs on rules only")
	}

	// 向量检索：Milvus 和 Embedding 都配置了才启用；否则知识和商品都只用关键词检索。
	vec := openVectorIndexes(cfg, logger)
	// 图片搜索：配置了 Milvus 就启用（默认本地图片特征，不调外部服务）；商品图只读平台内图片和白名单域名。
	images := vector.OpenImageSearch(cfg, st, objects, logger)
	if images != nil {
		logger.Info("image search configured", "embedder", images.EmbedderName(), "collection", cfg.MilvusImageCollection,
			"fetch_allowed_hosts", len(cfg.ImageFetchAllowedHosts))
	} else {
		logger.Warn("image search not configured (MILVUS_ADDR), /search/image returns 503")
	}

	server := httpapi.NewServer(httpapi.Options{
		VectorIndex:  vec.knowledgeOpt(),
		ProductIndex: vec.productOpt(),
		ImageSearch:  images,
		Logger:       logger,
		Settings:     configcenter.NewHTTPSettingsProvider(resolver, cfg.IsProduction()),
		Store:        st,
		ObjectStore:  objects,
		AvatarDir:    cfg.AvatarUploadDir,
		Configs:      configcenter.NewAdmin(resolver, dynamic),
		// 导购风险词和模型设置跟随动态配置（管理后台可改，立即生效）。
		RiskWords: func(ctx context.Context) []string { return configcenter.RiskBlockedWords(ctx, resolver) },
		LLM:       provider,
		AgentSettings: func(ctx context.Context) agent.ModelSettings {
			return httpapi.ModelSettingsFrom(configcenter.AgentSettingsNow(ctx, resolver))
		},
		Readiness: []httpapi.ReadinessCheck{
			{Name: "mysql", Check: func(ctx context.Context) error { return schemaReady(ctx, st) }},
		},
	})

	// 关闭超过支付期限的订单（回补库存、退券）；支付接口也会在发现超时时顺带关闭。
	go server.RunOrderCloser(ctx, orderCloseInterval)
	// 上一个进程没跑完的导购运行标为失败（数据库就绪后执行一次；本进程启动后开始的运行不受影响）。
	go recoverRunsWhenReady(ctx, server, st, startedAt, logger)
	// 非生产默认在启动时把已有知识和商品写入向量索引（BOOTSTRAP_VECTOR_INDEX）；生产用 cmd/vectorindex 显式执行。
	if cfg.BootstrapVectorIndex && (vec.enabled() || images != nil) {
		go bootstrapVectorsWhenReady(ctx, st, vec, server, logger)
	}

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	listener, err := net.Listen("tcp", cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", cfg.APIAddr, err)
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", listener.Addr().String())
		serveErr <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("api 异常退出: %w", err)
	case <-ctx.Done():
	}

	// 先停掉进行中的导购运行，让它们写完最终状态（failed: server_shutdown），再关闭 HTTP 服务。
	server.StopAgentRuns(agentStopTimeout)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("优雅关闭失败: %w", err)
	}
	logger.Info("api shutdown completed")
	return nil
}

// openObjectStore 按配置创建对象存储；未配置 MINIO_ENDPOINT 时返回 nil，服务照常启动，文件接口返回 object_storage_unavailable。
// MinIO 暂时连不上不影响启动，第一次上传时再建桶。
func openObjectStore(cfg configcenter.Config, logger *slog.Logger) (objectstore.Store, error) {
	if cfg.MinIOEndpoint == "" {
		logger.Warn("object storage not configured, file upload disabled")
		return nil, nil
	}
	minioStore, err := objectstore.NewMinIO(objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey,
		Bucket: cfg.MinIOBucket, UseSSL: cfg.MinIOUseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("对象存储配置不合法: %w", err)
	}
	logger.Info("object storage configured", "endpoint", cfg.MinIOEndpoint, "bucket", cfg.MinIOBucket, "use_ssl", cfg.MinIOUseSSL)
	return minioStore, nil
}

type mysqlDriverLogger struct{ logger *slog.Logger }

func (l mysqlDriverLogger) Print(v ...any) {
	l.logger.Warn("mysql driver", "detail", fmt.Sprint(v...))
}

const migrateRetryInterval = 5 * time.Second

// migrateUntilDone 在后台执行迁移；数据库暂不可用时每隔几秒重试，直到成功或进程退出。
// 迁移完成前 /ready 一直返回 503，因此不会有流量打到未迁移的库上。
func migrateUntilDone(ctx context.Context, st *mysqlstore.Store, logger *slog.Logger) {
	for {
		applied, err := st.Migrate(ctx, migrations.FS)
		if err == nil {
			logger.Info("database migrated", "applied", applied)
			return
		}
		logger.Error("database migration failed, will retry", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(migrateRetryInterval):
		}
	}
}

// schemaReady 是 /ready 的数据库检查：能连上，且没有未执行的迁移。
// recoverRunsWhenReady 等数据库迁移完成后把中断的运行标为失败；失败时稍后重试，直到成功或服务退出。
func recoverRunsWhenReady(ctx context.Context, server *httpapi.Server, st *mysqlstore.Store, startedAt time.Time, logger *slog.Logger) {
	for {
		if schemaReady(ctx, st) == nil {
			_, err := server.RecoverInterruptedRuns(ctx, startedAt)
			if err == nil {
				return
			}
			logger.Error("recover interrupted agent runs failed, will retry", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(migrateRetryInterval):
		}
	}
}

func schemaReady(ctx context.Context, st *mysqlstore.Store) error {
	if err := st.Ping(ctx); err != nil {
		return err
	}
	pending, err := st.PendingMigrations(ctx, migrations.FS)
	if err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("还有 %d 个数据库迁移未执行", pending)
	}
	return nil
}

// vectorIndexes 是启动时创建的向量索引；未配置时两个都是 nil。
type vectorIndexes struct {
	knowledge *vector.KnowledgeIndex
	products  *vector.ProductIndex
}

func (v vectorIndexes) enabled() bool { return v.knowledge != nil && v.products != nil }

// knowledgeOpt / productOpt 返回接口值：nil 指针不能直接赋给接口（会得到非 nil 接口）。
func (v vectorIndexes) knowledgeOpt() rag.VectorIndex {
	if v.knowledge == nil {
		return nil
	}
	return v.knowledge
}

func (v vectorIndexes) productOpt() rag.ProductIndex {
	if v.products == nil {
		return nil
	}
	return v.products
}

func openVectorIndexes(cfg configcenter.Config, logger *slog.Logger) vectorIndexes {
	if !cfg.VectorEnabled() {
		logger.Warn("vector search not configured (MILVUS_ADDR / EMBEDDING_API_KEY), using keyword retrieval only")
		return vectorIndexes{}
	}
	m := vector.NewMilvus(cfg.MilvusAddr, cfg.MilvusToken, 10*time.Second)
	e := vector.NewOpenAIEmbedder(vector.EmbedOptions{BaseURL: cfg.EmbeddingBaseURL, APIKey: cfg.EmbeddingAPIKey, Model: cfg.EmbeddingModel, Dim: cfg.EmbeddingDim})
	if m == nil || e == nil {
		logger.Warn("vector search not configured, using keyword retrieval only")
		return vectorIndexes{}
	}
	logger.Info("vector search configured", "milvus_addr", cfg.MilvusAddr, "embedding_model", cfg.EmbeddingModel, "embedding_dim", cfg.EmbeddingDim,
		"text_collection", cfg.MilvusTextCollection, "product_collection", cfg.MilvusProductCollection)
	return vectorIndexes{knowledge: vector.NewKnowledgeIndex(m, e, cfg.MilvusTextCollection), products: vector.NewProductIndex(m, e, cfg.MilvusProductCollection)}
}

// bootstrapVectorsWhenReady 等数据库迁移完成后建一次全量向量索引；失败只记日志（关键词检索照常可用）。
// 知识分块直接写；商品文本向量和商品图向量走 Server 的按商品串行同步，不会和同时发生的增量同步互相覆盖。
func bootstrapVectorsWhenReady(ctx context.Context, st *mysqlstore.Store, vec vectorIndexes, server *httpapi.Server, logger *slog.Logger) {
	for schemaReady(ctx, st) != nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(migrateRetryInterval):
		}
	}
	if vec.enabled() {
		stats, err := vector.Bootstrap(ctx, st, vec.knowledge, nil, logger)
		if err != nil {
			logger.Error("knowledge vector bootstrap failed", "error", err)
		} else {
			logger.Info("knowledge vector bootstrap done", "documents", stats.Documents, "chunks", stats.Chunks, "failed", stats.Failed)
		}
	}
	n, err := server.ResyncAllProducts(ctx)
	if err != nil {
		logger.Error("product vector bootstrap failed", "error", err)
		return
	}
	logger.Info("product vector bootstrap done", "products", n)
}
