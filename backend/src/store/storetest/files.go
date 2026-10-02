package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

func fileCases() []struct {
	name string
	fn   func(t *testing.T, s store.Store)
} {
	return []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"StoredFileRoundTrip", testStoredFileRoundTrip},
		{"StoredFileRejectsIncompleteRecord", testStoredFileInvalid},
		{"StoredFileUniqueKeys", testStoredFileUnique},
		{"StoredFileRollsBackWithTx", testStoredFileRollback},
	}
}

func newStoredFile(accountID, objectKey string) domain.StoredFile {
	return domain.StoredFile{
		AccountID: accountID, ObjectKey: objectKey, MimeType: "image/png", SizeBytes: 1234,
		ContentHash: strings.Repeat("ab", 32), StorageProvider: "minio",
	}
}

func testStoredFileRoundTrip(t *testing.T, s store.Store) {
	ctx := context.Background()
	in := newStoredFile("acct_owner", "uploads/2026/10/0123456789abcdef0123456789abcdef.png")
	in.SourceURL = "https://example.com/a.png"
	created, err := s.CreateStoredFile(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.FileID, domain.PrefixFile) || len(created.FileID) != len(domain.PrefixFile)+24 {
		t.Fatalf("file_id = %q", created.FileID)
	}
	if created.CreatedAt.IsZero() || time.Since(created.CreatedAt) > time.Minute {
		t.Fatalf("created_at = %v", created.CreatedAt)
	}
	got, err := s.GetStoredFile(ctx, created.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if got != created {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, created)
	}
	_, err = s.GetStoredFile(ctx, "file_missing")
	expectNotFound(t, err, "missing file")
}

func testStoredFileInvalid(t *testing.T, s store.Store) {
	ctx := context.Background()
	base := newStoredFile("acct_owner", "uploads/k")
	for name, mutate := range map[string]func(f *domain.StoredFile){
		"no account":    func(f *domain.StoredFile) { f.AccountID = "" },
		"no key":        func(f *domain.StoredFile) { f.ObjectKey = "" },
		"no mime":       func(f *domain.StoredFile) { f.MimeType = "" },
		"empty":         func(f *domain.StoredFile) { f.SizeBytes = 0 },
		"negative size": func(f *domain.StoredFile) { f.SizeBytes = -1 },
		"short hash":    func(f *domain.StoredFile) { f.ContentHash = "abc" },
		"upper hash":    func(f *domain.StoredFile) { f.ContentHash = strings.Repeat("AB", 32) },
		"no provider":   func(f *domain.StoredFile) { f.StorageProvider = "" },
	} {
		f := base
		mutate(&f)
		f.FileID = "file_invalid_" + strings.ReplaceAll(name, " ", "_")
		if _, err := s.CreateStoredFile(ctx, f); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
		_, err := s.GetStoredFile(ctx, f.FileID)
		expectNotFound(t, err, name)
	}
}

func testStoredFileUnique(t *testing.T, s store.Store) {
	ctx := context.Background()
	first, err := s.CreateStoredFile(ctx, newStoredFile("acct_a", "uploads/same"))
	if err != nil {
		t.Fatal(err)
	}
	var conflict *store.ConflictError
	if _, err := s.CreateStoredFile(ctx, newStoredFile("acct_b", "uploads/same")); !errors.As(err, &conflict) || conflict.Key != store.KeyStoredFileObjectKey {
		t.Fatalf("duplicate object_key err = %v", err)
	}
	dup := newStoredFile("acct_b", "uploads/other")
	dup.FileID = first.FileID
	if _, err := s.CreateStoredFile(ctx, dup); !errors.As(err, &conflict) || conflict.Key != store.KeyPrimary {
		t.Fatalf("duplicate file_id err = %v", err)
	}
	// 冲突不能覆盖已有记录。
	got, err := s.GetStoredFile(ctx, first.FileID)
	if err != nil || got != first {
		t.Fatalf("after conflicts: %+v, %v", got, err)
	}
}

func testStoredFileRollback(t *testing.T, s store.Store) {
	ctx := context.Background()
	var id string
	err := s.WithTx(ctx, func(ctx context.Context) error {
		f, err := s.CreateStoredFile(ctx, newStoredFile("acct_a", "uploads/tx"))
		id = f.FileID
		if err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WithTx err = %v", err)
	}
	_, err = s.GetStoredFile(ctx, id)
	expectNotFound(t, err, "rolled back file")
}
