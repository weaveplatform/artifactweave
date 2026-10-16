package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Blobs implements store.Store.
func (s *Store) Blobs() store.BlobIndex { return blobIndex{s: s, table: "blobs"} }

// blobIndex is a BlobIndex over one table with the blobs layout, shared by
// the hot tier (blobs) and the archive tier (cold_blobs).
type blobIndex struct {
	s     *Store
	table string
}

const blobCols = "sha256, sha512, sha1, md5, size, created_at, ref_count, state, deleted_at"

func scanBlob(row interface{ Scan(...any) error }) (types.Blob, error) {
	var (
		b                types.Blob
		created, deleted sql.NullInt64
		state            string
	)
	if err := row.Scan(
		&b.Checksums.SHA256,
		&b.Checksums.SHA512,
		&b.Checksums.SHA1,
		&b.Checksums.MD5,
		&b.Size,
		&created,
		&b.Refs,
		&state,
		&deleted,
	); err != nil {
		return b, err //nolint:wrapcheck // wrapped by the caller
	}
	b.Created, b.Deleted, b.State = fromMicros(created), fromMicros(deleted), types.BlobState(state)
	return b, nil
}

// insertRow adds a row for blob in state unless one exists. Inserting first
// and then locking means two first references racing on PostgreSQL both end
// up updating one row, instead of one of them failing on the primary key.
func (b blobIndex) insertRow(
	ctx context.Context,
	blob types.Blob,
	state types.BlobState,
	deleted time.Time,
) error {
	_, err := b.s.exec(ctx, "insert "+b.table,
		InsertIgnore(b.table, strings.Split(blobCols, ", "), []string{"sha256"}),
		blob.Checksums.SHA256, blob.Checksums.SHA512, blob.Checksums.SHA1, blob.Checksums.MD5,
		blob.Size,
		micros(blob.Created), 0, string(state), micros(deleted))
	return err
}

func (b blobIndex) Ref(ctx context.Context, blob types.Blob, n int64) (types.Blob, error) {
	if blob.Checksums.SHA256 == "" || n < 0 {
		return types.Blob{}, fmt.Errorf(
			"%s: sha256 and a non-negative count are required: %w",
			b.table,
			store.ErrInvalid,
		)
	}
	sha := blob.Checksums.SHA256
	var out types.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.insertRow(ctx, blob, types.BlobLive, time.Time{}); err != nil {
			return err
		}
		cur, err := b.get(ctx, sha, true)
		if err != nil {
			return err
		}
		if cur.State == types.BlobDeleting {
			return fmt.Errorf("%s %s: %w", b.table, sha, store.ErrDeleting)
		}
		if !sameContent(cur, blob) {
			return fmt.Errorf("%s sha256 %s: %w", b.table, sha, store.ErrConflict)
		}
		q := "UPDATE " + b.table + " SET ref_count = ref_count + ? WHERE sha256 = ?"
		args := []any{n, sha}
		if cur.State == types.BlobDeleted {
			// Revived by content written again. The digests the tombstone
			// lacks (it may stand for content the index never recorded) are
			// filled in; deleted_at stays, telling the caller to check the
			// content is there.
			q = "UPDATE " + b.table + " SET state = ?, ref_count = ?, created_at = ?," +
				" sha512 = CASE WHEN sha512 = '' THEN ? ELSE sha512 END," +
				" sha1 = CASE WHEN sha1 = '' THEN ? ELSE sha1 END," +
				" md5 = CASE WHEN md5 = '' THEN ? ELSE md5 END WHERE sha256 = ?"
			args = []any{
				string(types.BlobLive), n, micros(blob.Created),
				blob.Checksums.SHA512, blob.Checksums.SHA1, blob.Checksums.MD5, sha,
			}
		}
		if _, err := b.s.exec(ctx, "ref "+b.table, q, args...); err != nil {
			return err
		}
		out, err = b.get(ctx, sha, false)
		return err
	})
	return out, err
}

// sameContent reports whether a stored blob and a new reference describe the
// same bytes. A digest is compared only when both record it: a reference may
// know only sha256 and size, and so may a row garbage collection made for
// content the index never recorded.
func sameContent(stored, b types.Blob) bool {
	if stored.Size != b.Size {
		return false
	}
	differ := func(x, y string) bool { return x != "" && y != "" && !strings.EqualFold(x, y) }
	return !differ(stored.Checksums.SHA512, b.Checksums.SHA512) &&
		!differ(stored.Checksums.SHA1, b.Checksums.SHA1) &&
		!differ(stored.Checksums.MD5, b.Checksums.MD5)
}

func (b blobIndex) get(ctx context.Context, sha256 string, lock bool) (types.Blob, error) {
	q := "SELECT " + blobCols + " FROM " + b.table + " WHERE sha256 = ?"
	if lock {
		q += b.s.lockClause(ctx)
	}
	out, err := scanBlob(b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(q), sha256))
	return out, b.s.wrap(fmt.Sprintf("get %s %s", b.table, sha256), err)
}

