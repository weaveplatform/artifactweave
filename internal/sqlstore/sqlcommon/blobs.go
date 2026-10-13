package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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

const blobCols = "sha256, sha1, md5, size, created_at, ref_count"

func scanBlob(row interface{ Scan(...any) error }) (types.Blob, error) {
	var (
		b       types.Blob
		created sql.NullInt64
	)
	if err := row.Scan(
		&b.Checksums.SHA256,
		&b.Checksums.SHA1,
		&b.Checksums.MD5,
		&b.Size,
		&created,
		&b.Refs,
	); err != nil {
		return b, err //nolint:wrapcheck // wrapped by the caller
	}
	b.Created = fromMicros(created)
	return b, nil
}

func (b blobIndex) Ref(ctx context.Context, blob types.Blob, n int64) (types.Blob, error) {
	if blob.Checksums.SHA256 == "" || n < 0 {
		return types.Blob{}, fmt.Errorf(
			"%s: sha256 and a non-negative count are required: %w",
			b.table,
			store.ErrInvalid,
		)
	}
	var out types.Blob
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		// Insert-if-absent first, then lock: two first references racing on
		// PostgreSQL both end up incrementing one row instead of one of them
		// failing on the primary key.
		if _, err := b.s.exec(ctx, "ref "+b.table,
			InsertIgnore(b.table, strings.Split(blobCols, ", "), []string{"sha256"}),
			blob.Checksums.SHA256, blob.Checksums.SHA1, blob.Checksums.MD5, blob.Size,
			micros(blob.Created), 0); err != nil {
			return err
		}
		cur, err := b.get(ctx, blob.Checksums.SHA256, true)
		if err != nil {
			return err
		}
		if !sameContent(cur, blob) {
			return fmt.Errorf("%s sha256 %s: %w", b.table, blob.Checksums.SHA256, store.ErrConflict)
		}
		if _, err := b.s.exec(ctx, "ref "+b.table, "UPDATE "+b.table+
			" SET ref_count = ref_count + ? WHERE sha256 = ?", n, blob.Checksums.SHA256); err != nil {
			return err
		}
		out, err = b.get(ctx, blob.Checksums.SHA256, false)
		return err
	})
	return out, err
}

// sameContent reports whether a stored blob and a new reference describe the
// same bytes. Digests the reference leaves empty are not compared.
func sameContent(stored, b types.Blob) bool {
	if stored.Size != b.Size {
		return false
	}
	if b.Checksums.SHA1 != "" && !strings.EqualFold(stored.Checksums.SHA1, b.Checksums.SHA1) {
		return false
	}
	return b.Checksums.MD5 == "" || strings.EqualFold(stored.Checksums.MD5, b.Checksums.MD5)
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
	q := "SELECT " + blobCols + " FROM " + b.table + " WHERE " + col + " = ? ORDER BY sha256 LIMIT 1"
	out, err := scanBlob(b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(q), val))
	return out, b.s.wrap(fmt.Sprintf("find %s %s %s", b.table, col, val), err)
}

func (b blobIndex) Unreferenced(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, []string{"sha256"}, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []types.Blob
	err = b.s.rows(ctx, "list "+b.table,
		"SELECT "+blobCols+" FROM "+b.table+" WHERE ref_count = 0 AND "+cond+
			orderBy([]string{"sha256"}, false)+" LIMIT ?",
		append(args, p.Limit+1),
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

func (b blobIndex) Delete(ctx context.Context, sha256 string) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		cur, err := b.get(ctx, sha256, true)
		if err != nil {
			return err
		}
		if cur.Refs > 0 {
			return fmt.Errorf("%s %s still referenced: %w", b.table, sha256, store.ErrConflict)
		}
		return b.s.execOne(ctx, "delete "+b.table+" "+sha256,
			"DELETE FROM "+b.table+" WHERE sha256 = ? AND ref_count = 0", sha256)
	})
}

func (b blobIndex) Totals(ctx context.Context) (count, bytes int64, err error) {
	err = b.s.q(ctx).QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(size), 0) FROM "+b.table).Scan(&count, &bytes)
	return count, bytes, b.s.wrap("totals "+b.table, err)
}
