package sql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Stamps returns the conflict rule's item records (decision 0021).
func (s *Store) Stamps() port.ItemStampStore { return stampStore{s} }

type stampStore struct{ s *Store }

const stampCols = "repo_key, path, hlc_wall, hlc_logical, origin, deleted, folder, sha256, size, pending"

func stampArgs(v model.ItemStamp) []any {
	return []any{
		v.RepoKey, v.Path, v.Time.Wall, int64(v.Time.Logical), v.Origin, v.Deleted, v.Folder,
		v.SHA256, v.Size, dialect.RawJSON(v.Pending),
	}
}

func scanStamp(r interface{ Scan(...any) error }) (model.ItemStamp, error) {
	var (
		v       model.ItemStamp
		logical int64
		pending sql.NullString
	)
	err := r.Scan(&v.RepoKey, &v.Path, &v.Time.Wall, &logical, &v.Origin, &v.Deleted, &v.Folder,
		&v.SHA256, &v.Size, &pending)
	v.Time.Logical, v.Pending = uint32(logical), dialect.FromRaw(pending) //nolint:gosec // stored from a uint32
	return v, err                                                         //nolint:wrapcheck // wrapped by the caller
}

func (st stampStore) Stamp(ctx context.Context, repoKey, path string) (model.ItemStamp, error) {
	v, err := scanStamp(st.s.Q(ctx).QueryRowContext(ctx, st.s.Dialect().Rebind(
		"SELECT "+stampCols+" FROM federation_item_stamps WHERE repo_key = ? AND path = ?"), repoKey, path))
	return v, st.s.Wrap("read item stamp", err)
}

func (st stampStore) Record(ctx context.Context, v model.ItemStamp) error {
	_, err := st.s.Exec(ctx, "record item stamp",
		dialect.Upsert("federation_item_stamps", strings.Split(stampCols, ", "), []string{"repo_key", "path"}),
		stampArgs(v)...)
	return err
}

// Replace is a compare-and-set: an UPDATE guarded by the stamp read, or an
// INSERT that does nothing when a row appeared. Both wait on PostgreSQL for
// a transaction that holds the row, then test what it committed.
func (st stampStore) Replace(ctx context.Context, prev *model.Stamp, next model.ItemStamp) (bool, error) {
	if prev == nil {
		n, err := st.s.Exec(ctx, "insert item stamp",
			dialect.InsertIgnore("federation_item_stamps", strings.Split(stampCols, ", "), []string{"repo_key", "path"}),
			stampArgs(next)...)
		return n == 1, err
	}
	args := stampArgs(next)[2:]
	args = append(args, next.RepoKey, next.Path, prev.Time.Wall, int64(prev.Time.Logical), prev.Origin)
	n, err := st.s.Exec(ctx, "replace item stamp",
		"UPDATE federation_item_stamps SET hlc_wall = ?, hlc_logical = ?, origin = ?, deleted = ?, folder = ?, "+
			"sha256 = ?, size = ?, pending = ? "+
			"WHERE repo_key = ? AND path = ? AND hlc_wall = ? AND hlc_logical = ? AND origin = ?", args...)
	return n == 1, err
}

func (st stampStore) list(ctx context.Context, op, where string, args ...any) ([]model.ItemStamp, error) {
	var out []model.ItemStamp
	err := st.s.Rows(ctx, op,
		"SELECT "+stampCols+" FROM federation_item_stamps WHERE "+where, args,
		func(r *sql.Rows) error {
			v, err := scanStamp(r)
			out = append(out, v)
			return err
		})
	return out, err
}

func (st stampStore) PendingOn(ctx context.Context, repoKey, sha256 string) ([]model.ItemStamp, error) {
	return st.list(ctx, "list item stamps pending a blob",
		"repo_key = ? AND sha256 = ? AND pending IS NOT NULL ORDER BY path", repoKey, sha256)
}

func (st stampStore) Holds(ctx context.Context, repoKey, sha256 string) (bool, error) {
	return st.s.Exists(ctx, "find item stamp of a blob",
		"SELECT 1 FROM federation_item_stamps WHERE repo_key = ? AND sha256 = ? AND deleted = ? AND pending IS NULL LIMIT 1",
		repoKey, sha256, false)
}

func (st stampStore) PruneTombstones(ctx context.Context, before outbox.HLC) (int64, error) {
	return st.s.Exec(ctx, "prune item tombstones", //nolint:wrapcheck // Exec names the operation
		"DELETE FROM federation_item_stamps WHERE deleted = ? AND "+
			"(hlc_wall < ? OR hlc_wall = ? AND hlc_logical < ?)",
		true, before.Wall, before.Wall, int64(before.Logical))
}

func (st stampStore) Clear(ctx context.Context, repoKey string) error {
	_, err := st.s.Exec(ctx, "clear item stamps", "DELETE FROM federation_item_stamps WHERE repo_key = ?", repoKey)
	return err
}

func (st stampStore) List(ctx context.Context, repoKey, after string, limit int) ([]model.ItemStamp, error) {
	// path is COLLATE "C" on PostgreSQL (0015), so this order is the items'
	// full path order, byte by byte, as SQLite's is.
	return st.list(ctx, "list item stamps", "repo_key = ? AND path > ? ORDER BY path LIMIT ?", repoKey, after, limit)
}

// Remove deletes the record at path and, with below, those of the paths
// from "<path>/" up to, not including, "<path>0": '0' follows '/', so the
// range is exactly what lies under the folder.
func (st stampStore) Remove(ctx context.Context, repoKey, path string, below bool) error {
	q, args := "DELETE FROM federation_item_stamps WHERE repo_key = ? AND (path = ?", []any{repoKey, path}
	if below {
		q += " OR path > ? AND path < ?"
		args = append(args, path+"/", path+"0")
	}
	_, err := st.s.Exec(ctx, "remove item stamp", q+")", args...)
	return err
}
