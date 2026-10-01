package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ApplySeed 见 store.Store。主键已存在的记录跳过（不覆盖本地修改），其余唯一键冲突使整个种子回滚。
func (s *Store) ApplySeed(ctx context.Context, data store.SeedData) (store.SeedResult, error) {
	result := store.SeedResult{}
	err := s.WithTx(ctx, func(ctx context.Context) error {
		ins := &seedInserter{s: s, ctx: ctx, result: result}
		for _, a := range data.Accounts {
			ins.row("accounts", "account_id", a.AccountID, func() error { return s.insertAccount(ctx, a.Account, a.PasswordHash) })
		}
		for _, m := range data.Merchants {
			ins.row("merchants", "merchant_id", m.MerchantID, func() error { return s.insertMerchant(ctx, m) })
		}
		for _, c := range data.Categories {
			ins.row("categories", "category_id", c.CategoryID, func() error { return s.insertCategory(ctx, c) })
		}
		for _, p := range data.Products {
			ins.row("products", "product_id", p.ProductID, func() error { return s.insertProduct(ctx, p) })
			for _, sku := range p.SKUs {
				ins.row("product_skus", "sku_id", sku.SkuID, func() error { return s.insertSKU(ctx, sku) })
			}
		}
		for _, d := range data.Documents {
			ins.row("knowledge_documents", "document_id", d.DocumentID, func() error { return s.insertDocument(ctx, d) })
		}
		for _, c := range data.Chunks {
			ins.row("knowledge_chunks", "chunk_id", c.ChunkID, func() error { return s.insertChunk(ctx, c) })
		}
		for _, p := range data.Promotions {
			ins.row("promotion_rules", "promotion_id", p.PromotionID, func() error { return s.insertPromotion(ctx, p) })
		}
		for _, c := range data.Coupons {
			ins.row("coupons", "coupon_id", c.CouponID, func() error { return s.insertCoupon(ctx, c) })
		}
		for _, uc := range data.UserCoupons {
			ins.row("user_coupons", "user_coupon_id", uc.UserCouponID, func() error { return s.insertUserCoupon(ctx, uc) })
		}
		for _, o := range data.Orders {
			ins.row("orders", "order_id", o.OrderID, func() error { return s.insertOrder(ctx, o) })
			for _, it := range o.Items {
				ins.row("order_items", "order_item_id", it.OrderItemID, func() error { return s.insertOrderItem(ctx, it) })
			}
		}
		for _, p := range data.Payments {
			ins.row("payments", "payment_id", p.PaymentID, func() error { return s.insertPayment(ctx, p) })
		}
		for _, r := range data.Reviews {
			ins.row("product_reviews", "review_id", r.ReviewID, func() error { return s.insertReview(ctx, r) })
		}
		return ins.err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// seedInserter 按主键“不存在才插入”，并记录第一个错误。
type seedInserter struct {
	s      *Store
	ctx    context.Context
	result store.SeedResult
	err    error
}

func (i *seedInserter) row(table, pkColumn, id string, insert func() error) {
	if i.err != nil {
		return
	}
	if _, ok := i.result[table]; !ok {
		i.result[table] = 0
	}
	// table / pkColumn 只来自本文件的常量，不来自外部输入。
	var one int
	err := i.s.q(i.ctx).QueryRowContext(i.ctx, "SELECT 1 FROM "+table+" WHERE "+pkColumn+" = ? FOR UPDATE", id).Scan(&one)
	switch {
	case err == nil:
		return
	case !errors.Is(err, sql.ErrNoRows):
		i.err = err
		return
	}
	if err := insert(); err != nil {
		i.err = err
		return
	}
	i.result[table]++
}

func (s *Store) insertMerchant(ctx context.Context, m domain.Merchant) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO merchants
		(merchant_id, name, logo_url, description, service_phone, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MerchantID, m.Name, m.LogoURL, m.Description, m.ServicePhone, m.Status, s.orNow(m.CreatedAt), s.orNow(m.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertCategory(ctx context.Context, c domain.Category) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO categories
		(category_id, parent_id, name, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.CategoryID, c.ParentID, c.Name, c.SortOrder, s.orNow(c.CreatedAt), s.orNow(c.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertProduct(ctx context.Context, p domain.Product) error {
	// []string 和属性列表的 JSON 序列化不会失败。
	images, _ := jsonArray(p.ImageURLs)
	tags, _ := jsonArray(p.Tags)
	points, _ := jsonArray(p.SellingPoints)
	risks, _ := jsonArray(p.RiskNotes)
	attrs, _ := jsonArray(p.Attributes)
	suitable, _ := jsonArray(p.SuitableFor)
	unsuitable, _ := jsonArray(p.NotSuitableFor)
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO products (`+productColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ProductID, p.MerchantID, p.CategoryID, p.Name, p.Brand, p.ImageURL, images,
		p.Price, p.MarketPrice, p.StockQuantity, p.StockStatus, tags, points, p.RecommendReason,
		risks, attrs, suitable, unsuitable, p.Description, p.Status,
		p.SortOrder, s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertSKU(ctx context.Context, sku domain.ProductSKU) error {
	specs, err := jsonObject(sku.Specs)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO product_skus
		(sku_id, product_id, sku_name, price, stock_quantity, stock_status, specs_json, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sku.SkuID, sku.ProductID, sku.SkuName, sku.Price, sku.StockQuantity, sku.StockStatus, specs, sku.IsDefault,
		s.orNow(sku.CreatedAt), s.orNow(sku.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertDocument(ctx context.Context, d domain.KnowledgeDocument) error {
	meta, err := nullableJSON(d.Metadata)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO knowledge_documents
		(document_id, merchant_id, title, doc_type, content, status, chunk_count, source_url, content_hash,
		 metadata_json, error_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.DocumentID, d.MerchantID, d.Title, d.DocType, d.Content, d.Status, d.ChunkCount, d.SourceURL, d.ContentHash,
		meta, d.ErrorReason, s.orNow(d.CreatedAt), s.orNow(d.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertChunk(ctx context.Context, c domain.KnowledgeChunk) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO knowledge_chunks
		(chunk_id, document_id, merchant_id, product_id, chunk_index, title, content, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ChunkID, c.DocumentID, c.MerchantID, c.ProductID, c.ChunkIndex, c.Title, c.Content, c.Source, s.orNow(c.CreatedAt))
	return mapErr(err)
}

func (s *Store) insertPromotion(ctx context.Context, p domain.PromotionRule) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO promotion_rules
		(promotion_id, name, scope, merchant_id, product_id, category_id, type, threshold_amount, discount_amount,
		 discount_rate, stackable, start_at, end_at, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PromotionID, p.Name, p.Scope, p.MerchantID, p.ProductID, p.CategoryID, p.Type, p.ThresholdAmount, p.DiscountAmount,
		p.DiscountRate, p.Stackable, p.StartAt.UTC(), p.EndAt.UTC(), p.Status, s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertCoupon(ctx context.Context, c domain.Coupon) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO coupons
		(coupon_id, name, scope, merchant_id, type, threshold_amount, discount_amount, total_count, claimed_count,
		 per_user_limit, start_at, end_at, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CouponID, c.Name, c.Scope, c.MerchantID, c.Type, c.ThresholdAmount, c.DiscountAmount, c.TotalCount, c.ClaimedCount,
		c.PerUserLimit, c.StartAt.UTC(), c.EndAt.UTC(), c.Status, s.orNow(c.CreatedAt), s.orNow(c.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertUserCoupon(ctx context.Context, uc domain.UserCoupon) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO user_coupons
		(user_coupon_id, coupon_id, account_id, status, order_id, claimed_at, used_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uc.UserCouponID, uc.CouponID, uc.AccountID, uc.Status, uc.OrderID, s.orNow(uc.ClaimedAt), nullTime(uc.UsedAt))
	return mapErr(err)
}

func (s *Store) insertOrder(ctx context.Context, o domain.Order) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO orders
		(order_id, order_no, account_id, merchant_id, checkout_request_id, status, total_amount, discount_amount, pay_amount,
		 payment_deadline_at, paid_at, shipped_at, completed_at, closed_at, cancel_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OrderID, o.OrderNo, o.AccountID, o.MerchantID, o.CheckoutRequestID, o.Status, o.TotalAmount, o.DiscountAmount, o.PayAmount,
		nullTime(o.PaymentDeadlineAt), nullTime(o.PaidAt), nullTime(o.ShippedAt), nullTime(o.CompletedAt), nullTime(o.ClosedAt),
		o.CancelReason, s.orNow(o.CreatedAt), s.orNow(o.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertOrderItem(ctx context.Context, it domain.OrderItem) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO order_items
		(order_item_id, order_id, product_id, sku_id, name, sku_name, image_url, price, quantity, merchant_id, merchant_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.OrderItemID, it.OrderID, it.ProductID, it.SkuID, it.Name, it.SkuName, it.ImageURL, it.Price, it.Quantity,
		it.MerchantID, it.MerchantName, s.orNow(it.CreatedAt))
	return mapErr(err)
}

func (s *Store) insertPayment(ctx context.Context, p domain.Payment) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO payments
		(payment_id, order_id, account_id, amount, status, method, transaction_no, expires_at, paid_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PaymentID, p.OrderID, p.AccountID, p.Amount, p.Status, p.Method, p.TransactionNo, p.ExpiresAt.UTC(), nullTime(p.PaidAt),
		s.orNow(p.CreatedAt), s.orNow(p.UpdatedAt))
	return mapErr(err)
}

func (s *Store) insertReview(ctx context.Context, r domain.ProductReview) error {
	tags, err := jsonArray(r.Tags)
	if err != nil {
		return err
	}
	var reply any
	if strings.TrimSpace(r.MerchantReply) != "" {
		reply = r.MerchantReply
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO product_reviews
		(review_id, order_id, order_item_id, product_id, sku_id, account_id, rating, content, tags_json, status,
		 merchant_reply, merchant_replied_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ReviewID, r.OrderID, r.OrderItemID, r.ProductID, r.SkuID, r.AccountID, r.Rating, r.Content, tags, r.Status,
		reply, nullTime(r.MerchantRepliedAt), s.orNow(r.CreatedAt), s.orNow(r.UpdatedAt))
	return mapErr(err)
}
