// Package mysqltest 为集成测试创建一次性的 MySQL 数据库。
//
// 需要设置有建库权限的 DSN，例如：
//
//	BLINK_TEST_MYSQL_DSN='root:blink_dev_root@tcp(127.0.0.1:3306)/'
//
// 未设置时本地跳过；CI 环境（CI 非空）下未设置直接失败，保证集成测试一定被执行。
package mysqltest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

const DSNEnv = "BLINK_TEST_MYSQL_DSN"

// FreshDSN 创建一个空数据库，返回指向它的 DSN；用例结束后自动删除。
func FreshDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s 未设置：CI 中必须运行 MySQL 集成测试", DSNEnv)
		}
		t.Skipf("未设置 %s，跳过 MySQL 集成测试", DSNEnv)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "blink_test_" + hex.EncodeToString(b[:])

	adminCfg := cfg.Clone()
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	// name 由本函数生成，只含字母数字和下划线。
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name)
		admin.Close()
	})
	cfg.DBName = name
	return cfg.FormatDSN()
}
