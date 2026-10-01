// Command api 启动 Blink Shop HTTP 服务；这里只负责组装依赖和生命周期。
package main

import (
	"context"
	"database/sql"
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

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/httpapi"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
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

	db, err := openMySQL(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	// MySQL 暂时不可用不阻止启动：/health 仍然 200，/ready 返回 503，恢复后自动变为就绪。
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		logger.Warn("mysql unavailable at startup", "error", err)
	}
	cancel()

	server := httpapi.NewServer(httpapi.Options{
		Logger:   logger,
		Settings: configcenter.NewHTTPSettingsProvider(resolver, cfg.IsProduction()),
		Readiness: []httpapi.ReadinessCheck{
			{Name: "mysql", Check: db.PingContext},
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

func openMySQL(dsn string) (*sql.DB, error) {
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("MYSQL_DSN 格式不正确")
	}
	connector, err := mysql.NewConnector(parsed)
	if err != nil {
		return nil, errors.New("MYSQL_DSN 配置不可用")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}
