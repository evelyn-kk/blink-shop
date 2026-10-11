// Command migrate 是发布时执行数据库迁移的入口（生产环境 RUN_MIGRATIONS 默认关闭，API 不会自己迁移）：
//
//	go run ./cmd/migrate          # 只列出已应用 / 待执行的迁移，不改数据库
//	go run ./cmd/migrate -apply   # 执行待执行的迁移（MySQL 命名锁保证只有一个实例在迁移；已应用的迁移校验 checksum）
//
// 读取 MYSQL_DSN。数据库里有本程序不认识的更新版本时（新版本已升级过）：只列出时给出提示，-apply 拒绝执行。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "执行待执行的迁移（默认只列出）")
	timeout := fs.Duration("timeout", 10*time.Minute, "整个迁移的时限")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := configcenter.Load(ctx, configcenter.NewResolver(getenv, nil))
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
	st, err := mysqlstore.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := st.Ping(ctx); err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	applied, pending, unknown, err := st.MigrationPlan(ctx, migrations.FS)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "已应用 %d 个：%v\n待执行 %d 个：%v\n", len(applied), applied, len(pending), pending)
	if len(unknown) > 0 {
		fmt.Fprintf(out, "数据库里还有本程序不认识的版本 %v：数据库比程序新（例如回滚到了旧版本）。旧程序可以照常运行，但不能用它迁移。\n", unknown)
	}
	if !*apply {
		return nil
	}
	if len(unknown) > 0 {
		return errors.New("拒绝执行：数据库版本比程序新")
	}
	start := time.Now()
	done, err := st.Migrate(ctx, migrations.FS)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "本次执行 %d 个：%v（%s）\n", len(done), done, time.Since(start).Round(time.Millisecond))
	return nil
}
