package memstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func (s *Store) Checkout(ctx context.Context, accountID, idempotencyKey string, fn func(ctx context.Context, st store.CheckoutState) (store.CheckoutPlan, error)) (store.CheckoutResult, error) {
	var out store.CheckoutResult
	err := s.WithTx(ctx, func(ctx context.Context) error {
		if _, ok := s.data.accounts[accountID]; !ok {
			return store.ErrNotFound
		}
		for _, req := range s.data.checkouts {
			if req.AccountID == accountID && req.IdempotencyKey == idempotencyKey && req.Status == "succeeded" {
				out = store.CheckoutResult{RequestID: req.RequestID, Replayed: true}
				for _, id := range req.OrderIDs {
					out.Orders = append(out.Orders, s.orderDetail(s.data.orders[id], true))
				}
				return nil
			}
		}

		st := store.CheckoutState{Lines: []store.CartLine{}, Coupons: []store.OwnedCoupon{}}
		for _, it := range s.data.cartItems {
			if it.AccountID == accountID && it.Selected {
				st.Lines = append(st.Lines, s.lineState(it))
			}
		}
		sort.Slice(st.Lines, func(i, j int) bool {
			a, b := st.Lines[i], st.Lines[j]
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			if a.ProductID != b.ProductID {
				return a.ProductID < b.ProductID
			}
			return a.SkuID < b.SkuID
		})
		for _, uc := range s.data.userCoupons {
			if uc.AccountID != accountID || uc.Status != domain.UserCouponUnused {
				continue
			}
			c := s.data.coupons[uc.CouponID]
			st.Coupons = append(st.Coupons, store.OwnedCoupon{UserCoupon: uc, Coupon: c})
		}
		sort.Slice(st.Coupons, func(i, j int) bool {
			a, b := st.Coupons[i], st.Coupons[j]
			if !a.ClaimedAt.Equal(b.ClaimedAt) {
				return a.ClaimedAt.After(b.ClaimedAt)
			}
			return a.UserCouponID > b.UserCouponID
		})

		plan, err := fn(ctx, st)
		if err != nil {
			return err
		}
		if err := store.ValidateCheckoutPlan(st, plan); err != nil {
			return err
		}
		requestID := domain.NewID(domain.PrefixCheckout)
		now := s.timestamp()
		deadline := plan.PaymentDeadline.UTC().Truncate(time.Millisecond)
		var orderIDs []string
		couponOrder := map[string]string{}
		for _, po := range plan.Orders {
			o := domain.Order{OrderID: domain.NewID(domain.PrefixOrder), OrderNo: store.NewOrderNo(now), AccountID: accountID, MerchantID: po.MerchantID,
				CheckoutRequestID: requestID, Status: domain.OrderPendingPayment, TotalAmount: po.TotalAmount, DiscountAmount: po.DiscountAmount,
				PayAmount: po.PayAmount, PaymentDeadlineAt: &deadline, CreatedAt: now, UpdatedAt: now}
			for _, other := range s.data.orders {
				if other.OrderNo == o.OrderNo {
					return &store.ConflictError{Key: store.KeyOrderNo}
				}
			}
			s.data.orders[o.OrderID] = o
			for i, it := range po.Items {
				// 与 MySQL 实现相同：同一订单的项依次错开 1 毫秒，保持购物车中的顺序。
				it.OrderItemID, it.OrderID, it.CreatedAt = domain.NewID(domain.PrefixOrderItem), o.OrderID, now.Add(time.Duration(i)*time.Millisecond)
				s.data.orderItems[it.OrderItemID] = it.OrderItem
				if err := s.adjustStock(it.ProductID, it.SkuID, -it.Quantity); err != nil {
					return err
				}
				delete(s.data.cartItems, it.CartItemID)
			}
			pay := domain.Payment{PaymentID: domain.NewID(domain.PrefixPayment), OrderID: o.OrderID, AccountID: accountID, Amount: o.PayAmount,
				Status: domain.PaymentPending, ExpiresAt: deadline, CreatedAt: now, UpdatedAt: now}
			s.data.payments[pay.PaymentID] = pay
			for _, id := range po.UserCouponIDs {
				if _, ok := couponOrder[id]; !ok {
					couponOrder[id] = o.OrderID
				}
			}
			orderIDs = append(orderIDs, o.OrderID)
		}
		for id, orderID := range couponOrder {
			uc, ok := s.data.userCoupons[id]
			if !ok || uc.AccountID != accountID || uc.Status != domain.UserCouponUnused {
				return fmt.Errorf("%w: 券 %s 已被使用", store.ErrInvalid, id)
			}
			uc.Status, uc.OrderID, uc.UsedAt = domain.UserCouponUsed, orderID, &now
			s.data.userCoupons[id] = uc
		}
		s.data.checkouts[requestID] = domain.CheckoutRequest{RequestID: requestID, AccountID: accountID, IdempotencyKey: idempotencyKey,
			Status: "succeeded", OrderIDs: orderIDs, CreatedAt: now, UpdatedAt: now}
		out = store.CheckoutResult{RequestID: requestID}
		for _, id := range orderIDs {
			out.Orders = append(out.Orders, s.orderDetail(s.data.orders[id], true))
		}
		return nil
	})
	return out, err
}

