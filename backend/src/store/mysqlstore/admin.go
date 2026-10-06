package mysqlstore

import (
	"context"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// filters 拼接“列 = 值”的过滤条件，值为空的跳过。
func filters(where string, args []any, pairs ...string) (string, []any) {
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			where += ` AND ` + pairs[i] + ` = ?`
			args = append(args, pairs[i+1])
		}
	}
	return where, args
}

func (s *Store) ListAccounts(ctx context.Context, q store.AccountQuery) ([]domain.Account, int, error) {
	where, args := filters(` FROM accounts WHERE deleted_at IS NULL`, nil, "role", string(q.Role), "status", string(q.Status))
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		where += ` AND (username LIKE ? OR display_name LIKE ?)`
		pattern := "%" + likeEscape.Replace(kw) + "%"
		args = append(args, pattern, pattern)
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+accountColumns+where+` ORDER BY created_at DESC, account_id DESC LIMIT ? OFFSET ?`,
		append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Account{}
	for rows.Next() {
		acc, _, err := s.scanAccount(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, acc)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateAccountStatus(ctx context.Context, accountID string, fn func(a *domain.Account) error) (domain.Account, error) {
	var out domain.Account
	err := s.WithTx(ctx, func(ctx context.Context) error {
		acc, _, err := s.scanAccount(s.q(ctx).QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE account_id = ? AND deleted_at IS NULL FOR UPDATE`, accountID))
		if err != nil {
			return err
		}
		next := acc
		if err := fn(&next); err != nil {
			return err
		}
		if err := store.CheckEntityTransition(acc.Status, next.Status); err != nil {
			return err
		}
		acc.Status, acc.UpdatedAt = next.Status, s.timestamp()
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE accounts SET status = ?, updated_at = ? WHERE account_id = ?`, acc.Status, acc.UpdatedAt, accountID); err != nil {
			return mapErr(err)
		}
		out = acc
		return nil
	})
	return out, err
}

const merchantColumns = `merchant_id, name, logo_url, description, service_phone, status, created_at, updated_at`

func scanMerchant(row rowScanner) (domain.Merchant, error) {
	var m domain.Merchant
	if err := row.Scan(&m.MerchantID, &m.Name, &m.LogoURL, &m.Description, &m.ServicePhone, &m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return domain.Merchant{}, mapErr(err)
	}
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return m, nil
}

func (s *Store) ListMerchants(ctx context.Context, q store.MerchantQuery) ([]domain.Merchant, int, error) {
	where, args := filters(` FROM merchants WHERE 1 = 1`, nil, "status", string(q.Status))
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		where += ` AND name LIKE ?`
		args = append(args, "%"+likeEscape.Replace(kw)+"%")
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+merchantColumns+where+` ORDER BY created_at DESC, merchant_id DESC LIMIT ? OFFSET ?`,
		append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Merchant{}
	for rows.Next() {
		m, err := scanMerchant(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateMerchantStatus(ctx context.Context, merchantID string, fn func(m *domain.Merchant) error) (domain.Merchant, error) {
	var out domain.Merchant
	err := s.WithTx(ctx, func(ctx context.Context) error {
		m, err := scanMerchant(s.q(ctx).QueryRowContext(ctx, `SELECT `+merchantColumns+` FROM merchants WHERE merchant_id = ? FOR UPDATE`, merchantID))
		if err != nil {
			return err
		}
		next := m
		if err := fn(&next); err != nil {
			return err
		}
		if err := store.CheckEntityTransition(m.Status, next.Status); err != nil {
			return err
		}
		m.Status, m.UpdatedAt = next.Status, s.timestamp()
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE merchants SET status = ?, updated_at = ? WHERE merchant_id = ?`, m.Status, m.UpdatedAt, merchantID); err != nil {
			return mapErr(err)
		}
		out = m
		return nil
	})
	return out, err
}

func (s *Store) ListAllProducts(ctx context.Context, q store.AdminProductQuery) ([]store.CatalogProduct, int, error) {
	where, args := filters(` FROM products p LEFT JOIN merchants m ON m.merchant_id = p.merchant_id WHERE 1 = 1`, nil,
		"p.merchant_id", q.MerchantID, "p.status", string(q.Status))
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		where += ` AND p.name LIKE ?`
		args = append(args, "%"+likeEscape.Replace(kw)+"%")
	}
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+prefixed("p.", productColumns)+`, COALESCE(m.name, '')`+where+
		` ORDER BY p.updated_at DESC, p.product_id DESC LIMIT ? OFFSET ?`, append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []store.CatalogProduct{}
	for rows.Next() {
		var cp store.CatalogProduct
		if cp.Product, err = scanProduct(rows, &cp.MerchantName); err != nil {
			return nil, 0, err
		}
		out = append(out, cp)
	}
	return out, total, rows.Err()
}

func (s *Store) ListAllReviews(ctx context.Context, q store.AdminReviewQuery) ([]store.MerchantReview, int, error) {
	where, args := filters(` WHERE 1 = 1`, nil, "r.status", q.Status, "r.product_id", q.ProductID)
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

func (s *Store) UpdateReviewStatus(ctx context.Context, reviewID string, fn func(r *domain.ProductReview) error) (store.MerchantReview, error) {
	var out store.MerchantReview
	err := s.WithTx(ctx, func(ctx context.Context) error {
		r, err := scanMerchantReview(s.q(ctx).QueryRowContext(ctx, merchantReviewSelect+` WHERE r.review_id = ? FOR UPDATE OF r`, reviewID))
		if err != nil {
			return err
		}
		next := r.ProductReview
		if err := fn(&next); err != nil {
			return err
		}
		if err := store.CheckReviewTransition(r.Status, next.Status); err != nil {
			return err
		}
		r.Status, r.UpdatedAt = next.Status, s.timestamp()
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE product_reviews SET status = ?, updated_at = ? WHERE review_id = ?`, r.Status, r.UpdatedAt, reviewID); err != nil {
			return mapErr(err)
		}
		out = r
		return nil
	})
	return out, err
}

func (s *Store) CountStatuses(ctx context.Context) (store.StatusOverview, error) {
	out := store.StatusOverview{}
	for _, c := range []struct {
		dst   *map[string]int
		query string
	}{
		{&out.Accounts, `SELECT status, COUNT(*) FROM accounts WHERE deleted_at IS NULL GROUP BY status`},
		{&out.Merchants, `SELECT status, COUNT(*) FROM merchants GROUP BY status`},
		{&out.Products, `SELECT status, COUNT(*) FROM products WHERE status <> 'deleted' GROUP BY status`},
	} {
		rows, err := s.q(ctx).QueryContext(ctx, c.query)
		if err != nil {
			return store.StatusOverview{}, mapErr(err)
		}
		counts := map[string]int{}
		for rows.Next() {
			var status string
			var n int
			if err := rows.Scan(&status, &n); err != nil {
				rows.Close()
				return store.StatusOverview{}, err
			}
			counts[status] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return store.StatusOverview{}, err
		}
		*c.dst = counts
	}
	return out, nil
}

const auditColumns = `audit_id, operator_id, operator_name, action, target_type, target_id, before_value, after_value, reason, request_id, created_at`

func (s *Store) InsertAuditLog(ctx context.Context, l store.AuditLog) (store.AuditLog, error) {
	if l.AuditID == "" {
		l.AuditID = domain.NewID(domain.PrefixAudit)
	}
	l.CreatedAt = s.orNow(l.CreatedAt)
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO admin_audit_logs (`+auditColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.AuditID, l.OperatorID, l.OperatorName, l.Action, l.TargetType, l.TargetID, l.BeforeValue, l.AfterValue, l.Reason, l.RequestID, l.CreatedAt)
	return l, mapErr(err)
}

func (s *Store) ListAuditLogs(ctx context.Context, q store.AuditQuery) ([]store.AuditLog, int, error) {
	where, args := filters(` FROM admin_audit_logs WHERE 1 = 1`, nil, "target_type", q.TargetType, "target_id", q.TargetID, "operator_id", q.OperatorID)
	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+auditColumns+where+` ORDER BY seq DESC LIMIT ? OFFSET ?`,
		append(args, q.Page.PageSize, q.Page.Offset())...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []store.AuditLog{}
	for rows.Next() {
		var l store.AuditLog
		var created time.Time
		if err := rows.Scan(&l.AuditID, &l.OperatorID, &l.OperatorName, &l.Action, &l.TargetType, &l.TargetID, &l.BeforeValue, &l.AfterValue,
			&l.Reason, &l.RequestID, &created); err != nil {
			return nil, 0, err
		}
		l.CreatedAt = created.UTC()
		out = append(out, l)
	}
	return out, total, rows.Err()
}
