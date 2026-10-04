package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/seed"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func cartRaceCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"CartUpdateWaitsForProductChange", testCartWaitsForProductChange},
		{"ProductChangeWaitsForCartUpdate", testProductWaitsForCart},
		{"CartAndProductLockOrderNoDeadlock", testCartProductNoDeadlock},
	}
}

// blockedFor 判断 done 在 d 内没有完成（调用仍在等锁）。
func blockedFor(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return false
	case <-time.After(d):
		return true
	}
}

// 商家修改商品的事务先拿到锁（下架、删规格或降库存后暂停）：购物车更新必须等它提交，
// 然后在自己的事务里读到新状态，回调据此拒绝，购物车不变。
func testCartWaitsForProductChange(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	for _, c := range []struct {
		name   string
		change func(p *domain.Product)
		check  func(l store.CartLine) bool // 回调看到的状态必须是修改后的
	}{
		{"delist", func(p *domain.Product) { p.Status = domain.ProductInactive }, func(l store.CartLine) bool { return l.ProductStatus == domain.ProductInactive }},
		{"drop sku", func(p *domain.Product) {
			p.SKUs = []domain.ProductSKU{{SkuName: "替换规格", Price: domain.MustMoney("10"), StockQuantity: 5, IsDefault: true, Specs: map[string]string{}}}
		}, func(l store.CartLine) bool { return !l.SkuFound }},
		{"lower stock", func(p *domain.Product) { p.SKUs[0].StockQuantity = 1 }, func(l store.CartLine) bool { return l.SkuFound && l.StockQuantity == 1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			// 每轮用新的购物车项，商品先恢复成可售状态。
			if _, err := s.UpdateProduct(ctx, "p_seed_lamp", func(p *domain.Product) error {
				p.Status = domain.ProductActive
				p.SKUs = []domain.ProductSKU{{SkuID: "sku_seed_lamp_white", SkuName: "Blink L1 白色", Price: domain.MustMoney("249"), StockQuantity: 60, IsDefault: true, Specs: map[string]string{}}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			item, err := s.AddCartItem(ctx, seed.UserID, "p_seed_lamp", "sku_seed_lamp_white", add(3))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateCartItem(ctx, seed.UserID, item.CartItemID, func(it *domain.CartItem, _ store.CartLine) error { it.Selected = false; return nil }); err != nil {
				t.Fatal(err)
			}

			changed, release, productDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				_, err := s.UpdateProduct(ctx, "p_seed_lamp", func(p *domain.Product) error {
					c.change(p)
					close(changed)
					<-release // 持有商品锁，暂不提交
					return nil
				})
				productDone <- err
			}()
			<-changed

			var saw store.CartLine
			cartDone := make(chan struct{})
			var cartErr error
			go func() {
				defer close(cartDone)
				_, cartErr = s.UpdateCartItem(ctx, seed.UserID, item.CartItemID, func(it *domain.CartItem, line store.CartLine) error {
					saw = line
					it.Selected = true
					if line.ProductStatus != domain.ProductActive || !line.SkuFound || line.StockQuantity < it.Quantity {
						return errBoom
					}
					return nil
				})
			}()
			// 先记录结果，再放行并等待两个事务结束；失败时不能让暂停的事务一直占着锁。
			waited := blockedFor(cartDone, 300*time.Millisecond)
			close(release)
			productErr := <-productDone
			<-cartDone
			if !waited {
				t.Fatal("cart update did not wait for the uncommitted product change")
			}
			if productErr != nil {
				t.Fatal(productErr)
			}
			if !errors.Is(cartErr, errBoom) || !c.check(saw) {
				t.Fatalf("cart update err = %v, saw %+v", cartErr, saw)
			}
			lines, _ := s.ListCartLines(ctx, seed.UserID)
			for _, l := range lines {
				if l.CartItemID == item.CartItemID && (l.Selected || l.Quantity != 3) {
					t.Fatalf("cart changed: %+v", l.CartItem)
				}
			}
			if err := s.DeleteCartItem(ctx, seed.UserID, item.CartItemID); err != nil {
				t.Fatal(err)
			}
		})
	}

	// 加购同理：降库存的事务未提交时加购等待，之后按新库存判断。
	release, changed, productDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error {
			p.SKUs[0].StockQuantity = 2
			close(changed)
			<-release
			return nil
		})
		productDone <- err
	}()
	<-changed
	addDone := make(chan struct{})
	var sawStock int
	go func() {
		defer close(addDone)
		_, _ = s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", func(line store.CartLine, _ int) (int, error) {
			sawStock = line.StockQuantity
			return 0, errBoom
		})
	}()
	waited := blockedFor(addDone, 300*time.Millisecond)
	close(release)
	productErr := <-productDone
	<-addDone
	if !waited {
		t.Fatal("add did not wait for the uncommitted product change")
	}
	if productErr != nil {
		t.Fatal(productErr)
	}
	if sawStock != 2 {
		t.Fatalf("add saw stock %d, want 2", sawStock)
	}
}

// 购物车更新先拿到锁（读取商品状态后暂停）：商家修改商品必须等购物车事务提交，两者都成功，不死锁。
func testProductWaitsForCart(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	item, err := s.AddCartItem(ctx, seed.UserID, "p_seed_lamp", "sku_seed_lamp_white", add(1))
	if err != nil {
		t.Fatal(err)
	}
	read, release, cartDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.UpdateCartItem(ctx, seed.UserID, item.CartItemID, func(it *domain.CartItem, line store.CartLine) error {
			if line.ProductStatus != domain.ProductActive {
				return errBoom
			}
			it.Quantity = 2
			close(read)
			<-release
			return nil
		})
		cartDone <- err
	}()
	<-read
	productDone := make(chan struct{})
	var productErr error
	go func() {
		defer close(productDone)
		_, productErr = s.UpdateProduct(ctx, "p_seed_lamp", func(p *domain.Product) error { p.Status = domain.ProductInactive; return nil })
	}()
	waited := blockedFor(productDone, 300*time.Millisecond)
	close(release)
	cartErr := <-cartDone
	<-productDone
	if !waited {
		t.Fatal("product change did not wait for the cart transaction that read it")
	}
	if cartErr != nil {
		t.Fatal(cartErr)
	}
	if productErr != nil {
		t.Fatalf("product update: %v", productErr)
	}
}

// 多个购物车更新/加购与商品修改交错执行（锁顺序相反的两类事务），全部成功、不死锁。
func testCartProductNoDeadlock(t *testing.T, s store.Store) {
	ctx := context.Background()
	seeded(t, s)
	item, err := s.AddCartItem(ctx, seed.UserID, "p_seed_mouse", "sku_seed_mouse_gray", add(1))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for g := 0; g < 4; g++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				_, err := s.UpdateCartItem(ctx, seed.UserID, item.CartItemID, func(it *domain.CartItem, _ store.CartLine) error {
					it.Selected = !it.Selected
					return nil
				})
				if err != nil {
					errs <- err
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				if _, err := s.AddCartItem(ctx, seed.User2ID, "p_seed_mouse", "sku_seed_mouse_gray", func(line store.CartLine, _ int) (int, error) {
					return line.Quantity%50 + 1, nil
				}); err != nil {
					errs <- err
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				if _, err := s.UpdateProduct(ctx, "p_seed_mouse", func(p *domain.Product) error {
					p.SKUs[0].StockQuantity = 100 + i
					return nil
				}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("interleaved cart/product transactions failed: %v", err)
	}
	if r, ok := s.(interface{ TxRetries() int64 }); ok && r.TxRetries() != 0 {
		t.Fatalf("%d transaction retries", r.TxRetries())
	}
}
