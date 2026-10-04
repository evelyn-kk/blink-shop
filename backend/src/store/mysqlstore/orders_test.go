package mysqlstore_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
	"github.com/evelyn-kk/blink-shop/backend/src/store/storetest"
)

// TestConcurrentTradeNoDeadlock 同时进行：多个账户按不同顺序加购同一批商品后结算、取消已有订单、商家修改这些商品、继续加购。
// 期望没有死锁或唯一键重试，所有操作成功，最终库存 = 初始库存 − 未取消订单的件数。
func TestConcurrentTradeNoDeadlock(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	if _, err := s.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	type item struct{ product, sku string }
	items := []item{{"p_seed_mouse", "sku_seed_mouse_gray"}, {"p_seed_lamp", "sku_seed_lamp_white"}, {"p_seed_nova", "sku_seed_nova_128"}}
	initial := map[string]int{}
	for _, it := range items {
		p, _ := s.GetProduct(ctx, it.product)
		for _, sku := range p.SKUs {
			if sku.SkuID == it.sku {
				initial[it.sku] = sku.StockQuantity
			}
		}
	}
	plan := func(_ context.Context, st store.CheckoutState) (store.CheckoutPlan, error) {
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
			po.PayAmount = po.TotalAmount
		}
		out := store.CheckoutPlan{PaymentDeadline: at.Add(30 * time.Minute)}
		for _, id := range order {
			out.Orders = append(out.Orders, *byMerchant[id])
		}
		return out, nil
	}
	newBuyer := func(name string, reverse bool) string {
		acc, err := s.CreateAccount(ctx, store.NewAccount{Username: name, PasswordHash: "$2a$04$placeholderplaceholderplaceholderplaceholderpl", Role: domain.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		for i := range items {
			it := items[i]
			if reverse {
				it = items[len(items)-1-i]
			}
			if _, err := s.AddCartItem(ctx, acc.AccountID, it.product, it.sku, func(l store.CartLine, _ int) (int, error) { return l.Quantity + 1, nil }); err != nil {
				t.Fatal(err)
			}
		}
		return acc.AccountID
	}

	// 先下 10 单，作为并发取消的对象。
	var toCancel []string
	for i := range 10 {
		acc := newBuyer(fmt.Sprintf("early_%d", i), i%2 == 1)
		res, err := s.Checkout(ctx, acc, "k", plan)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range res.Orders {
			toCancel = append(toCancel, o.OrderID)
		}
	}
	var buyers []string
	for i := range 20 {
		buyers = append(buyers, newBuyer(fmt.Sprintf("buyer_%d", i), i%2 == 1))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for _, acc := range buyers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Checkout(ctx, acc, "k", plan); err != nil {
				errs <- fmt.Errorf("checkout: %w", err)
			}
		}()
	}
	for _, id := range toCancel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateOrder(ctx, id, func(o *domain.Order, p *domain.Payment) error {
				o.Status, p.Status = domain.OrderCancelled, domain.PaymentClosed
				return nil
			})
			if err != nil {
				errs <- fmt.Errorf("cancel: %w", err)
			}
		}()
	}
	for i := range 5 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.UpdateProduct(ctx, items[i%len(items)].product, func(p *domain.Product) error { p.Description += "."; return nil })
			if err != nil {
				errs <- fmt.Errorf("update product: %w", err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := s.AddCartItem(ctx, seed.UserID, items[i%len(items)].product, items[i%len(items)].sku, func(l store.CartLine, _ int) (int, error) { return l.Quantity + 1, nil })
			if err != nil {
				errs <- fmt.Errorf("add cart: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := s.TxRetries(); n != 0 {
		t.Fatalf("%d transaction retries (deadlocks or duplicate keys)", n)
	}
	for _, it := range items {
		p, _ := s.GetProduct(ctx, it.product)
		var got int
		for _, sku := range p.SKUs {
			if sku.SkuID == it.sku {
				got = sku.StockQuantity
			}
		}
		// 20 个新订单各 1 件仍有效，最早的 10 单已全部取消回补。
		if want := initial[it.sku] - 20; got != want {
			t.Fatalf("%s stock = %d, want %d", it.sku, got, want)
		}
		// 商品库存始终等于各规格之和（并发增减不能基于过期的快照）。
		sum := 0
		for _, sku := range p.SKUs {
			sum += sku.StockQuantity
		}
		if p.StockQuantity != sum || p.StockStatus != domain.StockStatusOf(sum) {
			t.Fatalf("%s product stock = %d (%s), skus sum = %d", it.product, p.StockQuantity, p.StockStatus, sum)
		}
	}
}

// TestConcurrentProductUpdatesNoDeadlock：多个商家同时修改不同商品（含删除规格）不会死锁，也不会碰到其他商品的规格。
// 删除规格曾用 `product_id = ? AND sku_id NOT IN (…)`，被优化器走主键范围扫描，锁到其他商品的规格行。
func TestConcurrentProductUpdatesNoDeadlock(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	if _, err := s.ApplySeed(ctx, storetest.DevSeed(t)); err != nil {
		t.Fatal(err)
	}
	products := []string{"p_seed_nova", "p_seed_vista", "p_seed_earbuds", "p_seed_mouse", "p_seed_keyboard", "p_seed_lamp"}
	before := map[string]int{}
	for _, id := range products {
		p, _ := s.GetProduct(ctx, id)
		before[id] = len(p.SKUs)
	}
	for round := range 10 {
		var wg sync.WaitGroup
		errs := make(chan error, len(products))
		for _, id := range products {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.UpdateProduct(ctx, id, func(p *domain.Product) error {
					// 偶数轮加一个临时规格，奇数轮删掉它：两种写法都要覆盖“删除规格”。
					if round%2 == 0 {
						p.SKUs = append(p.SKUs, domain.ProductSKU{SkuName: "临时", Price: domain.MustMoney("1"), StockQuantity: 1, Specs: map[string]string{}})
					} else {
						p.SKUs = p.SKUs[:len(p.SKUs)-1]
					}
					return nil
				})
				if err != nil {
					errs <- fmt.Errorf("%s round %d: %w", id, round, err)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	}
	for _, id := range products {
		p, _ := s.GetProduct(ctx, id)
		if len(p.SKUs) != before[id] {
			t.Fatalf("%s has %d skus, want %d", id, len(p.SKUs), before[id])
		}
	}
}
