package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const orderColumns = `o.order_id, o.order_no, o.account_id, o.merchant_id, o.checkout_request_id, o.status, o.total_amount, o.discount_amount,
	o.pay_amount, o.payment_deadline_at, o.paid_at, o.shipped_at, o.completed_at, o.closed_at, o.cancel_reason, o.created_at, o.updated_at`

func scanOrder(row rowScanner, extra ...any) (domain.Order, error) {
	var o domain.Order
	var deadline, paid, shipped, completed, closed sql.NullTime
	dest := append([]any{&o.OrderID, &o.OrderNo, &o.AccountID, &o.MerchantID, &o.CheckoutRequestID, &o.Status, &o.TotalAmount, &o.DiscountAmount,
		&o.PayAmount, &deadline, &paid, &shipped, &completed, &closed, &o.CancelReason, &o.CreatedAt, &o.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.Order{}, mapErr(err)
	}
	o.PaymentDeadlineAt, o.PaidAt, o.ShippedAt, o.CompletedAt, o.ClosedAt = timePtr(deadline), timePtr(paid), timePtr(shipped), timePtr(completed), timePtr(closed)
	o.CreatedAt, o.UpdatedAt = o.CreatedAt.UTC(), o.UpdatedAt.UTC()
	return o, nil
}

const paymentColumns = `payment_id, order_id, account_id, amount, status, method, transaction_no, expires_at, paid_at, created_at, updated_at`

func scanPayment(row rowScanner) (domain.Payment, error) {
	var p domain.Payment
	var paid sql.NullTime
	if err := row.Scan(&p.PaymentID, &p.OrderID, &p.AccountID, &p.Amount, &p.Status, &p.Method, &p.TransactionNo, &p.ExpiresAt, &paid,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return domain.Payment{}, mapErr(err)
	}
	p.PaidAt = timePtr(paid)
	p.ExpiresAt, p.CreatedAt, p.UpdatedAt = p.ExpiresAt.UTC(), p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

func (s *Store) Checkout(ctx context.Context, accountID, idempotencyKey string, at time.Time, fn func(st store.CheckoutState) (store.CheckoutPlan, error)) (store.CheckoutResult, error) {
	var out store.CheckoutResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		out = store.CheckoutResult{}
		err = s.WithTx(ctx, func(ctx context.Context) error {
			// 锁顺序：账户 → 用户券 → 购物车行 → 商品（按 ID）→ 店铺 → 规格（按 ID），全部按主键加锁。与加购（账户 → 购物车行 → 商品 → 店铺 → 规格）、
			// 改购物车（购物车行 → 商品 → 店铺 → 规格）、商家改商品（商品 → 规格）、取消订单（结算请求 → 订单 → 支付单 → 用户券 → 商品 → 规格）都不会形成环。
			var locked string
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT account_id FROM accounts WHERE account_id = ? FOR UPDATE`, accountID).Scan(&locked); err != nil {
				return mapErr(err)
			}
			var requestID, ordersJSON string
			err := s.q(ctx).QueryRowContext(ctx, `SELECT request_id, order_ids_json FROM checkout_requests
				WHERE account_id = ? AND idempotency_key = ? AND status = 'succeeded'`, accountID, idempotencyKey).Scan(&requestID, &ordersJSON)
			if err == nil {
				var ids []string
				if err := json.Unmarshal([]byte(ordersJSON), &ids); err != nil {
					return fmt.Errorf("checkout_requests.order_ids_json: %w", err)
				}
				out = store.CheckoutResult{RequestID: requestID, Replayed: true}
				for _, id := range ids {
					d, err := s.GetOrder(ctx, id)
					if err != nil {
						return err
					}
					out.Orders = append(out.Orders, d)
				}
				return nil
			}
			if !errors.Is(mapErr(err), store.ErrNotFound) {
				return mapErr(err)
			}

			st, err := s.lockCheckoutState(ctx, accountID, at)
			if err != nil {
				return err
			}
			plan, err := fn(st)
			if err != nil {
				return err
			}
			if err := store.ValidateCheckoutPlan(st, plan); err != nil {
				return err
			}
			requestID = domain.NewID(domain.PrefixCheckout)
			now := s.timestamp()
			deadline := plan.PaymentDeadline.UTC().Truncate(time.Millisecond)
			var orderIDs, cartItemIDs []string
			couponOrder := map[string]string{}
			for _, po := range plan.Orders {
				o := domain.Order{OrderID: domain.NewID(domain.PrefixOrder), OrderNo: store.NewOrderNo(now), AccountID: accountID, MerchantID: po.MerchantID,
					CheckoutRequestID: requestID, Status: domain.OrderPendingPayment, TotalAmount: po.TotalAmount, DiscountAmount: po.DiscountAmount,
					PayAmount: po.PayAmount, PaymentDeadlineAt: &deadline, CreatedAt: now, UpdatedAt: now}
				if err := s.insertOrder(ctx, o); err != nil {
					return err
				}
				for i, it := range po.Items {
					// 订单项按 created_at 排序；同一订单的项依次错开 1 毫秒，保持购物车中的顺序（created_at 不对外输出）。
					it.OrderItemID, it.OrderID, it.CreatedAt = domain.NewID(domain.PrefixOrderItem), o.OrderID, now.Add(time.Duration(i)*time.Millisecond)
					if err := s.insertOrderItem(ctx, it.OrderItem); err != nil {
						return err
					}
					if err := s.adjustStock(ctx, it.ProductID, it.SkuID, -it.Quantity); err != nil {
						return err
					}
					cartItemIDs = append(cartItemIDs, it.CartItemID)
				}
				pay := domain.Payment{PaymentID: domain.NewID(domain.PrefixPayment), OrderID: o.OrderID, AccountID: accountID, Amount: o.PayAmount,
					Status: domain.PaymentPending, ExpiresAt: deadline, CreatedAt: now, UpdatedAt: now}
				if err := s.insertPayment(ctx, pay); err != nil {
					return err
				}
				for _, id := range po.UserCouponIDs {
					if _, ok := couponOrder[id]; !ok {
						couponOrder[id] = o.OrderID
					}
				}
				orderIDs = append(orderIDs, o.OrderID)
			}
			for id, orderID := range couponOrder {
				res, err := s.q(ctx).ExecContext(ctx, `UPDATE user_coupons SET status = 'used', order_id = ?, used_at = ?
					WHERE user_coupon_id = ? AND account_id = ? AND status = 'unused'`, orderID, now, id, accountID)
				if err != nil {
					return mapErr(err)
				}
				if n, _ := res.RowsAffected(); n != 1 {
					return fmt.Errorf("%w: 券 %s 已被使用", store.ErrInvalid, id)
				}
			}
			// 按主键删除（这些行已加锁且已核对属于本账户），避免范围扫描加间隙锁。
			for _, id := range cartItemIDs {
				if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM cart_items WHERE cart_item_id = ?`, id); err != nil {
					return mapErr(err)
				}
			}
			ids, _ := json.Marshal(orderIDs)
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO checkout_requests (request_id, account_id, idempotency_key, status, order_ids_json, created_at, updated_at)
				VALUES (?, ?, ?, 'succeeded', ?, ?, ?)`, requestID, accountID, idempotencyKey, string(ids), now, now); err != nil {
				return mapErr(err)
			}
			out = store.CheckoutResult{RequestID: requestID}
			for _, id := range orderIDs {
				d, err := s.GetOrder(ctx, id)
				if err != nil {
					return err
				}
				out.Orders = append(out.Orders, d)
			}
			return nil
		})
		// 订单号撞车或兜底的死锁可以整体重试；同一幂等键的并发请求已被账户行锁串行化，不会走到唯一键冲突。
		if !retryable(err) {
			break
		}
		s.txRetries.Add(1)
	}
	return out, err
}

// lockCheckoutState 读取并锁定结算所需的数据，调用方已持有账户行锁。
// 用户券最先锁：取消订单退券也是“用户券 → 商品 → 规格”的顺序，两边不会互相等待。
func (s *Store) lockCheckoutState(ctx context.Context, accountID string, at time.Time) (store.CheckoutState, error) {
	coupons, err := s.lockUnusedCoupons(ctx, accountID, at)
	if err != nil {
		return store.CheckoutState{}, err
	}
	// 购物车项和券都按主键加锁，不做范围加锁：范围锁会在唯一索引上留下间隙锁，挡住其他账户加购时的插入，
	// 而加购事务已持有商品共享锁，结算随后要商品排他锁，就会死锁。候选 ID 用普通读取；本账户的加购被账户行锁挡在外面，
	// 只有“修改选中状态”可能并发，加锁后按最新的 selected 再筛一次。
	ids, err := s.plainIDs(ctx, `SELECT cart_item_id FROM cart_items WHERE account_id = ? AND selected = TRUE`, accountID)
	if err != nil {
		return store.CheckoutState{}, err
	}
	sort.Strings(ids)
	var items []domain.CartItem
	for _, id := range ids {
		it, err := scanCartItem(s.q(ctx).QueryRowContext(ctx, `SELECT `+cartItemColumns+` FROM cart_items c WHERE c.cart_item_id = ? FOR UPDATE`, id))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return store.CheckoutState{}, err
		}
		if it.AccountID == accountID && it.Selected {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		return a.SkuID < b.SkuID
	})

	// 商品和规格按 ID 顺序加排他锁：两个账户结算同一批商品时加锁顺序一致，不会死锁；扣库存时不需要再把共享锁升级。
	var productIDs, skuIDs []string
	for _, it := range items {
		productIDs = appendUnique(productIDs, it.ProductID)
		skuIDs = appendUnique(skuIDs, it.SkuID)
	}
	sort.Strings(productIDs)
	sort.Strings(skuIDs)
	type productRow struct {
		name, image, category, merchant string
		status                          domain.ProductStatus
	}
	products := map[string]productRow{}
	merchants := map[string]domain.Merchant{}
	for _, id := range productIDs {
		var p productRow
		err := s.q(ctx).QueryRowContext(ctx, `SELECT name, status, image_url, category_id, merchant_id FROM products WHERE product_id = ? FOR UPDATE`, id).
			Scan(&p.name, &p.status, &p.image, &p.category, &p.merchant)
		if errors.Is(mapErr(err), store.ErrNotFound) {
			continue
		}
		if err != nil {
			return store.CheckoutState{}, mapErr(err)
		}
		products[id] = p
		if _, ok := merchants[p.merchant]; !ok {
			var m domain.Merchant
			err := s.q(ctx).QueryRowContext(ctx, `SELECT name, status FROM merchants WHERE merchant_id = ? FOR SHARE`, p.merchant).Scan(&m.Name, &m.Status)
			if err != nil && !errors.Is(mapErr(err), store.ErrNotFound) {
				return store.CheckoutState{}, mapErr(err)
			}
			merchants[p.merchant] = m
		}
	}
	type skuRow struct {
		productID, name string
		price           domain.Money
		stock           int
	}
	skus := map[string]skuRow{}
	for _, id := range skuIDs {
		var r skuRow
		err := s.q(ctx).QueryRowContext(ctx, `SELECT product_id, sku_name, price, stock_quantity FROM product_skus WHERE sku_id = ? FOR UPDATE`, id).
			Scan(&r.productID, &r.name, &r.price, &r.stock)
		if errors.Is(mapErr(err), store.ErrNotFound) {
			continue
		}
		if err != nil {
			return store.CheckoutState{}, mapErr(err)
		}
		skus[id] = r
	}

	st := store.CheckoutState{Lines: []store.CartLine{}, Coupons: coupons}
	for _, it := range items {
		l := store.CartLine{CartItem: it}
		if p, ok := products[it.ProductID]; ok {
			l.ProductName, l.ProductStatus, l.ImageURL, l.CategoryID, l.MerchantID = p.name, p.status, p.image, p.category, p.merchant
			l.MerchantName, l.MerchantStatus = merchants[p.merchant].Name, merchants[p.merchant].Status
		}
		if r, ok := skus[it.SkuID]; ok && r.productID == it.ProductID {
			l.SkuFound, l.SkuName, l.UnitPrice, l.StockQuantity = true, r.name, r.price, r.stock
		}
		st.Lines = append(st.Lines, l)
	}

	return st, nil
}

// lockUnusedCoupons 按主键逐张锁定账户未使用的券（状态按 at 计算），同样不做范围加锁。
func (s *Store) lockUnusedCoupons(ctx context.Context, accountID string, at time.Time) ([]store.OwnedCoupon, error) {
	ids, err := s.plainIDs(ctx, `SELECT user_coupon_id FROM user_coupons WHERE account_id = ? AND status = 'unused'`, accountID)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	out := []store.OwnedCoupon{}
	for _, id := range ids {
		var oc store.OwnedCoupon
		var usedAt sql.NullTime
		err := s.q(ctx).QueryRowContext(ctx, `SELECT user_coupon_id, coupon_id, account_id, status, order_id, claimed_at, used_at
			FROM user_coupons WHERE user_coupon_id = ? FOR UPDATE`, id).
			Scan(&oc.UserCouponID, &oc.CouponID, &oc.AccountID, &oc.Status, &oc.OrderID, &oc.ClaimedAt, &usedAt)
		if errors.Is(mapErr(err), store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, mapErr(err)
		}
		if oc.AccountID != accountID || oc.Status != domain.UserCouponUnused {
			continue
		}
		c, err := scanCoupon(s.q(ctx).QueryRowContext(ctx, `SELECT `+couponColumns+` FROM coupons cp WHERE cp.coupon_id = ?`, oc.CouponID))
		if err != nil {
			return nil, err
		}
		oc.Coupon, oc.ClaimedAt, oc.UsedAt = c, oc.ClaimedAt.UTC(), timePtr(usedAt)
		oc.Status = store.EffectiveCouponStatus(oc.Status, c, at)
		out = append(out, oc)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ClaimedAt.Equal(out[j].ClaimedAt) {
			return out[i].ClaimedAt.After(out[j].ClaimedAt)
		}
		return out[i].UserCouponID > out[j].UserCouponID
	})
	return out, nil
}

// plainIDs 用普通（不加锁）读取返回单列 ID。
func (s *Store) plainIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// stockStatusSQL 按 stock_quantity 的新值计算库存状态，与 domain.StockStatusOf 一致（单表 UPDATE 中后面的赋值看到前面赋值后的新值）。
var stockStatusSQL = fmt.Sprintf(`CASE WHEN stock_quantity <= 0 THEN '%s' WHEN stock_quantity <= %d THEN '%s' ELSE '%s' END`,
	domain.StockOutOfStock, domain.LowStockThreshold, domain.StockLow, domain.StockInStock)

// adjustStock 调整规格库存（delta 为负表示扣减），并按同样的增量调整商品库存（商品库存 = 各规格之和）。调用方已锁定商品和规格行。
// 用 UPDATE 基于最新提交的值增减，不依赖事务开始时的快照。规格已被删除时：回补跳过，扣减返回 ErrNotFound；扣减后为负返回 ErrInvalid。
func (s *Store) adjustStock(ctx context.Context, productID, skuID string, delta int) error {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE product_skus SET stock_quantity = stock_quantity + ?, stock_status = `+stockStatusSQL+`
		WHERE sku_id = ? AND product_id = ? AND stock_quantity + ? >= 0`, delta, skuID, productID, delta)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var stock int
		err := s.q(ctx).QueryRowContext(ctx, `SELECT stock_quantity FROM product_skus WHERE sku_id = ? AND product_id = ? FOR UPDATE`, skuID, productID).Scan(&stock)
		switch {
		case errors.Is(mapErr(err), store.ErrNotFound) && delta > 0:
			return nil
		case err != nil:
			return mapErr(err)
		}
		return fmt.Errorf("%w: 规格 %s 库存不足", store.ErrInvalid, skuID)
	}
	_, err = s.q(ctx).ExecContext(ctx, `UPDATE products SET stock_quantity = stock_quantity + ?, stock_status = `+stockStatusSQL+` WHERE product_id = ?`,
		delta, productID)
	return mapErr(err)
}

