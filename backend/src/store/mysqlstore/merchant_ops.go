package mysqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// ---------- 促销管理 ----------

func (s *Store) CreatePromotion(ctx context.Context, p domain.PromotionRule) (domain.PromotionRule, error) {
	if p.PromotionID == "" {
		p.PromotionID = domain.NewID(domain.PrefixPromotion)
	}
	now := s.timestamp()
	p.StartAt, p.EndAt = p.StartAt.UTC().Truncate(time.Millisecond), p.EndAt.UTC().Truncate(time.Millisecond)
	p.CreatedAt, p.UpdatedAt = now, now
	if err := s.insertPromotion(ctx, p); err != nil {
		return domain.PromotionRule{}, err
	}
	return p, nil
}

func (s *Store) GetPromotion(ctx context.Context, promotionID string) (domain.PromotionRule, error) {
	return scanPromotion(s.q(ctx).QueryRowContext(ctx, `SELECT `+promotionColumns+` FROM promotion_rules WHERE promotion_id = ?`, promotionID))
}

func (s *Store) UpdatePromotion(ctx context.Context, promotionID string, fn func(ctx context.Context, p *domain.PromotionRule) error) (domain.PromotionRule, error) {
	var out domain.PromotionRule
	err := s.WithTx(ctx, func(ctx context.Context) error {
		p, err := scanPromotion(s.q(ctx).QueryRowContext(ctx, `SELECT `+promotionColumns+` FROM promotion_rules WHERE promotion_id = ? FOR UPDATE`, promotionID))
		if err != nil {
			return err
		}
		before := p
		if err := fn(ctx, &p); err != nil {
			return err
		}
		p.PromotionID, p.CreatedAt, p.UpdatedAt = before.PromotionID, before.CreatedAt, s.timestamp()
		p.StartAt, p.EndAt = p.StartAt.UTC().Truncate(time.Millisecond), p.EndAt.UTC().Truncate(time.Millisecond)
		if err := store.ValidatePromotion(p); err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE promotion_rules SET name = ?, scope = ?, merchant_id = ?, product_id = ?, category_id = ?,
			type = ?, threshold_amount = ?, discount_amount = ?, discount_rate = ?, stackable = ?, start_at = ?, end_at = ?, status = ?, updated_at = ?
			WHERE promotion_id = ?`, p.Name, p.Scope, p.MerchantID, p.ProductID, p.CategoryID, p.Type, p.ThresholdAmount, p.DiscountAmount,
			p.DiscountRate, p.Stackable, p.StartAt, p.EndAt, p.Status, p.UpdatedAt, promotionID); err != nil {
			return mapErr(err)
		}
		out = p
		return nil
	})
	return out, err
}

func (s *Store) ListPromotions(ctx context.Context, q store.PromotionListQuery) ([]domain.PromotionRule, int, error) {
	where := ` FROM promotion_rules WHERE 1 = 1`
	var args []any
	if q.MerchantID != "" {
		where += ` AND merchant_id = ?`
		args = append(args, q.MerchantID)
	}
	if q.Status != "" {
		where += ` AND status = ?`
		args = append(args, q.Status)
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+promotionColumns+where+` ORDER BY created_at DESC, promotion_id DESC LIMIT ? OFFSET ?`,
		append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.PromotionRule{}
	for rows.Next() {
		p, err := scanPromotion(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// ---------- 商家评价 ----------

const merchantReviewSelect = `SELECT r.review_id, r.order_id, r.order_item_id, r.product_id, r.sku_id, r.account_id,
		r.rating, r.content, r.tags_json, r.status, COALESCE(r.merchant_reply, ''), r.merchant_replied_at, r.created_at, r.updated_at,
		p.name, COALESCE(IF(a.deleted_at IS NULL, a.display_name, ''), '')
	FROM product_reviews r JOIN products p ON p.product_id = r.product_id LEFT JOIN accounts a ON a.account_id = r.account_id`

func scanMerchantReview(row rowScanner) (store.MerchantReview, error) {
	var (
		r       store.MerchantReview
		tags    []byte
		replied sql.NullTime
	)
	if err := row.Scan(&r.ReviewID, &r.OrderID, &r.OrderItemID, &r.ProductID, &r.SkuID, &r.AccountID, &r.Rating, &r.Content, &tags,
		&r.Status, &r.MerchantReply, &replied, &r.CreatedAt, &r.UpdatedAt, &r.ProductName, &r.ReviewerName); err != nil {
		return store.MerchantReview{}, mapErr(err)
	}
	if err := fromJSON("tags_json", tags, &r.Tags); err != nil {
		return store.MerchantReview{}, err
	}
	r.MerchantRepliedAt = timePtr(replied)
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return r, nil
}

func (s *Store) ListMerchantReviews(ctx context.Context, q store.MerchantReviewQuery) ([]store.MerchantReview, int, error) {
	where := ` WHERE p.merchant_id = ?`
	args := []any{q.MerchantID}
	if q.Replied != nil {
		if *q.Replied {
			where += ` AND r.merchant_replied_at IS NOT NULL`
		} else {
			where += ` AND r.merchant_replied_at IS NULL`
		}
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM product_reviews r JOIN products p ON p.product_id = r.product_id`+where,
		args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, merchantReviewSelect+where+` ORDER BY r.created_at DESC, r.review_id DESC LIMIT ? OFFSET ?`,
		append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []store.MerchantReview{}
	for rows.Next() {
		r, err := scanMerchantReview(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *Store) ReplyReview(ctx context.Context, merchantID, reviewID, reply string, at time.Time) (store.MerchantReview, error) {
	var out store.MerchantReview
	err := s.WithTx(ctx, func(ctx context.Context) error {
		// 按主键锁评价行；商品的归属用 JOIN 判断（商品的 merchant_id 创建后不变）。
		r, err := scanMerchantReview(s.q(ctx).QueryRowContext(ctx, merchantReviewSelect+` WHERE r.review_id = ? AND p.merchant_id = ? FOR UPDATE OF r`,
			reviewID, merchantID))
		if err != nil {
			return err
		}
		at = at.UTC().Truncate(time.Millisecond)
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE product_reviews SET merchant_reply = ?, merchant_replied_at = ?, updated_at = ? WHERE review_id = ?`,
			reply, at, s.timestamp(), reviewID); err != nil {
			return mapErr(err)
		}
		r.MerchantReply, r.MerchantRepliedAt = reply, &at
		out = r
		return nil
	})
	return out, err
}
