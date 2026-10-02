package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const cartItemColumns = `c.cart_item_id, c.account_id, c.product_id, c.sku_id, c.quantity, c.selected, c.created_at, c.updated_at`

func scanCartItem(row rowScanner, extra ...any) (domain.CartItem, error) {
	var it domain.CartItem
	dest := append([]any{&it.CartItemID, &it.AccountID, &it.ProductID, &it.SkuID, &it.Quantity, &it.Selected, &it.CreatedAt, &it.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.CartItem{}, mapErr(err)
	}
	it.CreatedAt, it.UpdatedAt = it.CreatedAt.UTC(), it.UpdatedAt.UTC()
	return it, nil
}

func (s *Store) ListCartLines(ctx context.Context, accountID string) ([]store.CartLine, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+cartItemColumns+`,
			COALESCE(p.name, ''), COALESCE(p.status, ''), COALESCE(p.image_url, ''), COALESCE(p.category_id, ''),
			COALESCE(p.merchant_id, ''), COALESCE(m.name, ''), COALESCE(m.status, ''),
			ps.sku_id IS NOT NULL, COALESCE(ps.sku_name, ''), COALESCE(ps.price, 0), COALESCE(ps.stock_quantity, 0)
		FROM cart_items c
		LEFT JOIN products p ON p.product_id = c.product_id
		LEFT JOIN merchants m ON m.merchant_id = p.merchant_id
		LEFT JOIN product_skus ps ON ps.sku_id = c.sku_id AND ps.product_id = c.product_id
		WHERE c.account_id = ?
		ORDER BY c.created_at, c.product_id, c.sku_id`, accountID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.CartLine{}
	for rows.Next() {
		var l store.CartLine
		if l.CartItem, err = scanCartItem(rows, &l.ProductName, &l.ProductStatus, &l.ImageURL, &l.CategoryID,
			&l.MerchantID, &l.MerchantName, &l.MerchantStatus, &l.SkuFound, &l.SkuName, &l.UnitPrice, &l.StockQuantity); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// retryable 判断事务是否可以重试：唯一键冲突或 InnoDB 死锁/锁等待超时（正常情况下账户行锁已避免，这里兜底）。
func retryable(err error) bool {
	var myErr *mysql.MySQLError
	return errors.Is(err, store.ErrConflict) || (errors.As(err, &myErr) && (myErr.Number == 1213 || myErr.Number == 1205))
}

func (s *Store) AddCartItem(ctx context.Context, accountID, productID, skuID string, fn func(current, lines int) (int, error)) (domain.CartItem, error) {
	var out domain.CartItem
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.WithTx(ctx, func(ctx context.Context) error {
			// 先锁定账户行，同一账户的加购在这里串行化：只锁购物车行时，两个请求对不存在的行各自拿到间隙锁，
			// 随后的 INSERT 互相等待，会造成死锁。先锁账户后锁序唯一，行数检查也是准确的。
			var locked string
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT account_id FROM accounts WHERE account_id = ? FOR UPDATE`, accountID).Scan(&locked); err != nil {
				return mapErr(err)
			}
			existing, err := scanCartItem(s.q(ctx).QueryRowContext(ctx, `SELECT `+cartItemColumns+` FROM cart_items c
				WHERE c.account_id = ? AND c.product_id = ? AND c.sku_id = ? FOR UPDATE`, accountID, productID, skuID))
			found := err == nil
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			var lines int
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM cart_items WHERE account_id = ?`, accountID).Scan(&lines); err != nil {
				return mapErr(err)
			}
			qty, err := fn(existing.Quantity, lines)
			if err != nil {
				return err
			}
			if qty <= 0 {
				return fmt.Errorf("%w: 购物车数量必须大于 0", store.ErrInvalid)
			}
			now := s.timestamp()
			if found {
				if _, err := s.q(ctx).ExecContext(ctx, `UPDATE cart_items SET quantity = ?, selected = TRUE, updated_at = ? WHERE cart_item_id = ?`,
					qty, now, existing.CartItemID); err != nil {
					return mapErr(err)
				}
				existing.Quantity, existing.Selected, existing.UpdatedAt = qty, true, now
				out = existing
				return nil
			}
			it := domain.CartItem{CartItemID: domain.NewID(domain.PrefixCartItem), AccountID: accountID, ProductID: productID, SkuID: skuID,
				Quantity: qty, Selected: true, CreatedAt: now, UpdatedAt: now}
			if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO cart_items (cart_item_id, account_id, product_id, sku_id, quantity, selected, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, it.CartItemID, it.AccountID, it.ProductID, it.SkuID, it.Quantity, it.Selected, it.CreatedAt, it.UpdatedAt); err != nil {
				return mapErr(err)
			}
			out = it
			return nil
		})
		if !retryable(err) {
			break
		}
		s.txRetries.Add(1)
	}
	return out, err
}

func (s *Store) UpdateCartItem(ctx context.Context, accountID, cartItemID string, fn func(item *domain.CartItem) error) (domain.CartItem, error) {
	var out domain.CartItem
	err := s.WithTx(ctx, func(ctx context.Context) error {
		it, err := scanCartItem(s.q(ctx).QueryRowContext(ctx, `SELECT `+cartItemColumns+` FROM cart_items c
			WHERE c.cart_item_id = ? AND c.account_id = ? FOR UPDATE`, cartItemID, accountID))
		if err != nil {
			return err
		}
		before := it
		if err := fn(&it); err != nil {
			return err
		}
		if it.Quantity <= 0 {
			return fmt.Errorf("%w: 购物车数量必须大于 0", store.ErrInvalid)
		}
		it.CartItemID, it.AccountID, it.ProductID, it.SkuID, it.CreatedAt = before.CartItemID, before.AccountID, before.ProductID, before.SkuID, before.CreatedAt
		it.UpdatedAt = s.timestamp()
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE cart_items SET quantity = ?, selected = ?, updated_at = ? WHERE cart_item_id = ?`,
			it.Quantity, it.Selected, it.UpdatedAt, it.CartItemID); err != nil {
			return mapErr(err)
		}
		out = it
		return nil
	})
	return out, err
}

func (s *Store) DeleteCartItem(ctx context.Context, accountID, cartItemID string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM cart_items WHERE cart_item_id = ? AND account_id = ?`, cartItemID, accountID)
	if err != nil {
		return mapErr(err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return store.ErrNotFound
	}
	return nil
}

const couponColumns = `cp.coupon_id, cp.name, cp.scope, cp.merchant_id, cp.type, cp.threshold_amount, cp.discount_amount, cp.total_count,
	cp.claimed_count, cp.per_user_limit, cp.start_at, cp.end_at, cp.status, cp.created_at, cp.updated_at`

func scanCoupon(row rowScanner, extra ...any) (domain.Coupon, error) {
	var c domain.Coupon
	dest := append([]any{&c.CouponID, &c.Name, &c.Scope, &c.MerchantID, &c.Type, &c.ThresholdAmount, &c.DiscountAmount, &c.TotalCount,
		&c.ClaimedCount, &c.PerUserLimit, &c.StartAt, &c.EndAt, &c.Status, &c.CreatedAt, &c.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.Coupon{}, mapErr(err)
	}
	c.StartAt, c.EndAt, c.CreatedAt, c.UpdatedAt = c.StartAt.UTC(), c.EndAt.UTC(), c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

const claimableFrom = ` FROM coupons cp LEFT JOIN merchants m ON m.merchant_id = cp.merchant_id
	WHERE cp.status = 'active' AND cp.start_at <= ? AND cp.end_at > ? AND (cp.merchant_id = '' OR m.status = 'active')`

func (s *Store) ListClaimableCoupons(ctx context.Context, at time.Time, page store.Page) ([]domain.Coupon, int, error) {
	at = at.UTC()
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+claimableFrom, at, at).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+couponColumns+claimableFrom+` ORDER BY cp.created_at DESC, cp.coupon_id DESC LIMIT ? OFFSET ?`,
		at, at, page.PageSize, page.Offset())
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Coupon{}
	for rows.Next() {
		c, err := scanCoupon(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func (s *Store) CountClaimed(ctx context.Context, accountID string, couponIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(couponIDs) == 0 {
		return out, nil
	}
	args := []any{accountID}
	for _, id := range couponIDs {
		args = append(args, id)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT coupon_id, COUNT(*) FROM user_coupons WHERE account_id = ? AND coupon_id IN (?`+
		strings.Repeat(`, ?`, len(couponIDs)-1)+`) GROUP BY coupon_id`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Store) ListUserCoupons(ctx context.Context, q store.UserCouponQuery) ([]store.OwnedCoupon, int, error) {
	at := q.At.UTC()
	where := ` FROM user_coupons uc JOIN coupons cp ON cp.coupon_id = uc.coupon_id WHERE uc.account_id = ?`
	args := []any{q.AccountID}
	switch q.Status {
	case "":
	case domain.UserCouponUnused:
		where += ` AND uc.status = 'unused' AND cp.end_at > ?`
		args = append(args, at)
	case domain.UserCouponUsed:
		where += ` AND uc.status = 'used'`
	case domain.UserCouponExpired:
		where += ` AND (uc.status = 'expired' OR (uc.status = 'unused' AND cp.end_at <= ?))`
		args = append(args, at)
	default:
		return nil, 0, fmt.Errorf("%w: 券状态 %q 不合法", store.ErrInvalid, q.Status)
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT uc.user_coupon_id, uc.coupon_id, uc.account_id, uc.status, uc.order_id, uc.claimed_at, uc.used_at, `+
		couponColumns+where+` ORDER BY uc.claimed_at DESC, uc.user_coupon_id DESC LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []store.OwnedCoupon{}
	for rows.Next() {
		var oc store.OwnedCoupon
		var usedAt sql.NullTime
		var c domain.Coupon
		if err := rows.Scan(&oc.UserCouponID, &oc.CouponID, &oc.AccountID, &oc.Status, &oc.OrderID, &oc.ClaimedAt, &usedAt,
			&c.CouponID, &c.Name, &c.Scope, &c.MerchantID, &c.Type, &c.ThresholdAmount, &c.DiscountAmount, &c.TotalCount,
			&c.ClaimedCount, &c.PerUserLimit, &c.StartAt, &c.EndAt, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, err
		}
		c.StartAt, c.EndAt, c.CreatedAt, c.UpdatedAt = c.StartAt.UTC(), c.EndAt.UTC(), c.CreatedAt.UTC(), c.UpdatedAt.UTC()
		oc.Coupon = c
		oc.ClaimedAt = oc.ClaimedAt.UTC()
		oc.UsedAt = timePtr(usedAt)
		oc.Status = store.EffectiveCouponStatus(oc.Status, c, at)
		out = append(out, oc)
	}
	return out, total, rows.Err()
}

func (s *Store) ClaimCoupon(ctx context.Context, accountID, couponID string, at time.Time) (store.OwnedCoupon, error) {
	var out store.OwnedCoupon
	err := s.WithTx(ctx, func(ctx context.Context) error {
		// 锁定券行后再判断总量和每人限领，并发领取在这里串行化，不会超发。
		var merchantStatus string
		c, err := scanCoupon(s.q(ctx).QueryRowContext(ctx, `SELECT `+couponColumns+`, COALESCE(m.status, '')
			FROM coupons cp LEFT JOIN merchants m ON m.merchant_id = cp.merchant_id WHERE cp.coupon_id = ? FOR UPDATE`, couponID), &merchantStatus)
		if err != nil {
			return err
		}
		var owned int
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM user_coupons WHERE account_id = ? AND coupon_id = ?`, accountID, couponID).Scan(&owned); err != nil {
			return mapErr(err)
		}
		if err := store.CheckClaim(c, domain.EntityStatus(merchantStatus), owned, at); err != nil {
			return err
		}
		uc := domain.UserCoupon{UserCouponID: domain.NewID(domain.PrefixUserCoup), CouponID: couponID, AccountID: accountID,
			Status: domain.UserCouponUnused, ClaimedAt: s.timestamp()}
		if err := s.insertUserCoupon(ctx, uc); err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE coupons SET claimed_count = claimed_count + 1, updated_at = ? WHERE coupon_id = ?`,
			uc.ClaimedAt, couponID); err != nil {
			return mapErr(err)
		}
		c.ClaimedCount++
		out = store.OwnedCoupon{UserCoupon: uc, Coupon: c}
		return nil
	})
	return out, err
}
