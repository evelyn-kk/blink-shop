// Command import-products 往 MySQL 写入生成的大目录，用于大数据量下的检索和压测：
//
//	MYSQL_DSN=... go run ./cmd/seed                                  # 先迁移并写入开发种子（商家和分类）
//	MYSQL_DSN=... go run ./cmd/import-products -generate 100000 -seed 42
//
// 商品按分类 × 品牌 × 系列 × 型号模板生成（seed.Catalog），同一个 -seed 结果完全相同；每批一条多行 INSERT。
// 生产环境（APP_ENV=production）拒绝运行。
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

	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "import-products:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("import-products", flag.ContinueOnError)
	n := fs.Int("generate", 0, "生成的商品件数")
	seedVal := fs.Uint64("seed", 42, "随机种子（相同种子生成相同数据）")
	batch := fs.Int("batch", 500, "每条 INSERT 的商品数")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *n <= 0 {
		return errors.New("需要 -generate N（N > 0）")
	}
	cfg, err := configcenter.Load(ctx, configcenter.NewResolver(getenv, nil))
	if err != nil {
		return fmt.Errorf("配置不合法: %w", err)
	}
	if cfg.IsProduction() {
		return errors.New("生产环境禁止写入生成的商品")
	}
	st, err := mysqlstore.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	cats, err := st.ListCategories(ctx)
	if err != nil {
		return fmt.Errorf("读取分类失败（先运行 go run ./cmd/seed）: %w", err)
	}
	have := map[string]bool{}
	for _, c := range cats {
		have[c.CategoryID] = true
	}
	for _, id := range []string{"c_phone", "c_audio", "c_mouse", "c_keyboard", "c_lamp"} {
		if !have[id] {
			return fmt.Errorf("缺少分类 %s：先运行 go run ./cmd/seed 写入开发种子", id)
		}
	}
	start := time.Now()
	products := seed.Catalog(*n, *seedVal)
	gen := time.Since(start)
	if err := st.BulkInsertProducts(ctx, products, *batch); err != nil {
		return err
	}
	skus := 0
	for _, p := range products {
		skus += len(p.SKUs)
	}
	fmt.Fprintf(out, "写入 %d 件商品、%d 个规格（seed=%d，生成 %s，写入 %s）\n", len(products), skus, *seedVal, gen.Round(time.Millisecond), (time.Since(start) - gen).Round(time.Millisecond))
	return nil
}