func (s *Store) GetOrder(ctx context.Context, orderID string) (store.OrderDetail, error) {
	var name string
	o, err := scanOrder(s.q(ctx).QueryRowContext(ctx, `SELECT `+orderColumns+`, COALESCE(m.name, '') FROM orders o
		LEFT JOIN merchants m ON m.merchant_id = o.merchant_id WHERE o.order_id = ?`, orderID), &name)
	if err != nil {
		return store.OrderDetail{}, err
	}
	return s.orderDetail(ctx, o, name, true)
}

// orderDetail 补齐订单项、店铺名称；full 时再补最新支付单和评价 ID。merchantName 为空时查询。
func (s *Store) orderDetail(ctx context.Context, o domain.Order, merchantName string, full bool) (store.OrderDetail, error) {
	d := store.OrderDetail{Order: o, MerchantName: merchantName}
	if merchantName == "" {
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT COALESCE((SELECT name FROM merchants WHERE merchant_id = ?), '')`, o.MerchantID).Scan(&d.MerchantName); err != nil {
			return store.OrderDetail{}, mapErr(err)
		}
	}
	items, err := s.listOrderItems(ctx, []string{o.OrderID})
	if err != nil {
		return store.OrderDetail{}, err
	}
	d.Items = items[o.OrderID]
	if !full {
		return d, nil
	}
	pay, err := scanPayment(s.q(ctx).QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM payments WHERE order_id = ?
		ORDER BY created_at DESC, payment_id DESC LIMIT 1`, o.OrderID))
	switch {
	case err == nil:
		d.Payment = &pay
	case !errors.Is(err, store.ErrNotFound):
		return store.OrderDetail{}, err
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT order_item_id, review_id FROM product_reviews WHERE order_id = ?`, o.OrderID)
	if err != nil {
		return store.OrderDetail{}, mapErr(err)
	}
	defer rows.Close()
	d.ReviewIDs = map[string]string{}
	for rows.Next() {
		var itemID, reviewID string
		if err := rows.Scan(&itemID, &reviewID); err != nil {
			return store.OrderDetail{}, err
		}
		d.ReviewIDs[itemID] = reviewID
	}
	return d, rows.Err()
}

func (s *Store) listOrderItems(ctx context.Context, orderIDs []string) (map[string][]domain.OrderItem, error) {
	out := map[string][]domain.OrderItem{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(orderIDs))
	for i, id := range orderIDs {
		args[i] = id
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT order_item_id, order_id, product_id, sku_id, name, sku_name, image_url, price, quantity,
		merchant_id, merchant_name, created_at FROM order_items WHERE order_id IN (?`+strings.Repeat(`, ?`, len(orderIDs)-1)+`)
		ORDER BY created_at, order_item_id`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var it domain.OrderItem
		if err := rows.Scan(&it.OrderItemID, &it.OrderID, &it.ProductID, &it.SkuID, &it.Name, &it.SkuName, &it.ImageURL, &it.Price, &it.Quantity,
			&it.MerchantID, &it.MerchantName, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.CreatedAt = it.CreatedAt.UTC()
		out[it.OrderID] = append(out[it.OrderID], it)
	}
	return out, rows.Err()
}

