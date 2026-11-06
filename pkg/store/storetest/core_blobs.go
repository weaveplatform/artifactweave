package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runBlobs covers the blob index of both tiers (decisions 0004 and 0024).
func runBlobs(t *testing.T, newStore Factory) {
	t.Run("Blobs", func(t *testing.T) { testBlobs(t, newStore(t).Blobs()) })
	t.Run("ColdBlobs", func(t *testing.T) { testBlobs(t, newStore(t).Archive().ColdBlobs()) })
	t.Run("BlobDeletion", func(t *testing.T) { testBlobDeletion(t, newStore(t).Blobs()) })
	t.Run("ColdBlobDeletion", func(t *testing.T) {
		testBlobDeletion(t, newStore(t).Archive().ColdBlobs())
	})
	t.Run("BlobTiersSeparate", func(t *testing.T) { testBlobTiers(t, newStore(t)) })
}

func testBlobs(t *testing.T, b store.BlobIndex) {
	ctx := context.Background()
	one := blob(1)
	got := get[types.Blob](t)(b.Ref(ctx, one, 1))
	wantOne := one
	wantOne.Refs, wantOne.State = 1, types.BlobLive
	same(t, "Ref", wantOne, got)

	// A second reference with only sha256 and size known is the same content.
	partial := types.Blob{Checksums: types.Checksums{SHA256: one.Checksums.SHA256}, Size: one.Size}
	if got := get[types.Blob](
		t,
	)(
		b.Ref(ctx, partial, 2),
	); got.Refs != 3 ||
		got.Checksums.SHA512 != one.Checksums.SHA512 ||
		got.Checksums.SHA1 != one.Checksums.SHA1 {
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
	bad = one
	bad.Checksums.SHA512 = fmt.Sprintf("%0128x", 99)
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
	_, err = b.MarkDeleting(ctx, one, at(50))
	wantErr(t, err, store.ErrConflict)
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
	unref := pages(
		t,
		2,
		func(p store.Page) ([]types.Blob, string, error) { return b.Unreferenced(ctx, p) },
	)
	var shas []string
	for _, u := range unref {
		shas = append(shas, u.Checksums.SHA256)
	}
	wantShas := []string{
		blob(1).Checksums.SHA256,
		blob(3).Checksums.SHA256,
		blob(5).Checksums.SHA256,
	}
	same(t, "Unreferenced", wantShas, shas)
}

// testBlobDeletion walks a blob through garbage collection's three steps
// (decision 0010): marked deleting, its content removed, a tombstone, and
// revived by content written again.
func testBlobDeletion(t *testing.T, b store.BlobIndex) {
	ctx := context.Background()
	one, sha := blob(1), blob(1).Checksums.SHA256
	get[types.Blob](t)(b.Ref(ctx, one, 1))
	get[types.Blob](t)(b.Unref(ctx, sha, 1))

	marked := get[types.Blob](t)(b.MarkDeleting(ctx, one, at(10)))
	if marked.State != types.BlobDeleting || !marked.Deleted.Equal(want(at(10))) ||
		marked.Refs != 0 {
		t.Fatalf("MarkDeleting = %+v", marked)
	}
	// Marking again resumes rather than restarting the clock.
	if again := get[types.Blob](
		t,
	)(
		b.MarkDeleting(ctx, one, at(11)),
	); !again.Deleted.Equal(
		want(at(10)),
	) {
		t.Fatalf("second MarkDeleting = %+v", again)
	}
	// A deleting blob refuses references and serves nothing.
	_, err := b.Ref(ctx, one, 1)
	wantErr(t, err, store.ErrDeleting)
	_, err = b.Find(ctx, types.Checksums{SHA1: one.Checksums.SHA1})
	wantErr(t, err, store.ErrNotFound)
	if rows, _, err := b.Unreferenced(ctx, store.Page{}); err != nil || len(rows) != 0 {
		t.Fatalf("Unreferenced lists a deleting blob: %v %v", rows, err)
	}
	if count, _, err := b.Totals(ctx); err != nil || count != 0 {
		t.Fatalf("Totals counts a deleting blob: %d %v", count, err)
	}
	deleting := pages(
		t,
		1,
		func(p store.Page) ([]types.Blob, string, error) { return b.Deleting(ctx, p) },
	)
	if len(deleting) != 1 || deleting[0].Checksums.SHA256 != sha {
		t.Fatalf("Deleting = %+v", deleting)
	}

	must(t, b.FinishDelete(ctx, sha, at(12)))
	must(t, b.FinishDelete(ctx, sha, at(13)))
	tomb := get[types.Blob](t)(b.Get(ctx, sha))
	if tomb.State != types.BlobDeleted || !tomb.Deleted.Equal(want(at(12))) {
		t.Fatalf("tombstone = %+v", tomb)
	}
	if rows, _, err := b.Deleting(ctx, store.Page{}); err != nil || len(rows) != 0 {
		t.Fatalf("Deleting after FinishDelete = %v %v", rows, err)
	}
	wantErr(t, b.FinishDelete(ctx, blob(9).Checksums.SHA256, at(12)), store.ErrNotFound)

	// Content written again revives the row and keeps when it was deleted,
	// so the deploy knows to check the content is there.
	revived := get[types.Blob](t)(b.Ref(ctx, one, 1))
	if revived.State != types.BlobLive || revived.Refs != 1 ||
		!revived.Deleted.Equal(want(at(12))) {
		t.Fatalf("revived = %+v", revived)
	}
	wantErr(t, b.FinishDelete(ctx, sha, at(14)), store.ErrConflict)

	// Content the index never recorded gets a deleting row from what the
	// collector knows, and a later reference fills in the missing digests.
	orphan := types.Blob{
		Checksums: types.Checksums{SHA256: blob(2).Checksums.SHA256},
		Size:      blob(2).Size,
	}
	if got := get[types.Blob](
		t,
	)(
		b.MarkDeleting(ctx, orphan, at(20)),
	); got.State != types.BlobDeleting {
		t.Fatalf("orphan MarkDeleting = %+v", got)
	}
	must(t, b.FinishDelete(ctx, orphan.Checksums.SHA256, at(21)))
	full := get[types.Blob](t)(b.Ref(ctx, blob(2), 1))
	if full.Checksums.SHA512 != blob(2).Checksums.SHA512 ||
		full.Checksums.SHA1 != blob(2).Checksums.SHA1 ||
		full.Checksums.MD5 != blob(2).Checksums.MD5 {
		t.Fatalf("revived orphan digests = %+v", full.Checksums)
	}
	if got := get[types.Blob](
		t,
	)(
		b.Find(ctx, types.Checksums{MD5: blob(2).Checksums.MD5}),
	); got.Refs != 1 {
		t.Fatalf("Find revived orphan = %+v", got)
	}

	// Prune drops old tombstones and clears old deletion times on live rows.
	get[types.Blob](t)(b.Ref(ctx, blob(3), 0))
	get[types.Blob](t)(b.MarkDeleting(ctx, blob(3), at(30)))
	must(t, b.FinishDelete(ctx, blob(3).Checksums.SHA256, at(31)))
	n, err := b.Prune(ctx, at(25))
	must(t, err)
	if n != 2 {
		t.Fatalf("Prune changed %d rows, want the two live rows deleted before then", n)
	}
	if got := get[types.Blob](t)(b.Get(ctx, sha)); !got.Deleted.IsZero() {
		t.Fatalf("Prune kept Deleted on a live row: %+v", got)
	}
	if got := get[types.Blob](
		t,
	)(
		b.Get(ctx, blob(3).Checksums.SHA256),
	); got.State != types.BlobDeleted {
		t.Fatalf("Prune dropped a recent tombstone: %+v", got)
	}
	n, err = b.Prune(ctx, at(40))
	must(t, err)
	_, err = b.Get(ctx, blob(3).Checksums.SHA256)
	if n != 1 || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Prune of an old tombstone: %d, %v", n, err)
	}
	_, err = b.MarkDeleting(ctx, types.Blob{}, at(50))
	wantErr(t, err, store.ErrInvalid)
}

func testBlobTiers(t *testing.T, s store.Store) {
	ctx := context.Background()
	get[types.Blob](t)(s.Blobs().Ref(ctx, blob(1), 1))
	_, err := s.Archive().ColdBlobs().Get(ctx, blob(1).Checksums.SHA256)
	wantErr(t, err, store.ErrNotFound)
}