// adjustStock 与 MySQL 实现相同：调整规格库存并重算商品库存。调用方持有锁。
func (s *Store) adjustStock(productID, skuID string, delta int) error {
	sku, ok := s.data.skus[skuID]
	if !ok || sku.ProductID != productID {
		if delta > 0 {
			return nil
		}
		return store.ErrNotFound
	}
	sku.StockQuantity += delta
	if sku.StockQuantity < 0 {
		return fmt.Errorf("%w: 规格 %s 库存不足", store.ErrInvalid, skuID)
	}
	sku.StockStatus = domain.StockStatusOf(sku.StockQuantity)
	s.data.skus[skuID] = sku
	total := 0
	for _, other := range s.data.skus {
		if other.ProductID == productID {
			total += other.StockQuantity
		}
	}
	if p, ok := s.data.products[productID]; ok {
		p.StockQuantity, p.StockStatus = total, domain.StockStatusOf(total)
		s.data.products[productID] = p
	}
	return nil
}

// orderDetail 补齐订单项、店铺名称；full 时再补最新支付单和评价 ID。调用方持有锁。
func (s *Store) orderDetail(o domain.Order, full bool) store.OrderDetail {
	d := store.OrderDetail{Order: o, MerchantName: s.data.merchants[o.MerchantID].Name}
	d.Items = s.orderItems(o.OrderID)
	if !full {
		return d
	}
	for _, p := range s.data.payments {
		if p.OrderID != o.OrderID {
			continue
		}
		if d.Payment == nil || p.CreatedAt.After(d.Payment.CreatedAt) || (p.CreatedAt.Equal(d.Payment.CreatedAt) && p.PaymentID > d.Payment.PaymentID) {
			copied := p
			d.Payment = &copied
		}
	}
	d.ReviewIDs = map[string]string{}
	for _, r := range s.data.reviews {
		if r.OrderID == o.OrderID {
			d.ReviewIDs[r.OrderItemID] = r.ReviewID
		}
	}
	return d
}

func (s *Store) orderItems(orderID string) []domain.OrderItem {
	var items []domain.OrderItem
	for _, it := range s.data.orderItems {
		if it.OrderID == orderID {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].OrderItemID < items[j].OrderItemID
	})
	return items
}

func (s *Store) GetOrder(ctx context.Context, orderID string) (store.OrderDetail, error) {
	defer s.lock(ctx)()
	o, ok := s.data.orders[orderID]
	if !ok {
		return store.OrderDetail{}, store.ErrNotFound
	}
	return s.orderDetail(o, true), nil
}

func (s *Store) ListOrders(ctx context.Context, q store.OrderQuery) ([]store.OrderDetail, int, error) {
	defer s.lock(ctx)()
	var all []domain.Order
	for _, o := range s.data.orders {
		if (q.AccountID != "" && o.AccountID != q.AccountID) || (q.MerchantID != "" && o.MerchantID != q.MerchantID) ||
			(q.Status != "" && o.Status != q.Status) || (q.OrderNo != "" && o.OrderNo != q.OrderNo) {
			continue
		}
		all = append(all, o)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].OrderID > all[j].OrderID
	})
	out := []store.OrderDetail{}
	for _, o := range pageOf(all, q.Page) {
		out = append(out, s.orderDetail(o, false))
	}
	return out, len(all), nil
}

