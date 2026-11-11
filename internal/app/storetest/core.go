package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	platformblob "github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// runCore covers transactions and pagination input, which every other
// concern builds on.
func runCore(t *testing.T, newStore Factory) {
	t.Run("TxCommit", func(t *testing.T) { testTxCommit(t, newStore(t)) })
	t.Run("TxRollback", func(t *testing.T) { testTxRollback(t, newStore(t)) })
	t.Run("TxNested", func(t *testing.T) { testTxNested(t, newStore(t)) })
	t.Run("Cancelled", func(t *testing.T) { testCancelled(t, newStore(t)) })
	t.Run("BadCursor", func(t *testing.T) { testBadCursor(t, newStore(t)) })
}

// blob builds the index record of fixture content i.
func blob(i int) platformblob.Blob {
	return platformblob.Blob{
		Checksums: kernel.Checksums{
			SHA256: fmt.Sprintf("%064x", i),
			SHA512: fmt.Sprintf("%0128x", i),
			SHA1:   fmt.Sprintf("%040x", i),
			MD5:    fmt.Sprintf("%032x", i),
		},
		Size:    int64(100 * i),
		Created: storekit.At(i),
	}
}

var errBoom = errors.New("boom")

func testTxCommit(t *testing.T, s Store) {
	ctx := context.Background()
	committed := false
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Blobs().Ref(ctx, blob(1), 1); err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
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
	got := storekit.Get[platformblob.Blob](t)(s.Blobs().Get(ctx, blob(1).Checksums.SHA256))
	if got.Refs != 1 {
		t.Fatalf("refs = %d", got.Refs)
	}
	ran := false
	s.AfterCommit(ctx, func() { ran = true })
	if !ran {
		t.Fatal("AfterCommit outside a transaction must run at once")
	}
}

func testTxRollback(t *testing.T, s Store) {
	ctx := context.Background()
	storekit.Get[platformblob.Blob](t)(s.Blobs().Ref(ctx, blob(1), 1))
	notified := false
	err := s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Blobs().Ref(ctx, blob(2), 1); err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}
		if _, err := s.Blobs().Ref(ctx, blob(1), 5); err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}
		s.AfterCommit(ctx, func() { notified = true })
		return errBoom
	})
	storekit.WantErr(t, err, errBoom)
	if notified {
		t.Fatal("AfterCommit ran after a rollback")
	}
	_, err = s.Blobs().Get(ctx, blob(2).Checksums.SHA256)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	if got := storekit.Get[platformblob.Blob](
		t,
	)(
		s.Blobs().Get(ctx, blob(1).Checksums.SHA256),
	); got.Refs != 1 {
		t.Fatalf("refs after rollback = %d, want 1", got.Refs)
	}
}

func testTxNested(t *testing.T, s Store) {
	ctx := context.Background()
	// The outer function returns the inner error, and both store families
	// then roll the whole transaction back. When the outer function drops
	// it, they differ on purpose (the SQL stores fail the outer transaction,
	// memtx commits it), so each unit of work checks that case itself:
	// TestAccTxDroppedInnerErrorFailsOuter in the dialect package and
	// TestUnitTxNestedJoinsOuter in memtx.
	err := s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Blobs().Ref(ctx, blob(1), 1); err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}
		return s.Tx(ctx, func(ctx context.Context) error {
			if _, err := s.Blobs().Ref(ctx, blob(2), 1); err != nil {
				return err //nolint:wrapcheck // the transaction returns the store's own error
			}
			return errBoom
		})
	})
	storekit.WantErr(t, err, errBoom)
	for _, i := range []int{1, 2} {
		_, err := s.Blobs().Get(ctx, blob(i).Checksums.SHA256)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		return s.Tx(ctx, func(ctx context.Context) error {
			_, err := s.Blobs().Ref(ctx, blob(3), 1)
			return err //nolint:wrapcheck // the transaction returns the store's own error
		})
	}))
	storekit.Get[platformblob.Blob](t)(s.Blobs().Get(ctx, blob(3).Checksums.SHA256))
}

func testCancelled(t *testing.T, s Store) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Blobs().Ref(ctx, blob(1), 1); err == nil {
		t.Fatal("Ref on a cancelled context succeeded")
	}
	if err := s.Tx(ctx, func(context.Context) error { return nil }); err == nil {
		t.Fatal("Tx on a cancelled context succeeded")
	}
}

func testBadCursor(t *testing.T, s Store) {
	ctx := context.Background()
	storekit.Get[platformblob.Blob](t)(s.Blobs().Ref(ctx, blob(1), 0))
	_, _, err := s.Blobs().Unreferenced(ctx, kernel.Page{After: "%%%"})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	_, _, err = s.Blobs().Unreferenced(ctx, kernel.Page{After: kernel.Cursor("a", "b")})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
}
