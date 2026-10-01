// Command seed 写入本地开发演示数据：先执行迁移，再按固定 ID “不存在才插入”，可重复执行。
// 生产环境（APP_ENV=production）拒绝运行，避免写入演示凭据。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdout, bcryptHash); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func bcryptHash(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func run(ctx context.Context, getenv func(string) string, out io.Writer, hash seed.HashFunc) error {
	resolver := configcenter.NewResolver(getenv, nil)
	cfg, err := configcenter.Load(ctx, resolver)
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
	if cfg.IsProduction() {
		return errors.New("生产环境禁止写入演示数据")
	}

	st, err := mysqlstore.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := st.Ping(pingCtx); err != nil {
		return fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	applied, err := st.Migrate(ctx, migrations.FS)
	if err != nil {
		return fmt.Errorf("迁移失败: %w", err)
	}
	fmt.Fprintf(out, "迁移：本次执行 %d 个 %v\n", len(applied), applied)

	data, err := seed.Dev(hash)
	if err != nil {
		return err
	}
	result, err := st.ApplySeed(ctx, data)
	if err != nil {
		return fmt.Errorf("写入种子失败（已整体回滚）: %w", err)
	}

	fmt.Fprintf(out, "种子：本次新增 %d 行（已存在的记录不会被覆盖）\n", result.Total())
	tables := make([]string, 0, len(result))
	for t := range result {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, t := range tables {
		fmt.Fprintf(w, "  %s\t%d\n", t, result[t])
	}
	w.Flush()

	fmt.Fprintf(out, "\n演示账号（初始密码均为 %s，仅限本地开发）：\n", seed.DevPassword)
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, a := range data.Accounts {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", a.Username, a.Role, a.DisplayName)
	}
	return w.Flush()
}