func (b blobIndex) Unref(ctx context.Context, sha256 string, n int64) (types.Blob, error) {
	var out types.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.s.execOne(ctx, "unref "+b.table+" "+sha256, "UPDATE "+b.table+
			" SET ref_count = CASE WHEN ref_count > ? THEN ref_count - ? ELSE 0 END WHERE sha256 = ?",
			n, n, sha256); err != nil {
			return err
		}
		var err error
		out, err = b.get(ctx, sha256, false)
		return err
	})
	return out, err
}

func (b blobIndex) Get(ctx context.Context, sha256 string) (types.Blob, error) {
	return b.get(ctx, sha256, false)
}

func (b blobIndex) Find(ctx context.Context, c types.Checksums) (types.Blob, error) {
	var col, val string
	switch {
	case c.SHA256 != "":
		col, val = "sha256", c.SHA256
	case c.SHA1 != "":
		col, val = "sha1", c.SHA1
	case c.MD5 != "":
		col, val = "md5", c.MD5
	default:
		return types.Blob{}, fmt.Errorf("no checksum given: %w", store.ErrInvalid)
	}
	q := "SELECT " + blobCols + " FROM " + b.table + " WHERE " + col + " = ? AND state = ?" +
		" ORDER BY sha256 LIMIT 1"
	out, err := scanBlob(
		b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(q), val, string(types.BlobLive)),
	)
	return out, b.s.wrap(fmt.Sprintf("find %s %s %s", b.table, col, val), err)
}

// list pages the rows matching where (with args), ordered by sha256.
func (b blobIndex) list(
	ctx context.Context,
	p store.Page,
	where string,
	args ...any,
) ([]types.Blob, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, kargs, err := keyset(p, []string{"sha256"}, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []types.Blob
	err = b.s.rows(ctx, "list "+b.table,
		"SELECT "+blobCols+" FROM "+b.table+" WHERE "+where+" AND "+cond+
			orderBy([]string{"sha256"}, false)+" LIMIT ?",
		append(append(args, kargs...), p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanBlob(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(v types.Blob) []string { return []string{v.Checksums.SHA256} },
	)
	return page, next, nil
}

func (b blobIndex) Unreferenced(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	return b.list(ctx, p, "state = ? AND ref_count = 0", string(types.BlobLive))
}

func (b blobIndex) Deleting(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	return b.list(ctx, p, "state = ?", string(types.BlobDeleting))
}

func (b blobIndex) MarkDeleting(
	ctx context.Context,
	blob types.Blob,
	at time.Time,
) (types.Blob, error) {
	sha := blob.Checksums.SHA256
	if sha == "" {
		return types.Blob{}, fmt.Errorf("%s: sha256 is required: %w", b.table, store.ErrInvalid)
	}
	var out types.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.insertRow(ctx, blob, types.BlobDeleting, at); err != nil {
			return err
		}
		cur, err := b.get(ctx, sha, true)
		if err != nil {
			return err
		}
		switch {
		case cur.State == types.BlobDeleting:
			out = cur
			return nil
		case cur.Refs > 0:
			return fmt.Errorf("%s %s still referenced: %w", b.table, sha, store.ErrConflict)
		}
		if _, err := b.s.exec(ctx, "mark "+b.table, "UPDATE "+b.table+
			" SET state = ?, deleted_at = ? WHERE sha256 = ?",
			string(types.BlobDeleting), micros(at), sha); err != nil {
			return err
		}
		out, err = b.get(ctx, sha, false)
		return err
	})
	return out, err
}

func (b blobIndex) FinishDelete(ctx context.Context, sha256 string, at time.Time) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		cur, err := b.get(ctx, sha256, true)
		if err != nil {
			return err
		}
		switch cur.State {
		case types.BlobDeleted:
			return nil
		case types.BlobDeleting:
			_, err := b.s.exec(ctx, "finish "+b.table, "UPDATE "+b.table+
				" SET state = ?, deleted_at = ? WHERE sha256 = ?",
				string(types.BlobDeleted), micros(at), sha256)
			return err
		default:
			return fmt.Errorf("%s %s is live: %w", b.table, sha256, store.ErrConflict)
		}
	})
}

func (b blobIndex) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		dropped, err := b.s.exec(ctx, "prune "+b.table,
			"DELETE FROM "+b.table+" WHERE state = ? AND deleted_at < ?",
			string(types.BlobDeleted), micros(before))
		if err != nil {
			return err
		}
		cleared, err := b.s.exec(ctx, "prune "+b.table,
			"UPDATE "+b.table+" SET deleted_at = NULL WHERE state = ? AND deleted_at < ?",
			string(types.BlobLive), micros(before))
		n = dropped + cleared
		return err
	})
	return n, err
}

func (b blobIndex) Totals(ctx context.Context) (count, bytes int64, err error) {
	err = b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(
		"SELECT COUNT(*), COALESCE(SUM(size), 0) FROM "+b.table+" WHERE state = ?"),
		string(types.BlobLive)).Scan(&count, &bytes)
	return count, bytes, b.s.wrap("totals "+b.table, err)
}
