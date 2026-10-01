// Package storetest 是 store.Store 的契约测试：MySQL 实现与内存实现跑同一套用例，保证行为一致。
package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// Factory 每次返回一个全新的空 Store。
type Factory func(t *testing.T) store.Store

// FastHash 用最低成本的 bcrypt 生成哈希，仅供测试提速。
func FastHash(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	return string(b), err
}

// DevSeed 返回使用 FastHash 的开发种子。
func DevSeed(t *testing.T) store.SeedData {
	t.Helper()
	data, err := seed.Dev(FastHash)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Run 执行全部契约用例。
func Run(t *testing.T, newStore Factory) {
	cases := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"CreateAndGetAccount", testCreateAndGetAccount},
		{"DuplicateUsernameIsConflict", testDuplicateUsername},
		{"ConcurrentDuplicateUsername", testConcurrentDuplicateUsername},
		{"InvalidAccountInput", testInvalidAccount},
		{"NotFound", testNotFound},
		{"TxCommit", testTxCommit},
		{"TxRollbackOnError", testTxRollbackOnError},
		{"TxRollbackOnConflict", testTxRollbackOnConflict},
		{"TxRollbackOnPanic", testTxRollbackOnPanic},
		{"NestedTxJoinsOuter", testNestedTx},
		{"SeedIsRepeatable", testSeedRepeatable},
		{"SeedAccountsUseBcrypt", testSeedPasswords},
		{"SeedProductRoundTrip", testSeedProduct},
		{"SeedCategoriesOrdered", testSeedCategories},
		{"SeedConflictRollsBackEverything", testSeedConflict},
		{"SoftDeletedAccountHidden", testSoftDeleted},
		{"UniqueKeysReportedByName", testUniqueKeyNames},
		{"OutOfRangeDiscountRateRejected", testOutOfRangeRate},
	}
	cases = append(cases, authCases()...)
	cases = append(cases, catalogCases()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newStore(t)) })
	}
}

func newUser(username string) store.NewAccount {
	return store.NewAccount{Username: username, PasswordHash: "$2a$04$placeholderplaceholderplaceholderplaceholderpl", DisplayName: "测试用户", Role: domain.RoleUser}
}

func mustCreate(t *testing.T, s store.Store, ctx context.Context, in store.NewAccount) domain.Account {
	t.Helper()
	acc, err := s.CreateAccount(ctx, in)
	if err != nil {
		t.Fatalf("CreateAccount(%s): %v", in.Username, err)
	}
	return acc
}

func expectNotFound(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%s: err = %v, want ErrNotFound", what, err)
	}
}

func testCreateAndGetAccount(t *testing.T, s store.Store) {
	ctx := context.Background()
	in := newUser("alice")
	in.Phone, in.Email = "13800000000", "alice@example.com"
	created := mustCreate(t, s, ctx, in)

	if !strings.HasPrefix(created.AccountID, domain.PrefixAccount) || created.Status != domain.StatusActive || created.CreatedAt.IsZero() {
		t.Fatalf("unexpected created account: %+v", created)
	}
	got, err := s.GetAccount(ctx, created.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Fatalf("GetAccount = %+v\nwant %+v", got, created)
	}
	byName, hash, err := s.GetAccountByUsername(ctx, "alice")
	if err != nil || !reflect.DeepEqual(byName, created) || hash != in.PasswordHash {
		t.Fatalf("GetAccountByUsername = %+v, %q, %v", byName, hash, err)
	}
}

func testDuplicateUsername(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreate(t, s, ctx, newUser("bob"))
	_, err := s.CreateAccount(ctx, newUser("bob"))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	var conflict *store.ConflictError
	if !errors.As(err, &conflict) || conflict.Key != store.KeyAccountUsername {
		t.Fatalf("conflict key = %+v, want %s", conflict, store.KeyAccountUsername)
	}
}

