package inmem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// Blobs returns the blob index over the blobs table: content-addressed,
// reference-counted rows (decisions 0004 and 0010).
func (s *Store) Blobs() port.BlobIndex { return blobIndex{s: s, table: "blobs"} }

// blobIndex is a BlobIndex over one table, shared by the hot tier (blobs)
// and the archive tier (cold_blobs).
type blobIndex struct {
	s     *Store
	table string
}

func (b blobIndex) t() *memtx.Table[string, blob.Blob] {
	return memtx.TableOf[string, blob.Blob](b.s.Store, b.table)
}

func (b blobIndex) Ref(ctx context.Context, row blob.Blob, n int64) (blob.Blob, error) {
	var out blob.Blob
	err := b.s.Do(ctx, func() error {
		if row.Checksums.SHA256 == "" || n < 0 {
			return fmt.Errorf(
				"%s: sha256 and a non-negative count are required: %w",
				b.table,
				kernel.ErrInvalid,
			)
		}
		cur, ok := b.t().Get(row.Checksums.SHA256)
		switch {
		case !ok:
			cur = row
			cur.Refs, cur.State, cur.Deleted = n, blob.BlobLive, time.Time{}
		case cur.State == blob.BlobDeleting:
			return fmt.Errorf("%s %s: %w", b.table, row.Checksums.SHA256, port.ErrDeleting)
		case !sameContent(cur, row):
			return memtx.Conflict(b.table+" sha256", row.Checksums.SHA256)
		case cur.State == blob.BlobDeleted:
			// Revived by content written again: the missing digests are
			// filled in and Deleted stays, as on the SQL stores.
			cur.State, cur.Refs, cur.Created = blob.BlobLive, n, row.Created
			cur.Checksums.SHA512 = cmpOr(cur.Checksums.SHA512, row.Checksums.SHA512)
			cur.Checksums.SHA1 = cmpOr(cur.Checksums.SHA1, row.Checksums.SHA1)
			cur.Checksums.MD5 = cmpOr(cur.Checksums.MD5, row.Checksums.MD5)
		default:
			cur.Refs += n
		}
		b.t().Put(cur.Checksums.SHA256, cur)
		out, _ = b.t().Get(cur.Checksums.SHA256)
		return nil
	})
	return out, err
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// sameContent reports whether a stored blob and a new reference describe the
// same bytes. A digest is compared only when both record it.
func sameContent(stored, b blob.Blob) bool {
	if stored.Size != b.Size {
		return false
	}
	differ := func(x, y string) bool { return x != "" && y != "" && !strings.EqualFold(x, y) }
	return !differ(stored.Checksums.SHA512, b.Checksums.SHA512) &&
		!differ(stored.Checksums.SHA1, b.Checksums.SHA1) &&
		!differ(stored.Checksums.MD5, b.Checksums.MD5)
}

func (b blobIndex) Unref(ctx context.Context, sha256 string, n int64) (blob.Blob, error) {
	var out blob.Blob
	err := b.s.Do(ctx, func() error {
		cur, ok := b.t().Get(sha256)
		if !ok {
			return memtx.NotFound(b.table, sha256)
		}
		cur.Refs = max(0, cur.Refs-n)
		b.t().Put(sha256, cur)
		out = cur
		return nil
	})
	return out, err
}

func (b blobIndex) Get(ctx context.Context, sha256 string) (blob.Blob, error) {
	var out blob.Blob
	err := b.s.Do(ctx, func() error {
		var ok bool
		if out, ok = b.t().Get(sha256); !ok {
			return memtx.NotFound(b.table, sha256)
		}
		return nil
	})
	return out, err
}

func (b blobIndex) Find(ctx context.Context, c kernel.Checksums) (blob.Blob, error) {
	var out blob.Blob
	err := b.s.Do(ctx, func() error {
		match, what, err := strongest(c)
		if err != nil {
			return err
		}
		rows := b.t().All(func(_ string, v blob.Blob) bool {
			return v.State == blob.BlobLive && match(v.Checksums)
		}, compareBlobs)
		if len(rows) == 0 {
			return memtx.NotFound(b.table+" "+what, c)
		}
		out = rows[0]
		return nil
	})
	return out, err
}

// strongest returns a matcher for the strongest digest set in c.
func strongest(c kernel.Checksums) (func(kernel.Checksums) bool, string, error) {
	switch {
	case c.SHA256 != "":
		return func(v kernel.Checksums) bool { return v.SHA256 == c.SHA256 }, "sha256", nil
	case c.SHA1 != "":
		return func(v kernel.Checksums) bool { return v.SHA1 == c.SHA1 }, "sha1", nil
	case c.MD5 != "":
		return func(v kernel.Checksums) bool { return v.MD5 == c.MD5 }, "md5", nil
	default:
		return nil, "", fmt.Errorf("no checksum given: %w", kernel.ErrInvalid)
	}
}

func compareBlobs(a, b blob.Blob) int {
	return strings.Compare(a.Checksums.SHA256, b.Checksums.SHA256)
}

// list pages the rows keep accepts, ordered by sha256.
func (b blobIndex) list(
	ctx context.Context,
	p kernel.Page,
	keep func(blob.Blob) bool,
) ([]blob.Blob, string, error) {
	var (
		out  []blob.Blob
		next string
	)
	err := b.s.Do(ctx, func() error {
		rows := b.t().All(func(_ string, v blob.Blob) bool { return keep(v) }, compareBlobs)
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(v blob.Blob) []any { return []any{v.Checksums.SHA256} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (b blobIndex) Unreferenced(ctx context.Context, p kernel.Page) ([]blob.Blob, string, error) {
	return b.list(
		ctx,
		p,
		func(v blob.Blob) bool { return v.State == blob.BlobLive && v.Refs == 0 },
	)
}

func (b blobIndex) Deleting(ctx context.Context, p kernel.Page) ([]blob.Blob, string, error) {
	return b.list(ctx, p, func(v blob.Blob) bool { return v.State == blob.BlobDeleting })
}

func (b blobIndex) MarkDeleting(
	ctx context.Context,
	row blob.Blob,
	at time.Time,
) (blob.Blob, error) {
	var out blob.Blob
	err := b.s.Do(ctx, func() error {
		sha := row.Checksums.SHA256
		if sha == "" {
			return fmt.Errorf("%s: sha256 is required: %w", b.table, kernel.ErrInvalid)
		}
		cur, ok := b.t().Get(sha)
		switch {
		case !ok:
			cur = row
			cur.Refs = 0
		case cur.State == blob.BlobDeleting:
			out = cur
			return nil
		case cur.Refs > 0:
			return memtx.Conflict(b.table+" still referenced", sha)
		}
		cur.State, cur.Deleted = blob.BlobDeleting, at
		b.t().Put(sha, cur)
		out, _ = b.t().Get(sha)
		return nil
	})
	return out, err
}

func (b blobIndex) FinishDelete(ctx context.Context, sha256 string, at time.Time) error {
	return b.s.Do(ctx, func() error {
		cur, ok := b.t().Get(sha256)
		switch {
		case !ok:
			return memtx.NotFound(b.table, sha256)
		case cur.State == blob.BlobDeleted:
			return nil
		case cur.State != blob.BlobDeleting:
			return memtx.Conflict(b.table+" is live", sha256)
		}
		cur.State, cur.Deleted = blob.BlobDeleted, at
		b.t().Put(sha256, cur)
		return nil
	})
}

func (b blobIndex) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := b.s.Do(ctx, func() error {
		for sha, v := range b.t().Rows {
			switch {
			case v.Deleted.IsZero() || !v.Deleted.Before(before):
			case v.State == blob.BlobDeleted:
				b.t().Del(sha)
				n++
			case v.State == blob.BlobLive:
				v.Deleted = time.Time{}
				b.t().Put(sha, v)
				n++
			}
		}
		return nil
	})
	return n, err
}

func (b blobIndex) Totals(ctx context.Context) (count, bytes int64, err error) {
	err = b.s.Do(ctx, func() error {
		for _, v := range b.t().Rows {
			if v.State == blob.BlobLive {
				count++
				bytes += v.Size
			}
		}
		return nil
	})
	return count, bytes, err
}
