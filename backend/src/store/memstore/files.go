package memstore

import (
	"context"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func (s *Store) CreateStoredFile(ctx context.Context, f domain.StoredFile) (domain.StoredFile, error) {
	if err := store.ValidateStoredFile(f); err != nil {
		return domain.StoredFile{}, err
	}
	defer s.lock(ctx)()
	if f.FileID == "" {
		f.FileID = domain.NewID(domain.PrefixFile)
	}
	if _, ok := s.data.files[f.FileID]; ok {
		return domain.StoredFile{}, &store.ConflictError{Key: store.KeyPrimary}
	}
	for _, other := range s.data.files {
		if other.ObjectKey == f.ObjectKey {
			return domain.StoredFile{}, &store.ConflictError{Key: store.KeyStoredFileObjectKey}
		}
	}
	f.CreatedAt = s.orNow(f.CreatedAt)
	s.data.files[f.FileID] = f
	return f, nil
}

func (s *Store) GetStoredFile(ctx context.Context, fileID string) (domain.StoredFile, error) {
	defer s.lock(ctx)()
	f, ok := s.data.files[fileID]
	if !ok {
		return domain.StoredFile{}, store.ErrNotFound
	}
	return f, nil
}
