package mysqlstore_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// freshDB 创建一个空数据库并返回指向它的 Store（未迁移）。需要 BLINK_TEST_MYSQL_DSN，见 mysqltest。
func freshDB(t *testing.T) *mysqlstore.Store {
	t.Helper()
	s, err := mysqlstore.Open(mysqltest.FreshDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func migrated(t *testing.T) *mysqlstore.Store {
	t.Helper()
	s := freshDB(t)
	if _, err := s.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return migrated(t) })
}

func TestMigrateEmptyThenAgain(t *testing.T) {
	ctx := context.Background()
	s := freshDB(t)

	applied, err := s.Migrate(ctx, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, []string{"0001_init.sql"}) {
		t.Fatalf("first run applied %v", applied)
	}
	applied, err = s.Migrate(ctx, migrations.FS)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second run applied %v, err %v; want nothing", applied, err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("schema_migrations rows = %d, %v", count, err)
	}
}

// expectedTables 与 docs/02 的表清单一致（加上文件、结算幂等和迁移记录表）。
var expectedTables = []string{
	"accounts", "agent_prompt_publish_records", "agent_prompts", "agent_runs", "agent_trace_events", "auth_tokens",
	"cart_items", "categories", "chat_sessions", "checkout_requests", "coupons", "knowledge_chunks", "knowledge_documents",
	"merchants", "order_items", "orders", "payments", "product_reviews", "product_skus", "products", "promotion_rules",
	"schema_migrations", "stored_files", "user_coupons", "user_messages",
}

// expectedUniqueKeys 是 docs/02 要求的唯一约束（表 -> 键名 -> 列）。
var expectedUniqueKeys = map[string]map[string]string{
	"accounts":            {"uk_accounts_username": "username"},
	"cart_items":          {"uk_cart_items_account_product_sku": "account_id,product_id,sku_id"},
	"user_messages":       {"uk_user_messages_client_message": "account_id,session_id,client_message_id"},
	"agent_runs":          {"uk_agent_runs_account_message": "account_id,message_id"},
	"knowledge_documents": {"uk_knowledge_documents_merchant_hash": "merchant_id,content_hash"},
	"knowledge_chunks":    {"uk_knowledge_chunks_document_index": "document_id,chunk_index"},
	"product_reviews":     {"uk_product_reviews_order_item": "order_item_id"},
	"agent_prompts":       {"uk_agent_prompts_key_version": "prompt_key,version"},
	"orders":              {"uk_orders_order_no": "order_no"},
	"product_skus":        {"uk_product_skus_product_sku": "product_id,sku_id"},
	"checkout_requests":   {"uk_checkout_requests_account_key": "account_id,idempotency_key"},
	"stored_files":        {"uk_stored_files_object_key": "object_key"},
}

func TestSchema(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	db := s.DB()

	// 表清单、引擎和字符集。
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME, ENGINE, TABLE_COLLATION FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name, engine, collation string
		if err := rows.Scan(&name, &engine, &collation); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
		if engine != "InnoDB" || collation != "utf8mb4_0900_ai_ci" {
			t.Errorf("%s: engine=%s collation=%s", name, engine, collation)
		}
	}
	rows.Close()
	if !reflect.DeepEqual(tables, expectedTables) {
		t.Fatalf("tables = %v\nwant %v", tables, expectedTables)
	}

	// 唯一键。
	rows, err = db.QueryContext(ctx, `SELECT TABLE_NAME, INDEX_NAME, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND NON_UNIQUE = 0 AND INDEX_NAME <> 'PRIMARY'
		GROUP BY TABLE_NAME, INDEX_NAME`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]string{}
	for rows.Next() {
		var table, index, cols string
		if err := rows.Scan(&table, &index, &cols); err != nil {
			t.Fatal(err)
		}
		if got[table] == nil {
			got[table] = map[string]string{}
		}
		got[table][index] = cols
	}
	rows.Close()
	if !reflect.DeepEqual(got, expectedUniqueKeys) {
		t.Fatalf("unique keys = %v\nwant %v", got, expectedUniqueKeys)
	}

	// 金额列一律 DECIMAL(10,2)，比例列 DECIMAL(5,4)，不得出现 FLOAT/DOUBLE。
	rows, err = db.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND (COLUMN_NAME LIKE '%amount' OR COLUMN_NAME LIKE '%price'
		OR COLUMN_NAME LIKE '%rate' OR DATA_TYPE IN ('float', 'double'))`)
	if err != nil {
		t.Fatal(err)
	}
	var moneyCols []string
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			t.Fatal(err)
		}
		want := "decimal(10,2)"
		if strings.HasSuffix(col, "rate") {
			want = "decimal(5,4)"
		}
		if typ != want {
			t.Errorf("%s.%s type = %s, want %s", table, col, typ, want)
		}
		moneyCols = append(moneyCols, table+"."+col)
	}
	rows.Close()
	sort.Strings(moneyCols)
	wantMoney := []string{
		"coupons.discount_amount", "coupons.threshold_amount", "order_items.price", "orders.discount_amount",
		"orders.pay_amount", "orders.total_amount", "payments.amount", "product_skus.price", "products.market_price",
		"products.price", "promotion_rules.discount_amount", "promotion_rules.discount_rate", "promotion_rules.threshold_amount",
	}
	if !reflect.DeepEqual(moneyCols, wantMoney) {
		t.Fatalf("money columns = %v\nwant %v", moneyCols, wantMoney)
	}

	// 软删字段。
	for _, tc := range [][2]string{{"accounts", "deleted_at"}, {"chat_sessions", "deleted_at"}} {
		var nullable string
		err := db.QueryRowContext(ctx, `SELECT IS_NULLABLE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, tc[0], tc[1]).Scan(&nullable)
		if err != nil || nullable != "YES" {
			t.Errorf("%s.%s: nullable=%q err=%v", tc[0], tc[1], nullable, err)
		}
	}
	// token 只存摘要。
	var tokenCols []string
	rows, _ = db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_tokens'`)
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		tokenCols = append(tokenCols, c)
	}
	rows.Close()
	sort.Strings(tokenCols)
	if !reflect.DeepEqual(tokenCols, []string{"account_id", "created_at", "expires_at", "token_hash"}) {
		t.Fatalf("auth_tokens columns = %v", tokenCols)
	}
}

