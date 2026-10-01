package mysqlstore

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const accountColumns = `account_id, username, password_hash, display_name, avatar_url, phone, email,
	role, merchant_id, status, deleted_at, created_at, updated_at`

func (s *Store) CreateAccount(ctx context.Context, in store.NewAccount) (domain.Account, error) {
	if err := store.ValidateNewAccount(in); err != nil {
		return domain.Account{}, err
	}
	now := s.timestamp()
	acc := domain.Account{
		AccountID:   in.AccountID,
		Username:    in.Username,
		DisplayName: in.DisplayName,
		AvatarURL:   in.AvatarURL,
		Phone:       in.Phone,
		Email:       in.Email,
		Role:        in.Role,
		MerchantID:  in.MerchantID,
		Status:      domain.StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if acc.AccountID == "" {
		acc.AccountID = domain.NewID(domain.PrefixAccount)
	}
	if err := s.insertAccount(ctx, acc, in.PasswordHash); err != nil {
		return domain.Account{}, err
	}
	return acc, nil
}

func (s *Store) insertAccount(ctx context.Context, acc domain.Account, passwordHash string) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO accounts (`+accountColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		acc.AccountID, acc.Username, passwordHash, acc.DisplayName, acc.AvatarURL, acc.Phone, acc.Email,
		acc.Role, acc.MerchantID, acc.Status, nullTime(acc.DeletedAt), s.orNow(acc.CreatedAt), s.orNow(acc.UpdatedAt))
	return mapErr(err)
}

func (s *Store) GetAccount(ctx context.Context, accountID string) (domain.Account, error) {
	acc, _, err := s.scanAccount(s.q(ctx).QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE account_id = ? AND deleted_at IS NULL`, accountID))
	return acc, err
}

func (s *Store) GetAccountByUsername(ctx context.Context, username string) (domain.Account, string, error) {
	return s.scanAccount(s.q(ctx).QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE username = ? AND deleted_at IS NULL`, username))
}

func (s *Store) UpdatePasswordHash(ctx context.Context, accountID, passwordHash string) error {
	if passwordHash == "" {
		return store.ErrInvalid
	}
	return s.updateAccount(ctx, accountID, []string{"password_hash = ?"}, []any{passwordHash})
}

func (s *Store) UpdateAccountProfile(ctx context.Context, accountID string, in store.ProfileUpdate) (domain.Account, error) {
	var sets []string
	var args []any
	if in.DisplayName != nil {
		sets, args = append(sets, "display_name = ?"), append(args, *in.DisplayName)
	}
	if in.AvatarURL != nil {
		sets, args = append(sets, "avatar_url = ?"), append(args, *in.AvatarURL)
	}
	if err := s.updateAccount(ctx, accountID, sets, args); err != nil {
		return domain.Account{}, err
	}
	return s.GetAccount(ctx, accountID)
}

func (s *Store) UpdateAccountContact(ctx context.Context, accountID string, in store.ContactUpdate) (domain.Account, error) {
	var sets []string
	var args []any
	if in.Phone != nil {
		sets, args = append(sets, "phone = ?"), append(args, *in.Phone)
	}
	if in.Email != nil {
		sets, args = append(sets, "email = ?"), append(args, *in.Email)
	}
	if err := s.updateAccount(ctx, accountID, sets, args); err != nil {
		return domain.Account{}, err
	}
	return s.GetAccount(ctx, accountID)
}

// updateAccount 更新未软删账户的若干列。MySQL 在值未变化时 RowsAffected 为 0，
// 所以不靠它判断存在性，而是在同一事务里先锁定该行。
func (s *Store) updateAccount(ctx context.Context, accountID string, sets []string, args []any) error {
	return s.WithTx(ctx, func(ctx context.Context) error {
		var id string
		err := s.q(ctx).QueryRowContext(ctx,
			`SELECT account_id FROM accounts WHERE account_id = ? AND deleted_at IS NULL FOR UPDATE`, accountID).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		sets = append(sets, "updated_at = ?")
		args = append(args, s.timestamp(), accountID)
		_, err = s.q(ctx).ExecContext(ctx, `UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE account_id = ?`, args...)
		return mapErr(err)
	})
}

func (s *Store) SoftDeleteAccount(ctx context.Context, accountID string) error {
	return s.WithTx(ctx, func(ctx context.Context) error {
		now := s.timestamp()
		res, err := s.q(ctx).ExecContext(ctx,
			`UPDATE accounts SET deleted_at = ?, updated_at = ? WHERE account_id = ? AND deleted_at IS NULL`, now, now, accountID)
		if err != nil {
			return mapErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return store.ErrNotFound
		}
		_, err = s.q(ctx).ExecContext(ctx, `DELETE FROM auth_tokens WHERE account_id = ?`, accountID)
		return mapErr(err)
	})
}

func (s *Store) CreateAuthToken(ctx context.Context, accountID string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		return "", time.Time{}, store.ErrInvalid
	}
	token, hash := store.NewAuthToken()
	now := s.timestamp()
	expires := now.Add(ttl)
	_, err := s.q(ctx).ExecContext(ctx,
		`INSERT INTO auth_tokens (token_hash, account_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash, accountID, now, expires)
	if err != nil {
		return "", time.Time{}, mapErr(err)
	}
	return token, expires, nil
}

func (s *Store) GetAccountByToken(ctx context.Context, token string) (domain.Account, error) {
	if token == "" {
		return domain.Account{}, store.ErrNotFound
	}
	acc, _, err := s.scanAccount(s.q(ctx).QueryRowContext(ctx,
		`SELECT `+prefixed("a.", accountColumns)+` FROM auth_tokens t JOIN accounts a ON a.account_id = t.account_id
		WHERE t.token_hash = ? AND t.expires_at > ? AND a.deleted_at IS NULL`,
		store.HashAuthToken(token), s.timestamp()))
	return acc, err
}

func (s *Store) DeleteAuthToken(ctx context.Context, token string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM auth_tokens WHERE token_hash = ?`, store.HashAuthToken(token))
	return mapErr(err)
}

// prefixed 给逗号分隔的列名加表别名前缀。
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func (s *Store) scanAccount(row *sql.Row) (domain.Account, string, error) {
	var (
		acc     domain.Account
		hash    string
		deleted sql.NullTime
	)
	err := row.Scan(&acc.AccountID, &acc.Username, &hash, &acc.DisplayName, &acc.AvatarURL, &acc.Phone, &acc.Email,
		&acc.Role, &acc.MerchantID, &acc.Status, &deleted, &acc.CreatedAt, &acc.UpdatedAt)
	if err != nil {
		return domain.Account{}, "", mapErr(err)
	}
	acc.DeletedAt = timePtr(deleted)
	acc.CreatedAt, acc.UpdatedAt = acc.CreatedAt.UTC(), acc.UpdatedAt.UTC()
	return acc, hash, nil
}
