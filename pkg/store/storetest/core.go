package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runCore covers transactions, pagination input and the blob index, which
// every other concern builds on.
func runCore(t *testing.T, newStore Factory) {
	t.Run("TxCommit", func(t *testing.T) { testTxCommit(t, newStore(t)) })
	t.Run("TxRollback", func(t *testing.T) { testTxRollback(t, newStore(t)) })
	t.Run("TxNested", func(t *testing.T) { testTxNested(t, newStore(t)) })
	t.Run("Cancelled", func(t *testing.T) { testCancelled(t, newStore(t)) })
	t.Run("BadCursor", func(t *testing.T) { testBadCursor(t, newStore(t)) })
	t.Run("Blobs", func(t *testing.T) { testBlobs(t, newStore(t).Blobs()) })
	t.Run("ColdBlobs", func(t *testing.T) { testBlobs(t, newStore(t).Archive().ColdBlobs()) })
	t.Run("BlobTiersSeparate", func(t *testing.T) { testBlobTiers(t, newStore(t)) })
}

// blob builds the index record of fixture content i.
func blob(i int) types.Blob {
	return types.Blob{
		Checksums: types.Checksums{
			SHA256: fmt.Sprintf("%064x", i),
			SHA1:   fmt.Sprintf("%040x", i),
			MD5:    fmt.Sprintf("%032x", i),
		},
		Size:    int64(100 * i),
		Created: at(i),
	}
}

var errBoom = errors.New("boom")

func testTxCommit(t *testing.T, s store.Store) {
	ctx := context.Background()
	committed := false
	must(t, s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Blobs().Ref(ctx, blob(1), 1); err != nil {
			return err
		}
		s.AfterCommit(ctx, func() { committed = true })
		if committed {
			t.Error("AfterCommit ran before the commit")
		}
		return nil
	}))
	if !committed {
		t.Fatal("AfterCommit did not run after the commit")
	}
	got := get[types.Blob](t)(s.Blobs().Get(ctx, blob(1).Checksums.SHA256))
	if got.Refs != 1 {
		t.Fatalf("refs = %d", got.Refs)
	}
	ran := false
	s.AfterCommit(ctx, func() { ran = true })
	if !ran {
		t.Fatal("AfterCommit outside a transaction must run at once")
	}
}

func testTxRollback(t *testing.T, s store.Store) {
	ctx := context.Background()
	get[types.Blob](t)(s.Blobs().Ref(ctx, blob(1), 1))
	notified := false
	err := s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Blobs().Ref(ctx, blob(2), 1); err != nil {
			return err
		}
		if _, err := s.Blobs().Ref(ctx, blob(1), 5); err != nil {
			return err
		}
		s.AfterCommit(ctx, func() { notified = true })
		return errBoom
	})
	wantErr(t, err, errBoom)
	if notified {
		t.Fatal("AfterCommit ran after a rollback")
	}
	_, err = s.Blobs().Get(ctx, blob(2).Checksums.SHA256)
	wantErr(t, err, store.ErrNotFound)
	if got := get[types.Blob](t)(s.Blobs().Get(ctx, blob(1).Checksums.SHA256)); got.Refs != 1 {
		t.Fatalf("refs after rollback = %d, want 1", got.Refs)
	}
}

func testTxNested(t *testing.T, s store.Store) {
	ctx := context.Background()
	// An inner error the outer function drops must still roll the whole
	// transaction back on the SQL stores; inmem reaches the same outcome
	// because the outer function returns the error here.
	err := s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Blobs().Ref(ctx, blob(1), 1); err != nil {
			return err
		}
		return s.Tx(ctx, func(ctx context.Context) error {
			if _, err := s.Blobs().Ref(ctx, blob(2), 1); err != nil {
				return err
			}
			return errBoom
		})
	})
	wantErr(t, err, errBoom)
	for _, i := range []int{1, 2} {
		_, err := s.Blobs().Get(ctx, blob(i).Checksums.SHA256)
		wantErr(t, err, store.ErrNotFound)
	}
	must(t, s.Tx(ctx, func(ctx context.Context) error {
		return s.Tx(ctx, func(ctx context.Context) error {
			_, err := s.Blobs().Ref(ctx, blob(3), 1)
			return err
		})
	}))
	get[types.Blob](t)(s.Blobs().Get(ctx, blob(3).Checksums.SHA256))
}