func TestMigrateRejectsModifiedMigration(t *testing.T) {
	ctx := context.Background()
	s := freshDB(t)
	v1 := fstest.MapFS{"0001_init.sql": {Data: []byte("CREATE TABLE t1 (id INT PRIMARY KEY);")}}
	if _, err := s.Migrate(ctx, v1); err != nil {
		t.Fatal(err)
	}
	edited := fstest.MapFS{"0001_init.sql": {Data: []byte("CREATE TABLE t1 (id INT PRIMARY KEY, x INT);")}}
	if _, err := s.Migrate(ctx, edited); err == nil || !strings.Contains(err.Error(), "被修改") {
		t.Fatalf("err = %v, want modified-migration error", err)
	}
}

func TestMigrateRejectsUnknownAppliedVersion(t *testing.T) {
	ctx := context.Background()
	s := freshDB(t)
	files := fstest.MapFS{"0001_init.sql": {Data: []byte("CREATE TABLE t1 (id INT PRIMARY KEY);")}}
	if _, err := s.Migrate(ctx, files); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (2, '0002_future.sql', 'x', NOW())"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx, files); err == nil || !strings.Contains(err.Error(), "0002") {
		t.Fatalf("err = %v, want unknown version error", err)
	}
}

func TestMigrateStopsAtFailingStatement(t *testing.T) {
	ctx := context.Background()
	s := freshDB(t)
	files := fstest.MapFS{
		"0001_ok.sql":     {Data: []byte("CREATE TABLE t1 (id INT PRIMARY KEY);")},
		"0002_broken.sql": {Data: []byte("CREATE TABLE t2 (id INT PRIMARY KEY);\nCREATE TABLEE oops;")},
	}
	applied, err := s.Migrate(ctx, files)
	if err == nil || !strings.Contains(err.Error(), "0002_broken.sql") {
		t.Fatalf("err = %v, want failure in 0002", err)
	}
	if !reflect.DeepEqual(applied, []string{"0001_ok.sql"}) {
		t.Fatalf("applied = %v", applied)
	}
	var n int
	_ = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 2").Scan(&n)
	if n != 0 {
		t.Fatal("failed migration must not be recorded")
	}
	// 修复文件后可以继续（IF NOT EXISTS 风格的迁移可重跑）。
	files["0002_broken.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE IF NOT EXISTS t2 (id INT PRIMARY KEY);")}
	if applied, err := s.Migrate(ctx, files); err != nil || !reflect.DeepEqual(applied, []string{"0002_broken.sql"}) {
		t.Fatalf("rerun after fix: %v, %v", applied, err)
	}
}

func TestLoadMigrationsValidatesNames(t *testing.T) {
	bad := []fstest.MapFS{
		{"1_init.sql": {Data: []byte("SELECT 1;")}},
		{"0001_Init.sql": {Data: []byte("SELECT 1;")}},
		{"0001_a.sql": {Data: []byte("SELECT 1;")}, "0001_b.sql": {Data: []byte("SELECT 1;")}},
	}
	for i, files := range bad {
		if _, err := mysqlstore.LoadMigrations(files); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	// 非 .sql 文件被忽略。
	if got, err := mysqlstore.LoadMigrations(fstest.MapFS{"README.md": {}, "embed.go": {}, "0001_a.sql": {Data: []byte("SELECT 1;")}}); err != nil || len(got) != 1 {
		t.Fatalf("non-sql files should be ignored: %v, %v", got, err)
	}
	got, err := mysqlstore.LoadMigrations(migrations.FS)
	if err != nil || len(got) == 0 || got[0].Version != 1 {
		t.Fatalf("embedded migrations: %v, %v", got, err)
	}
}

func TestCorruptJSONColumnIsReported(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	if _, err := s.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE products SET tags_json = '{"not":"a list"}' WHERE product_id = 'p_seed_nova'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetProduct(ctx, "p_seed_nova")
	if err == nil || !strings.Contains(err.Error(), "tags_json") {
		t.Fatalf("err = %v, want JSON structure error for tags_json", err)
	}
}

func TestTimesStoredInUTC(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	acc, err := s.CreateAccount(ctx, store.NewAccount{Username: "tz", PasswordHash: "h", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var sessionTZ string
	var created string
	if err := s.DB().QueryRowContext(ctx, "SELECT @@session.time_zone, DATE_FORMAT(created_at, '%Y-%m-%dT%H:%i:%s.%f') FROM accounts WHERE account_id = ?",
		acc.AccountID).Scan(&sessionTZ, &created); err != nil {
		t.Fatal(err)
	}
	if sessionTZ != "+00:00" {
		t.Fatalf("session time_zone = %s", sessionTZ)
	}
	if want := acc.CreatedAt.Format("2006-01-02T15:04:05.000000"); created != want {
		t.Fatalf("stored created_at = %s, want UTC %s", created, want)
	}
}