func (s *Store) ListOrders(ctx context.Context, q store.OrderQuery) ([]store.OrderDetail, int, error) {
	where := ` FROM orders o LEFT JOIN merchants m ON m.merchant_id = o.merchant_id WHERE 1 = 1`
	var args []any
	for _, f := range []struct{ column, value string }{
		{"o.account_id", q.AccountID}, {"o.merchant_id", q.MerchantID}, {"o.status", string(q.Status)}, {"o.order_no", q.OrderNo},
	} {
		if f.value != "" {
			where += ` AND ` + f.column + ` = ?`
			args = append(args, f.value)
		}
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+orderColumns+`, COALESCE(m.name, '')`+where+
		` ORDER BY o.created_at DESC, o.order_id DESC LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	out := []store.OrderDetail{}
	var ids []string
	for rows.Next() {
		var d store.OrderDetail
		if d.Order, err = scanOrder(rows, &d.MerchantName); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, d)
		ids = append(ids, d.OrderID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	items, err := s.listOrderItems(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].Items = items[out[i].OrderID]
	}
	return out, total, nil
}

func (s *Store) UpdateOrder(ctx context.Context, orderID string, fn func(o *domain.Order, p *domain.Payment) error) (store.OrderDetail, error) {
	var out store.OrderDetail
	err := s.WithTx(ctx, func(ctx context.Context) error {
		// 锁顺序：结算请求 → 订单 → 支付单 → 用户券 → 商品（按 ID）→ 规格，全部按主键加锁，不加间隙锁，
		// 并发结算往 orders / payments / user_coupons 插入新行时不会被挡住。
		// checkout_request_id 和支付单 ID 创建后不变，先用普通读取拿到，再按主键加锁读取最新数据。
		var requestID string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT checkout_request_id FROM orders WHERE order_id = ?`, orderID).Scan(&requestID); err != nil {
			return mapErr(err)
		}
		// 同一次结算的订单先锁结算请求行：判断“平台券能否退回”要看兄弟订单是否都已取消，这些订单的修改都在这里串行。
		var siblings []string
		if requestID != "" {
			var ids string
			err := s.q(ctx).QueryRowContext(ctx, `SELECT order_ids_json FROM checkout_requests WHERE request_id = ? FOR UPDATE`, requestID).Scan(&ids)
			if err != nil {
				return mapErr(err)
			}
			if err := json.Unmarshal([]byte(ids), &siblings); err != nil {
				return fmt.Errorf("checkout_requests.order_ids_json: %w", err)
			}
		}
		o, err := scanOrder(s.q(ctx).QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders o WHERE o.order_id = ? FOR UPDATE`, orderID))
		if err != nil {
			return err
		}
		var pay *domain.Payment
		var paymentID string
		err = s.q(ctx).QueryRowContext(ctx, `SELECT payment_id FROM payments WHERE order_id = ? ORDER BY created_at DESC, payment_id DESC LIMIT 1`, orderID).Scan(&paymentID)
		switch {
		case err == nil:
			p, err := scanPayment(s.q(ctx).QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM payments WHERE payment_id = ? FOR UPDATE`, paymentID))
			if err != nil {
				return err
			}
			pay = &p
		case !errors.Is(mapErr(err), store.ErrNotFound):
			return mapErr(err)
		}
		before := o
		var payBefore *domain.Payment
		if pay != nil {
			copied := *pay
			payBefore = &copied
		}
		if err := fn(&o, pay); err != nil {
			return err
		}
		if err := store.CheckOrderUpdate(before, o, payBefore, pay); err != nil {
			return err
		}
		now := s.timestamp()
		o.UpdatedAt = now
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE orders SET status = ?, paid_at = ?, shipped_at = ?, completed_at = ?, closed_at = ?,
			cancel_reason = ?, updated_at = ? WHERE order_id = ?`, o.Status, nullTime(o.PaidAt), nullTime(o.ShippedAt), nullTime(o.CompletedAt),
			nullTime(o.ClosedAt), o.CancelReason, now, orderID); err != nil {
			return mapErr(err)
		}
		if pay != nil {
			pay.UpdatedAt = now
			if _, err := s.q(ctx).ExecContext(ctx, `UPDATE payments SET status = ?, method = ?, transaction_no = ?, paid_at = ?, updated_at = ?
				WHERE payment_id = ?`, pay.Status, pay.Method, pay.TransactionNo, nullTime(pay.PaidAt), now, pay.PaymentID); err != nil {
				return mapErr(err)
			}
		}
		if before.Status != domain.OrderCancelled && o.Status == domain.OrderCancelled {
			if err := s.releaseOrder(ctx, o, siblings); err != nil {
				return err
			}
		}
		out, err = s.orderDetail(ctx, o, "", true)
		return err
	})
	return out, err
}

// releaseOrder 在订单取消时退回券并回补库存。调用方已锁定结算请求行和订单；siblings 是同一次结算的全部订单 ID（种子订单为空）。
func (s *Store) releaseOrder(ctx context.Context, o domain.Order, siblings []string) error {
	// 店铺券只用在本订单上，直接退回；平台券在同一次结算的订单全部取消后退回。
	// 兄弟订单的取消都要先锁结算请求行，这里按主键加锁读取它们的最新状态。
	release := map[string]bool{o.OrderID: true}
	allClosed := true
	for _, id := range siblings {
		if id == o.OrderID {
			continue
		}
		var status domain.OrderStatus
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT status FROM orders WHERE order_id = ? FOR SHARE`, id).Scan(&status); err != nil {
			return mapErr(err)
		}
		if status != domain.OrderCancelled {
			allClosed = false
		}
		release[id] = true
	}
	// 候选券用普通读取（快照可能稍旧：已被兄弟订单退回的券还显示为已使用），退回时按主键加条件更新，已退回的不会再改。
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT uc.user_coupon_id, uc.order_id, cp.merchant_id FROM user_coupons uc
		JOIN coupons cp ON cp.coupon_id = uc.coupon_id WHERE uc.account_id = ? AND uc.status = 'used'`, o.AccountID)
	if err != nil {
		return mapErr(err)
	}
	type candidate struct{ id, orderID string }
	var back []candidate
	for rows.Next() {
		var c candidate
		var merchantID string
		if err := rows.Scan(&c.id, &c.orderID, &merchantID); err != nil {
			rows.Close()
			return err
		}
		switch {
		case c.orderID == o.OrderID && (merchantID != "" || allClosed):
			back = append(back, c)
		case c.orderID != o.OrderID && release[c.orderID] && allClosed:
			back = append(back, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range back {
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE user_coupons SET status = 'unused', order_id = '', used_at = NULL
			WHERE user_coupon_id = ? AND status = 'used' AND order_id = ?`, c.id, c.orderID); err != nil {
			return mapErr(err)
		}
	}

	items, err := s.listOrderItems(ctx, []string{o.OrderID})
	if err != nil {
		return err
	}
	lines := items[o.OrderID]
	// 与结算相同的加锁顺序：商品（按 ID）→ 规格（按 ID）。
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].ProductID != lines[j].ProductID {
			return lines[i].ProductID < lines[j].ProductID
		}
		return lines[i].SkuID < lines[j].SkuID
	})
	for _, it := range lines {
		var locked string
		err := s.q(ctx).QueryRowContext(ctx, `SELECT product_id FROM products WHERE product_id = ? FOR UPDATE`, it.ProductID).Scan(&locked)
		if errors.Is(mapErr(err), store.ErrNotFound) {
			continue
		}
		if err != nil {
			return mapErr(err)
		}
	}
	for _, it := range lines {
		if err := s.adjustStock(ctx, it.ProductID, it.SkuID, it.Quantity); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListExpiredOrderIDs(ctx context.Context, at time.Time, limit int) ([]string, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT order_id FROM orders WHERE status = 'pending_payment' AND payment_deadline_at <= ?
		ORDER BY payment_deadline_at, order_id LIMIT ?`, at.UTC(), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) CreateReview(ctx context.Context, accountID, orderID, orderItemID string, fn func(o domain.Order, item domain.OrderItem) (domain.ProductReview, error)) (domain.ProductReview, error) {
	var out domain.ProductReview
	err := s.WithTx(ctx, func(ctx context.Context) error {
		o, err := scanOrder(s.q(ctx).QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders o WHERE o.order_id = ? FOR SHARE`, orderID))
		if err != nil {
			return err
		}
		if o.AccountID != accountID {
			return store.ErrNotFound
		}
		items, err := s.listOrderItems(ctx, []string{orderID})
		if err != nil {
			return err
		}
		var item *domain.OrderItem
		for i := range items[orderID] {
			if items[orderID][i].OrderItemID == orderItemID {
				item = &items[orderID][i]
			}
		}
		if item == nil {
			return store.ErrOrderItemNotFound
		}
		r, err := fn(o, *item)
		if err != nil {
			return err
		}
		now := s.timestamp()
		r.ReviewID, r.OrderID, r.OrderItemID, r.ProductID, r.SkuID, r.AccountID = domain.NewID(domain.PrefixReview), orderID, orderItemID, item.ProductID, item.SkuID, accountID
		r.CreatedAt, r.UpdatedAt = now, now
		if r.Tags == nil {
			r.Tags = []string{}
		}
		if err := s.insertReview(ctx, r); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}
