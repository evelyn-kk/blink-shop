package mysqlstore_test

import (
	"context"
	"errors"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
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
	if !reflect.DeepEqual(applied, []string{"0001_init.sql", "0002_promotion_discount_rate_check.sql",
		"0003_knowledge_document_product.sql", "0004_backfill_knowledge_document_product.sql", "0005_admin_audit_logs.sql"}) {
		t.Fatalf("first run applied %v", applied)
	}
	applied, err = s.Migrate(ctx, migrations.FS)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second run applied %v, err %v; want nothing", applied, err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != 5 {
		t.Fatalf("schema_migrations rows = %d, %v", count, err)
	}
}

// expectedTables 与 docs/02 的表清单一致（加上文件、结算幂等、管理审计和迁移记录表）。
var expectedTables = []string{
	"accounts", "admin_audit_logs", "agent_prompt_publish_records", "agent_prompts", "agent_runs", "agent_trace_events", "auth_tokens",
	"cart_items", "categories", "chat_sessions", "checkout_requests", "coupons", "knowledge_chunks", "knowledge_documents",
	"merchants", "order_items", "orders", "payments", "product_reviews", "product_skus", "products", "promotion_rules",
	"schema_migrations", "stored_files", "user_coupons", "user_messages",
}

// expectedUniqueKeys 是 docs/02 要求的唯一约束（表 -> 键名 -> 列）。
var expectedUniqueKeys = map[string]map[string]string{
	"accounts":            {"uk_accounts_username": "username"},
	"admin_audit_logs":    {"uk_admin_audit_seq": "seq"},
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

// TestDiscountRateRange 覆盖 REV-003：折扣率在 MySQL 的写入、读出和约束三层都限定在 [0, 1]。
func TestDiscountRateRange(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	if _, err := s.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	readRate := func() (domain.Rate, error) {
		var r domain.Rate
		err := db.QueryRowContext(ctx, "SELECT discount_rate FROM promotion_rules WHERE promotion_id = 'promo_seed_earbuds'").Scan(&r)
		return r, err
	}

	// 往返：0.95 写入后读回仍是 0.9500。
	if r, err := readRate(); err != nil || r.String() != "0.9500" {
		t.Fatalf("round trip = %s, %v", r, err)
	}

	// 约束存在。
	var clause string
	if err := db.QueryRowContext(ctx, `SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'chk_promotion_rules_discount_rate'`).Scan(&clause); err != nil {
		t.Fatalf("check constraint missing: %v", err)
	}

	// 绕过应用的越界写入被数据库拒绝（错误 3819），原值不变；边界值 0 和 1 允许。
	for _, bad := range []string{"1.5", "1.0001", "-0.1", "9.9999"} {
		_, err := db.ExecContext(ctx, "UPDATE promotion_rules SET discount_rate = ? WHERE promotion_id = 'promo_seed_earbuds'", bad)
		var myErr *mysql.MySQLError
		if !errors.As(err, &myErr) || myErr.Number != 3819 {
			t.Errorf("UPDATE discount_rate = %s: err = %v, want check constraint violation 3819", bad, err)
		}
	}
	if r, _ := readRate(); r.String() != "0.9500" {
		t.Fatalf("value changed after rejected updates: %s", r)
	}
	for _, edge := range []string{"0", "1"} {
		if _, err := db.ExecContext(ctx, "UPDATE promotion_rules SET discount_rate = ? WHERE promotion_id = 'promo_seed_earbuds'", edge); err != nil {
			t.Errorf("UPDATE discount_rate = %s should succeed: %v", edge, err)
		}
	}

	// 通过 Store 写入越界值：在 Value()/ValidatePromotion 处被拒绝，不会到达数据库。
	promo := storetest.DevSeed(t).Promotions[2]
	promo.PromotionID, promo.DiscountRate = "promo_store_bad", 15000
	if _, err := s.ApplySeed(ctx, store.SeedData{Promotions: []domain.PromotionRule{promo}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("ApplySeed with rate 1.5: err = %v, want ErrInvalid", err)
	}

	// 读出防线独立生效：即使约束被删掉、库里出现 1.5，读取也会报错而不是得到越界的 Rate。
	if _, err := db.ExecContext(ctx, "ALTER TABLE promotion_rules DROP CHECK chk_promotion_rules_discount_rate"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE promotion_rules SET discount_rate = 1.5 WHERE promotion_id = 'promo_seed_earbuds'"); err != nil {
		t.Fatal(err)
	}
	if r, err := readRate(); err == nil {
		t.Fatalf("scanning 1.5 should fail, got %s", r)
	}
}

// 已在 0001 上并写入了数据的库，升级时只执行新增的迁移，原有数据不受影响；
// 0004 按分块回填文档的 product_id（分块指向多个商品的文档保持为空）。
func TestMigrateUpgradeFrom0001(t *testing.T) {
	ctx := context.Background()
	s := freshDB(t)
	init, err := fs.ReadFile(migrations.FS, "0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx, fstest.MapFS{"0001_init.sql": {Data: init}}); err != nil {
		t.Fatal(err)
	}
	seedData := storetest.DevSeed(t)
	seedData.Documents, seedData.Chunks = nil, nil // 0001 的表结构没有 product_id，知识数据用旧结构的 SQL 写入
	if _, err := s.ApplySeed(ctx, seedData); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	for _, q := range []string{
		`INSERT INTO knowledge_documents (document_id, merchant_id, title, doc_type, content, status, content_hash)
		 VALUES ('doc_one', 'm_a', '单商品', 'product_detail', 'x', 'indexed', REPEAT('a', 64)),
		        ('doc_mixed', 'm_a', '多商品', 'product_detail', 'y', 'indexed', REPEAT('b', 64)),
		        ('doc_none', 'm_a', '无商品', 'policy', 'z', 'indexed', REPEAT('c', 64))`,
		`INSERT INTO knowledge_chunks (chunk_id, document_id, merchant_id, product_id, chunk_index, title, content) VALUES
		 ('ck_1', 'doc_one', 'm_a', 'p_1', 0, 't', 'x'), ('ck_2', 'doc_one', 'm_a', 'p_1', 1, 't', 'x'),
		 ('ck_3', 'doc_mixed', 'm_a', 'p_1', 0, 't', 'y'), ('ck_4', 'doc_mixed', 'm_a', 'p_2', 1, 't', 'y'),
		 ('ck_5', 'doc_none', 'm_a', '', 0, 't', 'z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	applied, err := s.Migrate(ctx, migrations.FS)
	want := []string{"0002_promotion_discount_rate_check.sql", "0003_knowledge_document_product.sql", "0004_backfill_knowledge_document_product.sql",
		"0005_admin_audit_logs.sql"}
	if err != nil || !reflect.DeepEqual(applied, want) {
		t.Fatalf("upgrade applied %v, %v", applied, err)
	}
	if p, err := s.GetProduct(ctx, "p_seed_nova"); err != nil || p.Price.String() != "2999.00" {
		t.Fatalf("data after upgrade: %+v, %v", p, err)
	}
	for id, want := range map[string]string{"doc_one": "p_1", "doc_mixed": "", "doc_none": ""} {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT product_id FROM knowledge_documents WHERE document_id = ?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("%s product_id = %q, %v; want %q", id, got, err, want)
		}
	}
	if n, err := s.PendingMigrations(ctx, migrations.FS); err != nil || n != 0 {
		t.Fatalf("pending after upgrade = %d, %v", n, err)
	}
}

// TestAuthTokenStoredAsDigest：auth_tokens 只有 SHA-256 摘要，没有任何一列等于 token 明文。
func TestAuthTokenStoredAsDigest(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	acc, err := s.CreateAccount(ctx, store.NewAccount{Username: "digest", PasswordHash: "h", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateAuthToken(ctx, acc.AccountID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var hash, accountID string
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*), MAX(token_hash), MAX(account_id) FROM auth_tokens`).Scan(&n, &hash, &accountID); err != nil {
		t.Fatal(err)
	}
	if n != 1 || hash != store.HashAuthToken(token) || hash == token || accountID != acc.AccountID {
		t.Fatalf("auth_tokens: n=%d hash=%q account=%q", n, hash, accountID)
	}
	var leaked int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_tokens WHERE token_hash = ? OR account_id = ?`, token, token).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("token 明文出现在 auth_tokens 中")
	}
}

// 同一账户并发加购同一规格（购物车里还没有这一行）：账户行锁让请求串行执行，不应出现死锁重试。
// 只锁购物车行时，各请求对不存在的行拿到间隙锁后互相等待 INSERT，会频繁死锁。
func TestConcurrentAddCartNoDeadlock(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	if _, err := s.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 5; round++ {
		sku := []string{"sku_seed_mouse_gray", "sku_seed_lamp_white", "sku_seed_nova_128", "sku_seed_nova_256", "sku_seed_vista_256"}[round]
		var wg sync.WaitGroup
		errs := make([]error, 30)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = s.AddCartItem(ctx, seed.UserID, "p_round", sku, func(line store.CartLine, _ int) (int, error) { return line.Quantity + 1, nil })
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
	if n := s.TxRetries(); n != 0 {
		t.Fatalf("%d transaction retries (deadlocks or duplicate inserts)", n)
	}
	lines, _ := s.ListCartLines(ctx, seed.UserID)
	if len(lines) != 5 {
		t.Fatalf("lines = %d", len(lines))
	}
	for _, l := range lines {
		if l.Quantity != 30 {
			t.Fatalf("%s quantity = %d", l.SkuID, l.Quantity)
		}
	}
}
