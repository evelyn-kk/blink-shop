package mysqlstore

import (
	"context"
	"database/sql"

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
