package mysqlstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// simplePlan 按购物车当前数据（单价、数量）拆单，不做优惠。
func simplePlan(at time.Time) func(context.Context, store.CheckoutState) (store.CheckoutPlan, error) {
	return func(_ context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		byMerchant := map[string]*store.PlannedOrder{}
		var order []string
		for _, l := range st.Lines {
			po := byMerchant[l.MerchantID]
			if po == nil {
				po = &store.PlannedOrder{MerchantID: l.MerchantID}
				byMerchant[l.MerchantID] = po
				order = append(order, l.MerchantID)
			}
			po.Items = append(po.Items, store.PlannedItem{CartItemID: l.CartItemID, OrderItem: domain.OrderItem{ProductID: l.ProductID, SkuID: l.SkuID,
				Name: l.ProductName, Price: l.UnitPrice, Quantity: l.Quantity, MerchantID: l.MerchantID, MerchantName: l.MerchantName}})
			po.TotalAmount += l.UnitPrice.Mul(l.Quantity)
			po.PayAmount += l.UnitPrice.Mul(l.Quantity)
		}
		out := store.CheckoutPlan{PaymentDeadline: at.Add(30 * time.Minute)}
		for _, id := range order {
			out.Orders = append(out.Orders, *byMerchant[id])
		}
		return out, nil
	}
}

// 结算快路径：读完快照、扣库存之前，别人买走了最后一件 → 回退到加锁路径，按最新库存拒绝，不超卖；
// 商家改了价格 → 回退后按新价格下单。两种情况都只回退一次，不计入死锁 / 冲突重试。
func TestCheckoutFastPathFallsBackOnStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	if _, err := s.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	buyer := func(name, product, sku string) string {
		acc, err := s.CreateAccount(ctx, store.NewAccount{Username: name, PasswordHash: "$2a$04$placeholderplaceholderplaceholderplaceholderpl", Role: domain.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddCartItem(ctx, acc.AccountID, product, sku, func(l store.CartLine, _ int) (int, error) { return 1, nil }); err != nil {
			t.Fatal(err)
		}
		return acc.AccountID
	}
	setSku := func(product, sku string, fn func(k *domain.ProductSKU)) {
		if _, err := s.UpdateProduct(ctx, product, func(p *domain.Product) error {
			for i := range p.SKUs {
				if p.SKUs[i].SkuID == sku {
					fn(&p.SKUs[i])
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	stockOf := func(product, sku string) int {
		p, err := s.GetProduct(ctx, product)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range p.SKUs {
			if k.SkuID == sku {
				return k.StockQuantity
			}
		}
		return -1
	}

	// 1. 最后一件被别人先买走
	setSku("p_seed_lamp", "sku_seed_lamp_white", func(k *domain.ProductSKU) { k.StockQuantity = 1 })
	a, b := buyer("fast_a", "p_seed_lamp", "sku_seed_lamp_white"), buyer("fast_b", "p_seed_lamp", "sku_seed_lamp_white")
	retries, fallbacks := s.TxRetries(), s.CheckoutFallbacks()
	calls := 0
	_, err := s.Checkout(ctx, a, "k", func(ctx context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		calls++
		if calls == 1 { // 快路径：此时没有持有商品行锁，另一个账户可以完整结算
			if _, err := s.Checkout(context.Background(), b, "k", simplePlan(at)); err != nil {
				t.Errorf("b checkout: %v", err)
			}
		}
		return simplePlan(at)(ctx, st)
	})
	if !errors.Is(err, store.ErrInvalid) || calls != 2 {
		t.Fatalf("a should be rejected by the locked retry: err=%v calls=%d", err, calls)
	}
	if got := stockOf("p_seed_lamp", "sku_seed_lamp_white"); got != 0 {
		t.Fatalf("stock = %d, want 0", got)
	}
	if s.CheckoutFallbacks()-fallbacks != 1 || s.TxRetries() != retries {
		t.Fatalf("fallbacks +%d retries +%d", s.CheckoutFallbacks()-fallbacks, s.TxRetries()-retries)
	}

	// 2. 读完快照后商家改价：回退后按新价格下单
	c := buyer("fast_c", "p_seed_mouse", "sku_seed_mouse_gray")
	before := stockOf("p_seed_mouse", "sku_seed_mouse_gray")
	fallbacks = s.CheckoutFallbacks()
	calls = 0
	res, err := s.Checkout(ctx, c, "k", func(ctx context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
		calls++
		if calls == 1 {
			setSku("p_seed_mouse", "sku_seed_mouse_gray", func(k *domain.ProductSKU) { k.Price = domain.MustMoney("99.00") })
		}
		return simplePlan(at)(ctx, st)
	})
	if err != nil || calls != 2 || len(res.Orders) != 1 || len(res.Orders[0].Items) != 1 {
		t.Fatalf("price change: err=%v calls=%d %+v", err, calls, res)
	}
	if got := res.Orders[0].Items[0].Price; got != domain.MustMoney("99.00") {
		t.Fatalf("order price %s, want the new price", got)
	}
	if got := stockOf("p_seed_mouse", "sku_seed_mouse_gray"); got != before-1 {
		t.Fatalf("stock %d, want %d", got, before-1)
	}
	if s.CheckoutFallbacks()-fallbacks != 1 {
		t.Fatalf("fallbacks +%d", s.CheckoutFallbacks()-fallbacks)
	}

	// 3. 没有并发变化：快路径一次完成，不回退
	d := buyer("fast_d", "p_seed_mouse", "sku_seed_mouse_gray")
	fallbacks = s.CheckoutFallbacks()
	if _, err := s.Checkout(ctx, d, "k", simplePlan(at)); err != nil || s.CheckoutFallbacks() != fallbacks {
		t.Fatalf("plain checkout: %v fallbacks +%d", err, s.CheckoutFallbacks()-fallbacks)
	}
}
