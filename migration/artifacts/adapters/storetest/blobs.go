package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runBlobs covers the blob index of both tiers (decisions 0004 and 0024).
func runBlobs(t *testing.T, newStore Factory) {
	t.Run("Blobs", func(t *testing.T) { testBlobs(t, newStore(t).Blobs()) })
	t.Run("ColdBlobs", func(t *testing.T) { testBlobs(t, newStore(t).ColdBlobs()) })
	t.Run("BlobDeletion", func(t *testing.T) { testBlobDeletion(t, newStore(t).Blobs()) })
	t.Run("ColdBlobDeletion", func(t *testing.T) {
		testBlobDeletion(t, newStore(t).ColdBlobs())
	})
	t.Run("BlobTiersSeparate", func(t *testing.T) { testBlobTiers(t, newStore(t)) })
}

// testBlob builds the index record of fixture content i.
func testBlob(i int) blob.Blob {
	return blob.Blob{
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

func testBlobs(t *testing.T, b port.BlobIndex) {
	ctx := context.Background()
	one := testBlob(1)
	got := storekit.Get[blob.Blob](t)(b.Ref(ctx, one, 1))
	wantOne := one
	wantOne.Refs, wantOne.State = 1, blob.BlobLive
	storekit.Same(t, "Ref", wantOne, got)

	// A second reference with only sha256 and size known is the same content.
	partial := blob.Blob{Checksums: kernel.Checksums{SHA256: one.Checksums.SHA256}, Size: one.Size}
	if got := storekit.Get[blob.Blob](
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
	storekit.WantErr(t, err, kernel.ErrConflict)
	bad = one
	bad.Checksums.MD5 = fmt.Sprintf("%032x", 99)
	_, err = b.Ref(ctx, bad, 1)
	storekit.WantErr(t, err, kernel.ErrConflict)
	bad = one
	bad.Checksums.SHA512 = fmt.Sprintf("%0128x", 99)
	_, err = b.Ref(ctx, bad, 1)
	storekit.WantErr(t, err, kernel.ErrConflict)
	_, err = b.Ref(ctx, blob.Blob{Size: 1}, 1)
	storekit.WantErr(t, err, kernel.ErrInvalid)

	for _, c := range []kernel.Checksums{
		{SHA256: one.Checksums.SHA256},
		{SHA1: one.Checksums.SHA1},
		{MD5: one.Checksums.MD5},
	} {
		if got := storekit.Get[blob.Blob](
			t,
		)(
			b.Find(ctx, c),
		); got.Checksums.SHA256 != one.Checksums.SHA256 {
			t.Fatalf("Find(%+v) = %s", c, got.Checksums.SHA256)
		}
	}
	_, err = b.Find(ctx, kernel.Checksums{SHA1: fmt.Sprintf("%040x", 77)})
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = b.Find(ctx, kernel.Checksums{})
	storekit.WantErr(t, err, kernel.ErrInvalid)

	// Deleting while referenced conflicts; Unref never goes below zero.
	_, err = b.MarkDeleting(ctx, one, storekit.At(50))
	storekit.WantErr(t, err, kernel.ErrConflict)
	if got := storekit.Get[blob.Blob](t)(b.Unref(ctx, one.Checksums.SHA256, 10)); got.Refs != 0 {
		t.Fatalf("refs after over-unref = %d", got.Refs)
	}
	_, err = b.Unref(ctx, testBlob(9).Checksums.SHA256, 1)
	storekit.WantErr(t, err, kernel.ErrNotFound)

	for i := 2; i <= 6; i++ {
		refs := int64(0)
		if i%2 == 0 {
			refs = 1
		}
		storekit.Get[blob.Blob](t)(b.Ref(ctx, testBlob(i), refs))
	}
	count, bytes, err := b.Totals(ctx)
	storekit.Must(t, err)
	if count != 6 || bytes != 2100 {
		t.Fatalf("Totals = %d, %d", count, bytes)
	}
	unref := storekit.Pages(
		t,
		2,
		func(p kernel.Page) ([]blob.Blob, string, error) { return b.Unreferenced(ctx, p) },
	)
	shas := make([]string, 0, len(unref))
	for _, u := range unref {
		shas = append(shas, u.Checksums.SHA256)
	}
	wantShas := []string{
		testBlob(1).Checksums.SHA256,
		testBlob(3).Checksums.SHA256,
		testBlob(5).Checksums.SHA256,
	}
	storekit.Same(t, "Unreferenced", wantShas, shas)
}

// testBlobDeletion walks a blob through garbage collection's three steps
// (decision 0010): marked deleting, its content removed, a tombstone, and
// revived by content written again.
func testBlobDeletion(t *testing.T, b port.BlobIndex) {
	ctx := context.Background()
	one, sha := testBlob(1), testBlob(1).Checksums.SHA256
	storekit.Get[blob.Blob](t)(b.Ref(ctx, one, 1))
	storekit.Get[blob.Blob](t)(b.Unref(ctx, sha, 1))

	marked := storekit.Get[blob.Blob](t)(b.MarkDeleting(ctx, one, storekit.At(10)))
	if marked.State != blob.BlobDeleting ||
		!marked.Deleted.Equal(storekit.Want(storekit.At(10))) ||
		marked.Refs != 0 {
		t.Fatalf("MarkDeleting = %+v", marked)
	}
	// Marking again resumes rather than restarting the clock.
	if again := storekit.Get[blob.Blob](
		t,
	)(
		b.MarkDeleting(ctx, one, storekit.At(11)),
	); !again.Deleted.Equal(
		storekit.Want(storekit.At(10)),
	) {
		t.Fatalf("second MarkDeleting = %+v", again)
	}
	// A deleting blob refuses references and serves nothing.
	_, err := b.Ref(ctx, one, 1)
	storekit.WantErr(t, err, port.ErrDeleting)
	_, err = b.Find(ctx, kernel.Checksums{SHA1: one.Checksums.SHA1})
	storekit.WantErr(t, err, kernel.ErrNotFound)
	if rows, _, err := b.Unreferenced(ctx, kernel.Page{}); err != nil || len(rows) != 0 {
		t.Fatalf("Unreferenced lists a deleting blob: %v %v", rows, err)
	}
	if count, _, err := b.Totals(ctx); err != nil || count != 0 {
		t.Fatalf("Totals counts a deleting blob: %d %v", count, err)
	}
	deleting := storekit.Pages(
		t,
		1,
		func(p kernel.Page) ([]blob.Blob, string, error) { return b.Deleting(ctx, p) },
	)
	if len(deleting) != 1 || deleting[0].Checksums.SHA256 != sha {
		t.Fatalf("Deleting = %+v", deleting)
	}

	storekit.Must(t, b.FinishDelete(ctx, sha, storekit.At(12)))
	storekit.Must(t, b.FinishDelete(ctx, sha, storekit.At(13)))
	tomb := storekit.Get[blob.Blob](t)(b.Get(ctx, sha))
	if tomb.State != blob.BlobDeleted || !tomb.Deleted.Equal(storekit.Want(storekit.At(12))) {
		t.Fatalf("tombstone = %+v", tomb)
	}
	if rows, _, err := b.Deleting(ctx, kernel.Page{}); err != nil || len(rows) != 0 {
		t.Fatalf("Deleting after FinishDelete = %v %v", rows, err)
	}
	storekit.WantErr(
		t,
		b.FinishDelete(ctx, testBlob(9).Checksums.SHA256, storekit.At(12)),
		kernel.ErrNotFound,
	)

	// Content written again revives the row and keeps when it was deleted,
	// so the deploy knows to check the content is there.
	revived := storekit.Get[blob.Blob](t)(b.Ref(ctx, one, 1))
	if revived.State != blob.BlobLive || revived.Refs != 1 ||
		!revived.Deleted.Equal(storekit.Want(storekit.At(12))) {
		t.Fatalf("revived = %+v", revived)
	}
	storekit.WantErr(t, b.FinishDelete(ctx, sha, storekit.At(14)), kernel.ErrConflict)

	// Content the index never recorded gets a deleting row from what the
	// collector knows, and a later reference fills in the missing digests.
	orphan := blob.Blob{
		Checksums: kernel.Checksums{SHA256: testBlob(2).Checksums.SHA256},
		Size:      testBlob(2).Size,
	}
	if got := storekit.Get[blob.Blob](
		t,
	)(
		b.MarkDeleting(ctx, orphan, storekit.At(20)),
	); got.State != blob.BlobDeleting {
		t.Fatalf("orphan MarkDeleting = %+v", got)
	}
	storekit.Must(t, b.FinishDelete(ctx, orphan.Checksums.SHA256, storekit.At(21)))
	full := storekit.Get[blob.Blob](t)(b.Ref(ctx, testBlob(2), 1))
	if full.Checksums.SHA512 != testBlob(2).Checksums.SHA512 ||
		full.Checksums.SHA1 != testBlob(2).Checksums.SHA1 ||
		full.Checksums.MD5 != testBlob(2).Checksums.MD5 {
		t.Fatalf("revived orphan digests = %+v", full.Checksums)
	}
	if got := storekit.Get[blob.Blob](
		t,
	)(
		b.Find(ctx, kernel.Checksums{MD5: testBlob(2).Checksums.MD5}),
	); got.Refs != 1 {
		t.Fatalf("Find revived orphan = %+v", got)
	}

	// Prune drops old tombstones and clears old deletion times on live rows.
	storekit.Get[blob.Blob](t)(b.Ref(ctx, testBlob(3), 0))
	storekit.Get[blob.Blob](t)(b.MarkDeleting(ctx, testBlob(3), storekit.At(30)))
	storekit.Must(t, b.FinishDelete(ctx, testBlob(3).Checksums.SHA256, storekit.At(31)))
	n, err := b.Prune(ctx, storekit.At(25))
	storekit.Must(t, err)
	if n != 2 {
		t.Fatalf("Prune changed %d rows, want the two live rows deleted before then", n)
	}
	if got := storekit.Get[blob.Blob](t)(b.Get(ctx, sha)); !got.Deleted.IsZero() {
		t.Fatalf("Prune kept Deleted on a live row: %+v", got)
	}
	if got := storekit.Get[blob.Blob](
		t,
	)(
		b.Get(ctx, testBlob(3).Checksums.SHA256),
	); got.State != blob.BlobDeleted {
		t.Fatalf("Prune dropped a recent tombstone: %+v", got)
	}
	n, err = b.Prune(ctx, storekit.At(40))
	storekit.Must(t, err)
	_, err = b.Get(ctx, testBlob(3).Checksums.SHA256)
	if n != 1 || !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("Prune of an old tombstone: %d, %v", n, err)
	}
	_, err = b.MarkDeleting(ctx, blob.Blob{}, storekit.At(50))
	storekit.WantErr(t, err, kernel.ErrInvalid)
}

func testBlobTiers(t *testing.T, s port.Store) {
	ctx := context.Background()
	storekit.Get[blob.Blob](t)(s.Blobs().Ref(ctx, testBlob(1), 1))
	_, err := s.ColdBlobs().Get(ctx, testBlob(1).Checksums.SHA256)
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