func testConcurrentDuplicateUsername(t *testing.T, s store.Store) {
	ctx := context.Background()
	const n = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		ok, confl int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateAccount(ctx, newUser("racer"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrConflict):
				confl++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || confl != n-1 {
		t.Fatalf("success=%d conflict=%d, want 1 and %d", ok, confl, n-1)
	}
}

func testInvalidAccount(t *testing.T, s store.Store) {
	ctx := context.Background()
	bad := []store.NewAccount{
		{Username: "", PasswordHash: "h", Role: domain.RoleUser},
		{Username: "x", PasswordHash: "", Role: domain.RoleUser},
		{Username: "x", PasswordHash: "h", Role: "root"},
		{Username: "x", PasswordHash: "h", Role: domain.RoleMerchant},
		{Username: "x", PasswordHash: "h", Role: domain.RoleUser, MerchantID: "m_1"},
	}
	for i, in := range bad {
		if _, err := s.CreateAccount(ctx, in); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("case %d: err = %v, want ErrInvalid", i, err)
		}
	}
}

func testNotFound(t *testing.T, s store.Store) {
	ctx := context.Background()
	_, err := s.GetAccount(ctx, "acct_missing")
	expectNotFound(t, err, "GetAccount")
	_, _, err = s.GetAccountByUsername(ctx, "nobody")
	expectNotFound(t, err, "GetAccountByUsername")
	_, err = s.GetProduct(ctx, "p_missing")
	expectNotFound(t, err, "GetProduct")
	cats, err := s.ListCategories(ctx)
	if err != nil || cats == nil || len(cats) != 0 {
		t.Fatalf("ListCategories on empty store = %v, %v; want empty non-nil slice", cats, err)
	}
}

