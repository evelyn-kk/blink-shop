package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
	"github.com/evelyn-kk/blink-shop/backend/src/configcenter"
	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/logging"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// testClock 是可以在测试中拨动的服务端时钟。
type testClock struct{ t atomic.Pointer[time.Time] }

func newTestClock(t time.Time) *testClock {
	c := &testClock{}
	c.set(t)
	return c
}
func (c *testClock) set(t time.Time)     { c.t.Store(&t) }
func (c *testClock) now() time.Time      { return *c.t.Load() }
func (c *testClock) add(d time.Duration) { c.set(c.now().Add(d)) }

// checkoutAsync 在后台发起结算，返回接收响应的通道。
func (ts *testServer) checkoutAsync(t *testing.T, tok, key string) <-chan checkoutResponse {
	t.Helper()
	out := make(chan checkoutResponse, 1)
	go func() {
		rec := ts.call(t, http.MethodPost, ordersPath+":checkout", tok, map[string]any{"idempotency_key": key})
		if rec.Code != http.StatusCreated {
			t.Errorf("checkout %s: %d %s", key, rec.Code, rec.Body)
			close(out)
			return
		}
		out <- decodeBody[checkoutResponse](t, rec)
	}()
	return out
}

// pausingStore 在 Checkout 真正开始前停住，模拟结算排队等锁（例如同账户的上一笔结算还没结束）。
type pausingStore struct {
	store.Store
	entered chan struct{}
	resume  chan struct{}
}

func (p *pausingStore) Checkout(ctx context.Context, accountID, key string, fn func(context.Context, store.CheckoutState) (store.CheckoutPlan, error)) (store.CheckoutResult, error) {
	close(p.entered)
	<-p.resume
	return p.Store.Checkout(ctx, accountID, key, fn)
}

// TestCheckoutReadsRulesAndTimeInsideTransaction（REV-010）：结算在等锁期间新增了一条平台活动、时间过去了一小时（超过 30 分钟的支付期限）。
// 订单必须按事务内此刻生效的活动计价，支付期限从订单真正创建时算起，拿到订单后可以立即支付。
func TestCheckoutReadsRulesAndTimeInsideTransaction(t *testing.T) {
	ts := newTestServer(t, nil, nil, nil)
	clock := newTestClock(testNow)
	ts.now = clock.now
	paused := &pausingStore{Store: ts.mem, entered: make(chan struct{}), resume: make(chan struct{})}
	ts.store = paused
	tok := ts.login(t, seed.User2Username, seed.DevPassword).Token
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"}) // 249：旧规则下只用平台券满 200 减 20

	done := ts.checkoutAsync(t, tok, "late")
	<-paused.entered
	addRev010Promotion(t, ts.mem)
	clock.add(time.Hour)
	close(paused.resume)
	checkLateCheckout(t, ts, tok, done)
}

func addRev010Promotion(t *testing.T, st store.Store) {
	t.Helper()
	if _, err := st.ApplySeed(context.Background(), store.SeedData{Promotions: []domain.PromotionRule{{
		PromotionID: "promo_rev010", Name: "平台满 100 减 50", Scope: domain.ScopePlatform, Type: domain.PromotionFullReduction,
		ThresholdAmount: domain.MustMoney("100"), DiscountAmount: domain.MustMoney("50"), Stackable: true,
		StartAt: testNow.Add(-time.Hour), EndAt: testNow.Add(48 * time.Hour), Status: domain.StatusActive,
	}}}); err != nil {
		t.Fatal(err)
	}
}

// checkLateCheckout 断言等待后创建的订单按新活动计价、支付期限从事务内的当前时间（testNow + 1h）算起，并能立即支付。
func checkLateCheckout(t *testing.T, ts *testServer, tok string, done <-chan checkoutResponse) {
	t.Helper()
	res, ok := <-done
	if !ok {
		t.FailNow()
	}
	o := res.Items[0]
	// 事务内的规则：新活动减 50 后 199 不到平台券门槛 200，不用券。按进入请求时的旧规则会是减 20（平台券）、实付 229。
	if o.DiscountAmount.String() != "50.00" || o.PayAmount.String() != "199.00" {
		t.Errorf("order priced with stale rules: discount %s, pay %s", o.DiscountAmount, o.PayAmount)
	}
	if want := testNow.Add(time.Hour + DefaultPaymentTimeout); o.PaymentDeadlineAt == nil || !o.PaymentDeadlineAt.Equal(want) || !o.Payment.ExpiresAt.Equal(want) {
		t.Errorf("deadline = %v / %v, want %v", o.PaymentDeadlineAt, o.Payment.ExpiresAt, want)
	}
	expectStatus(t, ts.call(t, http.MethodPost, ordersPath+"/"+o.OrderID+":pay", tok, nil), http.StatusOK, "")
}

// TestCheckoutUsesRulesFromTransactionMySQL（REV-010）：真实 MySQL 上，结算请求被另一事务挡在账户锁上；等待期间新增一条平台活动、
// 时间过去一小时。放开后订单必须按事务内此刻生效的活动计价，支付期限从此刻算起，并能立即支付。
func TestCheckoutUsesRulesFromTransactionMySQL(t *testing.T) {
	ctx := context.Background()
	st, err := mysqlstore.Open(mysqltest.FreshDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	clock := newTestClock(testNow)
	logs := &bytes.Buffer{}
	s := NewServer(Options{
		Logger: logging.New(logs, slog.LevelDebug), Store: st, PasswordCost: bcrypt.MinCost, AvatarDir: t.TempDir(), Now: clock.now,
		Settings: configcenter.NewHTTPSettingsProvider(configcenter.NewResolver(func(string) string { return "" }, configcenter.NewMemorySource(nil)), false),
	})
	ts := &testServer{Server: s, handler: s.Handler(), logs: logs}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	tok := ts.login(t, seed.User2Username, seed.DevPassword).Token
	ts.addToCart(t, tok, map[string]any{"product_id": "p_seed_lamp"}) // 249：不够平台满 300 减 30，旧规则下只用平台券满 200 减 20

	// 另一个事务锁住 blink_user2 的账户行，结算会在 Store.Checkout 的第一步等待。
	holder, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	var locked string
	if err := holder.QueryRowContext(ctx, `SELECT account_id FROM accounts WHERE account_id = ? FOR UPDATE`, seed.User2ID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	done := ts.checkoutAsync(t, tok, "waiting")
	waitForLockWait(t, st)

	// 等待期间：新增一条平台活动（已提交），时间过去一小时。
	addRev010Promotion(t, st)
	clock.add(time.Hour)
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	checkLateCheckout(t, ts, tok, done)
}

// waitForLockWait 等到本测试库里有连接正在执行结算第一步的账户加锁语句（被另一事务挡住，最多 5 秒）。
func waitForLockWait(t *testing.T, st *mysqlstore.Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := st.DB().QueryRow(`SELECT COUNT(*) FROM information_schema.processlist
			WHERE db = DATABASE() AND info LIKE 'SELECT account_id FROM accounts WHERE account_id = %FOR UPDATE'`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("checkout never waited for the account lock")
}
