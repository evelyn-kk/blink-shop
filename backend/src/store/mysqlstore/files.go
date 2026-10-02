package mysqlstore

import (
	"context"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

const storedFileColumns = `file_id, account_id, object_key, mime_type, size_bytes, content_hash, storage_provider, source_url, created_at`

func (s *Store) CreateStoredFile(ctx context.Context, f domain.StoredFile) (domain.StoredFile, error) {
	if err := store.ValidateStoredFile(f); err != nil {
		return domain.StoredFile{}, err
	}
	if f.FileID == "" {
		f.FileID = domain.NewID(domain.PrefixFile)
	}
	f.CreatedAt = s.orNow(f.CreatedAt)
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO stored_files (`+storedFileColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.FileID, f.AccountID, f.ObjectKey, f.MimeType, f.SizeBytes, f.ContentHash, f.StorageProvider, f.SourceURL, f.CreatedAt)
	if err := mapErr(err); err != nil {
		return domain.StoredFile{}, err
	}
	return f, nil
}

func (s *Store) GetStoredFile(ctx context.Context, fileID string) (domain.StoredFile, error) {
	var f domain.StoredFile
	err := s.q(ctx).QueryRowContext(ctx, `SELECT `+storedFileColumns+` FROM stored_files WHERE file_id = ?`, fileID).Scan(
		&f.FileID, &f.AccountID, &f.ObjectKey, &f.MimeType, &f.SizeBytes, &f.ContentHash, &f.StorageProvider, &f.SourceURL, &f.CreatedAt)
	if err != nil {
		return domain.StoredFile{}, mapErr(err)
	}
	f.CreatedAt = f.CreatedAt.UTC()
	return f, nil
}