func (s *Store) UpdateOrder(ctx context.Context, orderID string, fn func(o *domain.Order, p *domain.Payment) error) (store.OrderDetail, error) {
	var out store.OrderDetail
	err := s.WithTx(ctx, func(ctx context.Context) error {
		o, ok := s.data.orders[orderID]
		if !ok {
			return store.ErrNotFound
		}
		d := s.orderDetail(o, true)
		pay := d.Payment
		var payBefore *domain.Payment
		if pay != nil {
			copied := *pay
			payBefore = &copied
		}
		before := o
		if err := fn(&o, pay); err != nil {
			return err
		}
		if err := store.CheckOrderUpdate(before, o, payBefore, pay); err != nil {
			return err
		}
		now := s.timestamp()
		o.Items, o.UpdatedAt = nil, now
		s.data.orders[orderID] = o
		if pay != nil {
			pay.UpdatedAt = now
			s.data.payments[pay.PaymentID] = *pay
		}
		if before.Status != domain.OrderCancelled && o.Status == domain.OrderCancelled {
			s.releaseOrder(o)
		}
		out = s.orderDetail(o, true)
		return nil
	})
	return out, err
}

// releaseOrder 与 MySQL 实现相同：回补库存，退回店铺券；同一次结算的订单全部取消后退回平台券。调用方持有锁。
func (s *Store) releaseOrder(o domain.Order) {
	for _, it := range s.orderItems(o.OrderID) {
		_ = s.adjustStock(it.ProductID, it.SkuID, it.Quantity) // 回补不会失败；规格已删除时跳过
	}
	siblings := map[string]bool{o.OrderID: true}
	siblingsOpen := false
	if o.CheckoutRequestID != "" {
		for _, other := range s.data.orders {
			if other.CheckoutRequestID != o.CheckoutRequestID || other.OrderID == o.OrderID {
				continue
			}
			siblings[other.OrderID] = true
			if other.Status != domain.OrderCancelled {
				siblingsOpen = true
			}
		}
	}
	for id, uc := range s.data.userCoupons {
		if uc.Status != domain.UserCouponUsed || uc.AccountID != o.AccountID {
			continue
		}
		var release bool
		switch {
		case o.CheckoutRequestID == "":
			release = uc.OrderID == o.OrderID
		case siblingsOpen:
			release = uc.OrderID == o.OrderID && s.data.coupons[uc.CouponID].MerchantID != ""
		default:
			release = siblings[uc.OrderID]
		}
		if release {
			uc.Status, uc.OrderID, uc.UsedAt = domain.UserCouponUnused, "", nil
			s.data.userCoupons[id] = uc
		}
	}
}

func (s *Store) ListExpiredOrderIDs(ctx context.Context, at time.Time, limit int) ([]string, error) {
	defer s.lock(ctx)()
	var expired []domain.Order
	for _, o := range s.data.orders {
		if o.Status == domain.OrderPendingPayment && o.PaymentDeadlineAt != nil && !o.PaymentDeadlineAt.After(at) {
			expired = append(expired, o)
		}
	}
	sort.Slice(expired, func(i, j int) bool {
		if !expired[i].PaymentDeadlineAt.Equal(*expired[j].PaymentDeadlineAt) {
			return expired[i].PaymentDeadlineAt.Before(*expired[j].PaymentDeadlineAt)
		}
		return expired[i].OrderID < expired[j].OrderID
	})
	out := []string{}
	for i, o := range expired {
		if i >= limit {
			break
		}
		out = append(out, o.OrderID)
	}
	return out, nil
}

func (s *Store) CreateReview(ctx context.Context, accountID, orderID, orderItemID string, fn func(o domain.Order, item domain.OrderItem) (domain.ProductReview, error)) (domain.ProductReview, error) {
	var out domain.ProductReview
	err := s.WithTx(ctx, func(ctx context.Context) error {
		o, ok := s.data.orders[orderID]
		if !ok || o.AccountID != accountID {
			return store.ErrNotFound
		}
		item, ok := s.data.orderItems[orderItemID]
		if !ok || item.OrderID != orderID {
			return store.ErrOrderItemNotFound
		}
		r, err := fn(o, item)
		if err != nil {
			return err
		}
		for _, other := range s.data.reviews {
			if other.OrderItemID == orderItemID {
				return &store.ConflictError{Key: store.KeyReviewOrderItem}
			}
		}
		now := s.timestamp()
		r.ReviewID, r.OrderID, r.OrderItemID, r.ProductID, r.SkuID, r.AccountID = domain.NewID(domain.PrefixReview), orderID, orderItemID, item.ProductID, item.SkuID, accountID
		r.CreatedAt, r.UpdatedAt = now, now
		r.Tags = cloneSlice(r.Tags)
		if r.Tags == nil {
			r.Tags = []string{}
		}
		s.data.reviews[r.ReviewID] = r
		out = r
		return nil
	})
	return out, err
}