func testTxCommit(t *testing.T, s store.Store) {
	ctx := context.Background()
	var id string
	err := s.WithTx(ctx, func(ctx context.Context) error {
		acc, err := s.CreateAccount(ctx, newUser("carol"))
		id = acc.AccountID
		if err != nil {
			return err
		}
		// 事务内可读到自己的写入。
		_, err = s.GetAccount(ctx, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAccount(ctx, id); err != nil {
		t.Fatalf("committed account not visible: %v", err)
	}
}

var errBoom = errors.New("boom")

func testTxRollbackOnError(t *testing.T, s store.Store) {
	ctx := context.Background()
	var id string
	err := s.WithTx(ctx, func(ctx context.Context) error {
		id = mustCreate(t, s, ctx, newUser("dave")).AccountID
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WithTx err = %v, want errBoom", err)
	}
	_, err = s.GetAccount(ctx, id)
	expectNotFound(t, err, "rolled back account")
}

func testTxRollbackOnConflict(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreate(t, s, ctx, newUser("erin"))
	err := s.WithTx(ctx, func(ctx context.Context) error {
		mustCreate(t, s, ctx, newUser("frank"))
		_, err := s.CreateAccount(ctx, newUser("erin"))
		return err
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("WithTx err = %v, want ErrConflict", err)
	}
	_, _, err = s.GetAccountByUsername(ctx, "frank")
	expectNotFound(t, err, "frank after rollback")
}

func testTxRollbackOnPanic(t *testing.T, s store.Store) {
	ctx := context.Background()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic should propagate")
			}
		}()
		_ = s.WithTx(ctx, func(ctx context.Context) error {
			mustCreate(t, s, ctx, newUser("grace"))
			panic("handler bug")
		})
	}()
	_, _, err := s.GetAccountByUsername(ctx, "grace")
	expectNotFound(t, err, "grace after panic")
	// panic 后 Store 仍可用。
	mustCreate(t, s, ctx, newUser("grace"))
}

func testNestedTx(t *testing.T, s store.Store) {
	ctx := context.Background()
	err := s.WithTx(ctx, func(ctx context.Context) error {
		mustCreate(t, s, ctx, newUser("heidi"))
		if err := s.WithTx(ctx, func(ctx context.Context) error {
			mustCreate(t, s, ctx, newUser("ivan"))
			return nil
		}); err != nil {
			return err
		}
		return errBoom // 外层失败，内层的写入也必须回滚
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	for _, name := range []string{"heidi", "ivan"} {
		_, _, err := s.GetAccountByUsername(ctx, name)
		expectNotFound(t, err, name)
	}
}

func testSeedRepeatable(t *testing.T, s store.Store) {
	ctx := context.Background()
	data := DevSeed(t)
	first, err := s.ApplySeed(ctx, data)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	want := map[string]int{
		"accounts": len(data.Accounts), "merchants": len(data.Merchants), "categories": len(data.Categories),
		"products": len(data.Products), "knowledge_documents": len(data.Documents), "knowledge_chunks": len(data.Chunks),
		"promotion_rules": len(data.Promotions), "coupons": len(data.Coupons), "user_coupons": len(data.UserCoupons),
		"orders": len(data.Orders), "payments": len(data.Payments), "product_reviews": len(data.Reviews),
	}
	for table, n := range want {
		if first[table] != n || n == 0 {
			t.Errorf("first seed %s = %d, want %d (>0)", table, first[table], n)
		}
	}
	if first["product_skus"] < len(data.Products) || first["order_items"] < len(data.Orders) {
		t.Errorf("child rows missing: %v", first)
	}
	second, err := s.ApplySeed(ctx, data)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if second.Total() != 0 {
		t.Fatalf("second seed inserted rows: %v", second)
	}
}

func testSeedPasswords(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	roles := map[string]domain.Role{
		seed.AdminUsername: domain.RoleAdmin, seed.MerchantUsername: domain.RoleMerchant, seed.UserUsername: domain.RoleUser,
	}
	for username, role := range roles {
		acc, hash, err := s.GetAccountByUsername(ctx, username)
		if err != nil {
			t.Fatalf("%s: %v", username, err)
		}
		if acc.Role != role {
			t.Errorf("%s role = %s, want %s", username, acc.Role, role)
		}
		if hash == seed.DevPassword || !strings.HasPrefix(hash, "$2") {
			t.Errorf("%s password column is not a bcrypt hash: %q", username, hash)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(seed.DevPassword)); err != nil {
			t.Errorf("%s hash does not verify dev password: %v", username, err)
		}
	}
	merchant, _, _ := s.GetAccountByUsername(ctx, seed.MerchantUsername)
	if merchant.MerchantID != seed.DigitalMerchant {
		t.Errorf("merchant account not bound to merchant: %+v", merchant)
	}
}

func testSeedProduct(t *testing.T, s store.Store) {
	ctx := context.Background()
	data := DevSeed(t)
	if _, err := s.ApplySeed(ctx, data); err != nil {
		t.Fatal(err)
	}
	var want domain.Product
	for _, p := range data.Products {
		if p.ProductID == "p_seed_nova" {
			want = p
		}
	}
	got, err := s.GetProduct(ctx, "p_seed_nova")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("product round trip mismatch\n got: %+v\nwant: %+v", got, want)
	}
	if got.Price.String() != "2999.00" || len(got.SKUs) != 2 || !got.SKUs[0].IsDefault || got.SKUs[1].Price.String() != "3299.00" {
		t.Fatalf("unexpected price/SKUs: %s %+v", got.Price, got.SKUs)
	}

	// 字段为空的 JSON 列读回为空切片，而不是 nil。
	speaker, err := s.GetProduct(ctx, "p_seed_speaker")
	if err != nil {
		t.Fatal(err)
	}
	if speaker.Attributes == nil || len(speaker.Attributes) != 0 || speaker.Status != domain.ProductInactive {
		t.Fatalf("speaker = %+v", speaker)
	}
}

func testSeedCategories(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.ApplySeed(ctx, DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	cats, err := s.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cats {
		ids = append(ids, c.CategoryID)
	}
	want := []string{"c_digital", "c_office", "c_home", "c_phone", "c_audio", "c_lamp", "c_mouse", "c_keyboard"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("category order = %v, want %v", ids, want)
	}
}

func testSeedConflict(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreate(t, s, ctx, newUser(seed.UserUsername)) // 先占用种子用户名
	data := DevSeed(t)
	_, err := s.ApplySeed(ctx, data)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ApplySeed err = %v, want ErrConflict", err)
	}
	// 整个种子回滚：先于冲突写入的管理员也不存在。
	_, _, err = s.GetAccountByUsername(ctx, seed.AdminUsername)
	expectNotFound(t, err, "admin after failed seed")
	cats, _ := s.ListCategories(ctx)
	if len(cats) != 0 {
		t.Fatalf("categories written despite rollback: %d", len(cats))
	}
}

func testSoftDeleted(t *testing.T, s store.Store) {
	ctx := context.Background()
	deleted := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	data := store.SeedData{Accounts: []store.SeedAccount{{PasswordHash: "h", Account: domain.Account{
		AccountID: "acct_gone", Username: "gone", DisplayName: "已注销", Role: domain.RoleUser,
		Status: domain.StatusActive, DeletedAt: &deleted,
	}}}}
	if _, err := s.ApplySeed(ctx, data); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetAccount(ctx, "acct_gone")
	expectNotFound(t, err, "deleted account by id")
	_, _, err = s.GetAccountByUsername(ctx, "gone")
	expectNotFound(t, err, "deleted account by username")
	// 用户名在注销后不可复用。
	if _, err := s.CreateAccount(ctx, newUser("gone")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reusing deleted username: err = %v, want ErrConflict", err)
	}
}

// testUniqueKeyNames 确认 docs/02 中的唯一约束在两种实现里都生效，并报告相同的键名。
func testUniqueKeyNames(t *testing.T, s store.Store) {
	ctx := context.Background()
	base := DevSeed(t)
	if _, err := s.ApplySeed(ctx, base); err != nil {
		t.Fatal(err)
	}
	order := base.Orders[0]
	order.OrderID, order.Items = "o_dup_no", nil // 新主键、重复 order_no
	review := base.Reviews[0]
	review.ReviewID = "rv_dup" // 同一订单项重复评价
	doc := base.Documents[0]
	doc.DocumentID = "doc_dup" // 同商家同内容
	chunk := base.Chunks[0]
	chunk.ChunkID = "ck_dup" // 同文档同序号

	cases := []struct {
		name string
		data store.SeedData
		key  string
	}{
		{"order_no", store.SeedData{Orders: []domain.Order{order}}, store.KeyOrderNo},
		{"review per order item", store.SeedData{Reviews: []domain.ProductReview{review}}, store.KeyReviewOrderItem},
		{"document merchant+hash", store.SeedData{Documents: []domain.KnowledgeDocument{doc}}, store.KeyDocumentMerchantHash},
		{"chunk document+index", store.SeedData{Chunks: []domain.KnowledgeChunk{chunk}}, store.KeyChunkDocumentIndex},
	}
	for _, c := range cases {
		_, err := s.ApplySeed(ctx, c.data)
		var conflict *store.ConflictError
		if !errors.As(err, &conflict) || conflict.Key != c.key {
			t.Errorf("%s: err = %v, want conflict on %s", c.name, err, c.key)
		}
	}
}

// testOutOfRangeRate 确认越界折扣率在写入前被拒绝（ErrInvalid），且同一份种子整体回滚。
func testOutOfRangeRate(t *testing.T, s store.Store) {
	ctx := context.Background()
	base := DevSeed(t)
	for _, bad := range []domain.Rate{10001, 15000, -1} {
		promo := base.Promotions[2]
		promo.PromotionID = "promo_bad_rate"
		promo.DiscountRate = bad // 绕过 ParseRate 直接构造
		data := store.SeedData{
			Accounts:   base.Accounts[:1],
			Promotions: []domain.PromotionRule{promo},
		}
		if _, err := s.ApplySeed(ctx, data); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("rate %s: err = %v, want ErrInvalid", bad, err)
		}
		_, _, err := s.GetAccountByUsername(ctx, base.Accounts[0].Username)
		expectNotFound(t, err, "account in rejected seed")
	}
	// 边界值 0 和 1 可以写入。
	for i, ok := range []domain.Rate{0, 10000} {
		promo := base.Promotions[2]
		promo.PromotionID = fmt.Sprintf("promo_edge_%d", i)
		promo.DiscountRate = ok
		if _, err := s.ApplySeed(ctx, store.SeedData{Promotions: []domain.PromotionRule{promo}}); err != nil {
			t.Errorf("rate %s should be accepted: %v", ok, err)
		}
	}
}