func testCancelled(t *testing.T, s store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Blobs().Ref(ctx, blob(1), 1); err == nil {
		t.Fatal("Ref on a cancelled context succeeded")
	}
	if err := s.Tx(ctx, func(context.Context) error { return nil }); err == nil {
		t.Fatal("Tx on a cancelled context succeeded")
	}
}

func testBadCursor(t *testing.T, s store.Store) {
	ctx := context.Background()
	get[types.Blob](t)(s.Blobs().Ref(ctx, blob(1), 0))
	_, _, err := s.Blobs().Unreferenced(ctx, store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = s.Blobs().Unreferenced(ctx, store.Page{After: store.Cursor("a", "b")})
	wantErr(t, err, store.ErrInvalidCursor)
}

func testBlobs(t *testing.T, b store.BlobIndex) {
	ctx := context.Background()
	one := blob(1)
	got := get[types.Blob](t)(b.Ref(ctx, one, 1))
	wantOne := one
	wantOne.Refs = 1
	same(t, "Ref", wantOne, got)

	// A second reference with only sha256 and size known is the same content.
	partial := types.Blob{Checksums: types.Checksums{SHA256: one.Checksums.SHA256}, Size: one.Size}
	if got := get[types.Blob](t)(b.Ref(ctx, partial, 2)); got.Refs != 3 || got.Checksums.SHA1 != one.Checksums.SHA1 {
		t.Fatalf("second Ref = %+v", got)
	}
	// Different size or digests under the same sha256 is a conflict.
	bad := one
	bad.Size++
	_, err := b.Ref(ctx, bad, 1)
	wantErr(t, err, store.ErrConflict)
	bad = one
	bad.Checksums.MD5 = fmt.Sprintf("%032x", 99)
	_, err = b.Ref(ctx, bad, 1)
	wantErr(t, err, store.ErrConflict)
	_, err = b.Ref(ctx, types.Blob{Size: 1}, 1)
	wantErr(t, err, store.ErrInvalid)

	for _, c := range []types.Checksums{
		{SHA256: one.Checksums.SHA256},
		{SHA1: one.Checksums.SHA1},
		{MD5: one.Checksums.MD5},
	} {
		if got := get[types.Blob](t)(b.Find(ctx, c)); got.Checksums.SHA256 != one.Checksums.SHA256 {
			t.Fatalf("Find(%+v) = %s", c, got.Checksums.SHA256)
		}
	}
	_, err = b.Find(ctx, types.Checksums{SHA1: fmt.Sprintf("%040x", 77)})
	wantErr(t, err, store.ErrNotFound)
	_, err = b.Find(ctx, types.Checksums{})
	wantErr(t, err, store.ErrInvalid)

	// Deleting while referenced conflicts; Unref never goes below zero.
	wantErr(t, b.Delete(ctx, one.Checksums.SHA256), store.ErrConflict)
	if got := get[types.Blob](t)(b.Unref(ctx, one.Checksums.SHA256, 10)); got.Refs != 0 {
		t.Fatalf("refs after over-unref = %d", got.Refs)
	}
	_, err = b.Unref(ctx, blob(9).Checksums.SHA256, 1)
	wantErr(t, err, store.ErrNotFound)

	for i := 2; i <= 6; i++ {
		refs := int64(0)
		if i%2 == 0 {
			refs = 1
		}
		get[types.Blob](t)(b.Ref(ctx, blob(i), refs))
	}
	count, bytes, err := b.Totals(ctx)
	must(t, err)
	if count != 6 || bytes != 2100 {
		t.Fatalf("Totals = %d, %d", count, bytes)
	}
	unref := pages(t, 2, func(p store.Page) ([]types.Blob, string, error) { return b.Unreferenced(ctx, p) })
	var shas []string
	for _, u := range unref {
		shas = append(shas, u.Checksums.SHA256)
	}
	wantShas := []string{blob(1).Checksums.SHA256, blob(3).Checksums.SHA256, blob(5).Checksums.SHA256}
	same(t, "Unreferenced", wantShas, shas)

	must(t, b.Delete(ctx, one.Checksums.SHA256))
	_, err = b.Get(ctx, one.Checksums.SHA256)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, b.Delete(ctx, one.Checksums.SHA256), store.ErrNotFound)
}

func testBlobTiers(t *testing.T, s store.Store) {
	ctx := context.Background()
	get[types.Blob](t)(s.Blobs().Ref(ctx, blob(1), 1))
	_, err := s.Archive().ColdBlobs().Get(ctx, blob(1).Checksums.SHA256)
	wantErr(t, err, store.ErrNotFound)
}
