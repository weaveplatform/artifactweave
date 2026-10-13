package inmem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Blobs implements store.Store.
func (s *Store) Blobs() store.BlobIndex { return blobIndex{s: s, table: "blobs"} }

// blobIndex is a BlobIndex over one table, shared by the hot tier (blobs)
// and the archive tier (cold_blobs).
type blobIndex struct {
	s     *Store
	table string
}

func (b blobIndex) t() *table[string, types.Blob] { return tableOf[string, types.Blob](b.s, b.table) }

func (b blobIndex) Ref(ctx context.Context, blob types.Blob, n int64) (types.Blob, error) {
	var out types.Blob
	err := b.s.do(ctx, func() error {
		if blob.Checksums.SHA256 == "" || n < 0 {
			return fmt.Errorf(
				"%s: sha256 and a non-negative count are required: %w",
				b.table,
				store.ErrInvalid,
			)
		}
		cur, ok := b.t().get(blob.Checksums.SHA256)
		switch {
		case !ok:
			cur = blob
			cur.Refs, cur.State, cur.Deleted = n, types.BlobLive, time.Time{}
		case cur.State == types.BlobDeleting:
			return fmt.Errorf("%s %s: %w", b.table, blob.Checksums.SHA256, store.ErrDeleting)
		case !sameContent(cur, blob):
			return conflict(b.table+" sha256", blob.Checksums.SHA256)
		case cur.State == types.BlobDeleted:
			// Revived by content written again: the missing digests are
			// filled in and Deleted stays, as on the SQL stores.
			cur.State, cur.Refs, cur.Created = types.BlobLive, n, blob.Created
			cur.Checksums.SHA1 = cmpOr(cur.Checksums.SHA1, blob.Checksums.SHA1)
			cur.Checksums.MD5 = cmpOr(cur.Checksums.MD5, blob.Checksums.MD5)
		default:
			cur.Refs += n
		}
		b.t().put(cur.Checksums.SHA256, cur)
		out, _ = b.t().get(cur.Checksums.SHA256)
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
func sameContent(stored, b types.Blob) bool {
	if stored.Size != b.Size {
		return false
	}
	differ := func(x, y string) bool { return x != "" && y != "" && !strings.EqualFold(x, y) }
	return !differ(stored.Checksums.SHA1, b.Checksums.SHA1) &&
		!differ(stored.Checksums.MD5, b.Checksums.MD5)
}

func (b blobIndex) Unref(ctx context.Context, sha256 string, n int64) (types.Blob, error) {
	var out types.Blob
	err := b.s.do(ctx, func() error {
		cur, ok := b.t().get(sha256)
		if !ok {
			return notFound(b.table, sha256)
		}
		cur.Refs = max(0, cur.Refs-n)
		b.t().put(sha256, cur)
		out = cur
		return nil
	})
	return out, err
}

func (b blobIndex) Get(ctx context.Context, sha256 string) (types.Blob, error) {
	var out types.Blob
	err := b.s.do(ctx, func() error {
		var ok bool
		if out, ok = b.t().get(sha256); !ok {
			return notFound(b.table, sha256)
		}
		return nil
	})
	return out, err
}

func (b blobIndex) Find(ctx context.Context, c types.Checksums) (types.Blob, error) {
	var out types.Blob
	err := b.s.do(ctx, func() error {
		match, what, err := strongest(c)
		if err != nil {
			return err
		}
		rows := b.t().all(func(_ string, v types.Blob) bool {
			return v.State == types.BlobLive && match(v.Checksums)
		}, compareBlobs)
		if len(rows) == 0 {
			return notFound(b.table+" "+what, c)
		}
		out = rows[0]
		return nil
	})
	return out, err
}

// strongest returns a matcher for the strongest digest set in c.
func strongest(c types.Checksums) (func(types.Checksums) bool, string, error) {
	switch {
	case c.SHA256 != "":
		return func(v types.Checksums) bool { return v.SHA256 == c.SHA256 }, "sha256", nil
	case c.SHA1 != "":
		return func(v types.Checksums) bool { return v.SHA1 == c.SHA1 }, "sha1", nil
	case c.MD5 != "":
		return func(v types.Checksums) bool { return v.MD5 == c.MD5 }, "md5", nil
	default:
		return nil, "", fmt.Errorf("no checksum given: %w", store.ErrInvalid)
	}
}

func compareBlobs(a, b types.Blob) int {
	return strings.Compare(a.Checksums.SHA256, b.Checksums.SHA256)
}

// list pages the rows keep accepts, ordered by sha256.
func (b blobIndex) list(
	ctx context.Context,
	p store.Page,
	keep func(types.Blob) bool,
) ([]types.Blob, string, error) {
	var (
		out  []types.Blob
		next string
	)
	err := b.s.do(ctx, func() error {
		rows := b.t().all(func(_ string, v types.Blob) bool { return keep(v) }, compareBlobs)
		var err error
		out, next, err = paginate(
			rows,
			p,
			false,
			func(v types.Blob) []any { return []any{v.Checksums.SHA256} },
		)
		return err
	})
	return out, next, err
}

func (b blobIndex) Unreferenced(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	return b.list(
		ctx,
		p,
		func(v types.Blob) bool { return v.State == types.BlobLive && v.Refs == 0 },
	)
}

func (b blobIndex) Deleting(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	return b.list(ctx, p, func(v types.Blob) bool { return v.State == types.BlobDeleting })
}

func (b blobIndex) MarkDeleting(
	ctx context.Context,
	blob types.Blob,
	at time.Time,
) (types.Blob, error) {
	var out types.Blob
	err := b.s.do(ctx, func() error {
		sha := blob.Checksums.SHA256
		if sha == "" {
			return fmt.Errorf("%s: sha256 is required: %w", b.table, store.ErrInvalid)
		}
		cur, ok := b.t().get(sha)
		switch {
		case !ok:
			cur = blob
			cur.Refs = 0
		case cur.State == types.BlobDeleting:
			out = cur
			return nil
		case cur.Refs > 0:
			return conflict(b.table+" still referenced", sha)
		}
		cur.State, cur.Deleted = types.BlobDeleting, at
		b.t().put(sha, cur)
		out, _ = b.t().get(sha)
		return nil
	})
	return out, err
}

func (b blobIndex) FinishDelete(ctx context.Context, sha256 string, at time.Time) error {
	return b.s.do(ctx, func() error {
		cur, ok := b.t().get(sha256)
		switch {
		case !ok:
			return notFound(b.table, sha256)
		case cur.State == types.BlobDeleted:
			return nil
		case cur.State != types.BlobDeleting:
			return conflict(b.table+" is live", sha256)
		}
		cur.State, cur.Deleted = types.BlobDeleted, at
		b.t().put(sha256, cur)
		return nil
	})
}

func (b blobIndex) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := b.s.do(ctx, func() error {
		for sha, v := range b.t().rows {
			switch {
			case v.Deleted.IsZero() || !v.Deleted.Before(before):
			case v.State == types.BlobDeleted:
				b.t().del(sha)
				n++
			case v.State == types.BlobLive:
				v.Deleted = time.Time{}
				b.t().put(sha, v)
				n++
			}
		}
		return nil
	})
	return n, err
}

func (b blobIndex) Totals(ctx context.Context) (count, bytes int64, err error) {
	err = b.s.do(ctx, func() error {
		for _, v := range b.t().rows {
			if v.State == types.BlobLive {
				count++
				bytes += v.Size
			}
		}
		return nil
	})
	return count, bytes, err
}
