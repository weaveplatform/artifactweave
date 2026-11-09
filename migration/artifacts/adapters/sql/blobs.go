package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Blobs returns the blob index over the blobs table: content-addressed,
// reference-counted rows (decisions 0004 and 0010).
func (s *Store) Blobs() port.BlobIndex { return blobIndex{s: s, table: "blobs"} }

// blobIndex is a BlobIndex over one table with the blobs layout, shared by
// the hot tier (blobs) and the archive tier (cold_blobs).
type blobIndex struct {
	s     *Store
	table string
}

const blobCols = "sha256, sha512, sha1, md5, size, created_at, ref_count, state, deleted_at"

func scanBlob(row interface{ Scan(...any) error }) (blob.Blob, error) {
	var (
		b                blob.Blob
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
	b.Created, b.Deleted, b.State = dialect.FromMicros(created), dialect.FromMicros(deleted),
		blob.BlobState(state)
	return b, nil
}

// insertRow adds a row for the blob in state unless one exists. Inserting
// first and then locking means two first references racing on PostgreSQL
// both end up updating one row, instead of one of them failing on the
// primary key.
func (b blobIndex) insertRow(
	ctx context.Context,
	row blob.Blob,
	state blob.BlobState,
	deleted time.Time,
) error {
	_, err := b.s.Exec(ctx, "insert "+b.table,
		dialect.InsertIgnore(b.table, strings.Split(blobCols, ", "), []string{"sha256"}),
		row.Checksums.SHA256, row.Checksums.SHA512, row.Checksums.SHA1, row.Checksums.MD5,
		row.Size,
		dialect.Micros(row.Created), 0, string(state), dialect.Micros(deleted))
	return err
}

func (b blobIndex) Ref(ctx context.Context, row blob.Blob, n int64) (blob.Blob, error) {
	if row.Checksums.SHA256 == "" || n < 0 {
		return blob.Blob{}, fmt.Errorf(
			"%s: sha256 and a non-negative count are required: %w",
			b.table,
			kernel.ErrInvalid,
		)
	}
	sha := row.Checksums.SHA256
	var out blob.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.insertRow(ctx, row, blob.BlobLive, time.Time{}); err != nil {
			return err
		}
		cur, err := b.get(ctx, sha, true)
		if err != nil {
			return err
		}
		if cur.State == blob.BlobDeleting {
			return fmt.Errorf("%s %s: %w", b.table, sha, port.ErrDeleting)
		}
		if !sameContent(cur, row) {
			return fmt.Errorf("%s sha256 %s: %w", b.table, sha, kernel.ErrConflict)
		}
		q := "UPDATE " + b.table + " SET ref_count = ref_count + ? WHERE sha256 = ?"
		args := []any{n, sha}
		if cur.State == blob.BlobDeleted {
			// Revived by content written again. The digests the tombstone
			// lacks (it may stand for content the index never recorded) are
			// filled in; deleted_at stays, telling the caller to check the
			// content is there.
			q = "UPDATE " + b.table + " SET state = ?, ref_count = ?, created_at = ?," +
				" sha512 = CASE WHEN sha512 = '' THEN ? ELSE sha512 END," +
				" sha1 = CASE WHEN sha1 = '' THEN ? ELSE sha1 END," +
				" md5 = CASE WHEN md5 = '' THEN ? ELSE md5 END WHERE sha256 = ?"
			args = []any{
				string(blob.BlobLive), n, dialect.Micros(row.Created),
				row.Checksums.SHA512, row.Checksums.SHA1, row.Checksums.MD5, sha,
			}
		}
		if _, err := b.s.Exec(ctx, "ref "+b.table, q, args...); err != nil {
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
func sameContent(stored, b blob.Blob) bool {
	if stored.Size != b.Size {
		return false
	}
	differ := func(x, y string) bool { return x != "" && y != "" && !strings.EqualFold(x, y) }
	return !differ(stored.Checksums.SHA512, b.Checksums.SHA512) &&
		!differ(stored.Checksums.SHA1, b.Checksums.SHA1) &&
		!differ(stored.Checksums.MD5, b.Checksums.MD5)
}

func (b blobIndex) get(ctx context.Context, sha256 string, lock bool) (blob.Blob, error) {
	q := "SELECT " + blobCols + " FROM " + b.table + " WHERE sha256 = ?"
	if lock {
		q += b.s.LockClause(ctx)
	}
	out, err := scanBlob(b.s.Q(ctx).QueryRowContext(ctx, b.s.Dialect().Rebind(q), sha256))
	return out, b.s.Wrap(fmt.Sprintf("get %s %s", b.table, sha256), err)
}

func (b blobIndex) Unref(ctx context.Context, sha256 string, n int64) (blob.Blob, error) {
	var out blob.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.s.ExecOne(ctx, "unref "+b.table+" "+sha256, "UPDATE "+b.table+
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

func (b blobIndex) Get(ctx context.Context, sha256 string) (blob.Blob, error) {
	return b.get(ctx, sha256, false)
}

func (b blobIndex) Find(ctx context.Context, c kernel.Checksums) (blob.Blob, error) {
	var col, val string
	switch {
	case c.SHA256 != "":
		col, val = "sha256", c.SHA256
	case c.SHA1 != "":
		col, val = "sha1", c.SHA1
	case c.MD5 != "":
		col, val = "md5", c.MD5
	default:
		return blob.Blob{}, fmt.Errorf("no checksum given: %w", kernel.ErrInvalid)
	}
	q := "SELECT " + blobCols + " FROM " + b.table + " WHERE " + col + " = ? AND state = ?" +
		" ORDER BY sha256 LIMIT 1"
	out, err := scanBlob(
		b.s.Q(ctx).QueryRowContext(ctx, b.s.Dialect().Rebind(q), val, string(blob.BlobLive)),
	)
	return out, b.s.Wrap(fmt.Sprintf("find %s %s %s", b.table, col, val), err)
}

// list pages the rows matching where (with args), ordered by sha256.
func (b blobIndex) list(
	ctx context.Context,
	p kernel.Page,
	where string,
	args ...any,
) ([]blob.Blob, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, kargs, err := dialect.Keyset(p, []string{"sha256"}, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []blob.Blob
	err = b.s.Rows(ctx, "list "+b.table,
		"SELECT "+blobCols+" FROM "+b.table+" WHERE "+where+" AND "+cond+
			dialect.OrderBy([]string{"sha256"}, false)+" LIMIT ?",
		append(append(args, kargs...), p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanBlob(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(v blob.Blob) []string { return []string{v.Checksums.SHA256} },
	)
	return page, next, nil
}

func (b blobIndex) Unreferenced(ctx context.Context, p kernel.Page) ([]blob.Blob, string, error) {
	return b.list(ctx, p, "state = ? AND ref_count = 0", string(blob.BlobLive))
}

func (b blobIndex) Deleting(ctx context.Context, p kernel.Page) ([]blob.Blob, string, error) {
	return b.list(ctx, p, "state = ?", string(blob.BlobDeleting))
}

func (b blobIndex) MarkDeleting(
	ctx context.Context,
	row blob.Blob,
	at time.Time,
) (blob.Blob, error) {
	sha := row.Checksums.SHA256
	if sha == "" {
		return blob.Blob{}, fmt.Errorf("%s: sha256 is required: %w", b.table, kernel.ErrInvalid)
	}
	var out blob.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.insertRow(ctx, row, blob.BlobDeleting, at); err != nil {
			return err
		}
		cur, err := b.get(ctx, sha, true)
		if err != nil {
			return err
		}
		switch {
		case cur.State == blob.BlobDeleting:
			out = cur
			return nil
		case cur.Refs > 0:
			return fmt.Errorf("%s %s still referenced: %w", b.table, sha, kernel.ErrConflict)
		}
		if _, err := b.s.Exec(ctx, "mark "+b.table, "UPDATE "+b.table+
			" SET state = ?, deleted_at = ? WHERE sha256 = ?",
			string(blob.BlobDeleting), dialect.Micros(at), sha); err != nil {
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
		case blob.BlobDeleted:
			return nil
		case blob.BlobDeleting:
			_, err := b.s.Exec(ctx, "finish "+b.table, "UPDATE "+b.table+
				" SET state = ?, deleted_at = ? WHERE sha256 = ?",
				string(blob.BlobDeleted), dialect.Micros(at), sha256)
			return err
		default:
			return fmt.Errorf("%s %s is live: %w", b.table, sha256, kernel.ErrConflict)
		}
	})
}

func (b blobIndex) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		dropped, err := b.s.Exec(ctx, "prune "+b.table,
			"DELETE FROM "+b.table+" WHERE state = ? AND deleted_at < ?",
			string(blob.BlobDeleted), dialect.Micros(before))
		if err != nil {
			return err
		}
		cleared, err := b.s.Exec(ctx, "prune "+b.table,
			"UPDATE "+b.table+" SET deleted_at = NULL WHERE state = ? AND deleted_at < ?",
			string(blob.BlobLive), dialect.Micros(before))
		n = dropped + cleared
		return err
	})
	return n, err
}

func (b blobIndex) Totals(ctx context.Context) (count, bytes int64, err error) {
	err = b.s.Q(ctx).QueryRowContext(ctx, b.s.Dialect().Rebind(
		"SELECT COUNT(*), COALESCE(SUM(size), 0) FROM "+b.table+" WHERE state = ?"),
		string(blob.BlobLive)).Scan(&count, &bytes)
	return count, bytes, b.s.Wrap("totals "+b.table, err)
}
