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
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/httpapi"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
)

const shutdownTimeout = 10 * time.Second

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
	resolver := configcenter.NewResolver(getenv, configcenter.NewMemorySource(nil))
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
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

	server := httpapi.NewServer(httpapi.Options{
		Logger:    logger,
		Settings:  configcenter.NewHTTPSettingsProvider(resolver, cfg.IsProduction()),
		Store:     st,
		AvatarDir: cfg.AvatarUploadDir,
		Readiness: []httpapi.ReadinessCheck{
			{Name: "mysql", Check: func(ctx context.Context) error { return schemaReady(ctx, st) }},
		},
	})

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

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("优雅关闭失败: %w", err)
	}
	logger.Info("api shutdown completed")
	return nil
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
