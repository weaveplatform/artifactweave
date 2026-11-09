package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// --- Trash -----------------------------------------------------------------

type trash struct{ s *Store }

const trashCols = "id, item_json, deleted_at, deleted_by"

func scanTrash(row interface{ Scan(...any) error }) (types.TrashEntry, error) {
	var (
		e       types.TrashEntry
		doc     string
		deleted sql.NullInt64
	)
	if err := row.Scan(&e.ID, &doc, &deleted, &e.DeletedBy); err != nil {
		return e, err //nolint:wrapcheck // wrapped by the caller
	}
	e.DeletedAt = dialect.FromMicros(deleted)
	if err := json.Unmarshal([]byte(doc), &e.Record); err != nil {
		return e, fmt.Errorf("decode trash record: %w", err)
	}
	return e, nil
}

func (t trash) Add(ctx context.Context, e types.TrashEntry) (types.TrashEntry, error) {
	it, err := canonical(e.Record.Item)
	if err != nil {
		return e, err
	}
	e.Record.Item = it
	e = store.Normalise(e)
	doc, err := json.Marshal(e.Record)
	if err != nil {
		return e, fmt.Errorf("encode trash record: %w", err)
	}
	e.ID, err = t.s.InsertID(ctx, "add trash entry", Insert("trash", []string{
		"original_repo_key",
		"original_path",
		"original_name",
		"item_json",
		"deleted_at",
		"deleted_by",
	}), it.RepoKey, it.Path, it.Name, string(doc), dialect.Micros(e.DeletedAt), e.DeletedBy)
	if err != nil {
		return e, err
	}
	return t.Get(ctx, e.ID)
}

func (t trash) Get(ctx context.Context, id int64) (types.TrashEntry, error) {
	e, err := scanTrash(t.s.Q(ctx).QueryRowContext(ctx, t.s.Dialect().Rebind(
		"SELECT "+trashCols+" FROM trash WHERE id = ?"), id))
	return e, t.s.Wrap(fmt.Sprintf("get trash entry %d", id), err)
}

func (t trash) query(ctx context.Context, op, q string, args ...any) ([]types.TrashEntry, error) {
	var out []types.TrashEntry
	err := t.s.Rows(ctx, op, q, args, func(rs *sql.Rows) error {
		e, err := scanTrash(rs)
		out = append(out, e)
		return err
	})
	return out, err
}

func (t trash) Find(ctx context.Context, repoKey, fullPath string) ([]types.TrashEntry, error) {
	parent, name, _, err := types.SplitItemPath(fullPath)
	if err != nil {
		return nil, nil //nolint:nilerr // a path that cannot name an item has no entries
	}
	return t.query(ctx, "find trash entries", "SELECT "+trashCols+
		" FROM trash WHERE original_repo_key = ? AND original_path = ? AND original_name = ?"+
		" ORDER BY COALESCE(deleted_at, 0) DESC, id DESC", repoKey, parent, name)
}

func (t trash) List(
	ctx context.Context, repoKey, prefix string, p store.Page,
) ([]types.TrashEntry, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("list trash: %w", err)
	}
	cols := []string{"original_repo_key", "original_path", "original_name", "id"}
	cond, args, err := dialect.Keyset(p, cols, map[string]bool{"id": true}, false)
	if err != nil {
		return nil, "", fmt.Errorf("list trash: %w", err)
	}
	where := cond
	if repoKey != "" {
		where += " AND original_repo_key = ?"
		args = append(args, repoKey)
	}
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		parent, name, _, err := types.SplitItemPath(prefix)
		if err != nil {
			return nil, "", nil //nolint:nilerr // a prefix that cannot name an item matches nothing
		}
		below := prefix + "/"
		where += " AND ((original_path = ? AND original_name = ?) OR original_path = ? OR substr(original_path, 1, ?) = ?)"
		args = append(args, parent, name, prefix, utf8.RuneCountInString(below), below)
	}
	out, err := t.query(ctx, "list trash", "SELECT "+trashCols+" FROM trash WHERE "+where+
		dialect.OrderBy(cols, false)+" LIMIT ?", append(args, p.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(e types.TrashEntry) []string {
		return []string{
			e.Record.Item.RepoKey,
			e.Record.Item.Path,
			e.Record.Item.Name,
			dialect.Itoa(e.ID),
		}
	})
	return page, next, nil
}

func (t trash) Delete(ctx context.Context, id int64) error {
	return t.s.ExecOne(
		ctx,
		fmt.Sprintf("delete trash entry %d", id),
		"DELETE FROM trash WHERE id = ?",
		id,
	)
}

func (t trash) Expired(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]types.TrashEntry, error) {
	return t.query(ctx, "expired trash entries", "SELECT "+trashCols+
		" FROM trash WHERE deleted_at < ? ORDER BY deleted_at, id LIMIT ?", before.UnixMicro(), dialect.LimitOr(limit))
}

// --- Package index ---------------------------------------------------------

type packages struct{ s *Store }

const (
	packageCols = "id, repo_key, package_type, name, created_at"
	versionCols = "id, package_id, version, order_key, created_at, last_downloaded_at, metadata"
)

func scanPackage(row interface{ Scan(...any) error }) (types.Package, error) {
	var (
		p       types.Package
		typ     string
		created sql.NullInt64
	)
	err := row.Scan(&p.ID, &p.RepoKey, &typ, &p.Name, &created)
	p.Type, p.Created = repo.PackageType(typ), dialect.FromMicros(created)
	return p, err //nolint:wrapcheck // wrapped by the caller
}

func scanVersion(row interface{ Scan(...any) error }) (types.PackageVersion, error) {
	var (
		v             types.PackageVersion
		created, last sql.NullInt64
		metadata      sql.NullString
	)
	err := row.Scan(&v.ID, &v.PackageID, &v.Version, &v.OrderKey, &created, &last, &metadata)
	v.Created, v.LastDownloaded, v.Metadata = dialect.FromMicros(
		created,
	), dialect.FromMicros(
		last,
	), dialect.FromRaw(
		metadata,
	)
	return v, err //nolint:wrapcheck // wrapped by the caller
}

func (p packages) Ensure(ctx context.Context, pkg types.Package) (types.Package, error) {
	if pkg.RepoKey == "" || pkg.Name == "" {
		return pkg, fmt.Errorf(
			"ensure package: repository and name are required: %w",
			store.ErrInvalid,
		)
	}
	var out types.Package
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.Exec(ctx, "ensure package "+pkg.Name,
			InsertIgnore("packages", []string{"repo_key", "package_type", "name", "created_at"},
				[]string{"repo_key", "name"}),
			pkg.RepoKey, string(pkg.Type), pkg.Name, dialect.Micros(pkg.Created)); err != nil {
			return err
		}
		var err error
		out, err = p.Find(ctx, pkg.RepoKey, pkg.Name)
		return err
	})
	return out, err
}

func (p packages) Get(ctx context.Context, id int64) (types.Package, error) {
	out, err := scanPackage(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+packageCols+" FROM packages WHERE id = ?"), id))
	return out, p.s.Wrap(fmt.Sprintf("get package %d", id), err)
}

func (p packages) Find(ctx context.Context, repoKey, name string) (types.Package, error) {
	out, err := scanPackage(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+packageCols+" FROM packages WHERE repo_key = ? AND name = ?"), repoKey, name))
	return out, p.s.Wrap(fmt.Sprintf("find package %s:%s", repoKey, name), err)
}

func (p packages) List(
	ctx context.Context,
	repoKey string,
	pg store.Page,
) ([]types.Package, string, error) {
	pg, err := pg.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("list packages: %w", err)
	}
	cond, args, err := dialect.Keyset(pg, []string{"name"}, nil, false)
	if err != nil {
		return nil, "", fmt.Errorf("list packages: %w", err)
	}
	var out []types.Package
	err = p.s.Rows(
		ctx,
		"list packages",
		"SELECT "+packageCols+" FROM packages WHERE repo_key = ? AND "+cond+
			" ORDER BY name LIMIT ?",
		append(append([]any{repoKey}, args...), pg.Limit+1),
		func(rs *sql.Rows) error {
			v, err := scanPackage(rs)
			out = append(out, v)
			return err
		},
	)
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		pg.Limit,
		func(v types.Package) []string { return []string{v.Name} },
	)
	return page, next, nil
}

func (p packages) Delete(ctx context.Context, id int64) error {
	return p.s.ExecOne(
		ctx,
		fmt.Sprintf("delete package %d", id),
		"DELETE FROM packages WHERE id = ?",
		id,
	)
}

func (p packages) PutVersion(
	ctx context.Context,
	v types.PackageVersion,
) (types.PackageVersion, error) {
	if v.Version == "" {
		return v, fmt.Errorf("put package version: version is required: %w", store.ErrInvalid)
	}
	var out types.PackageVersion
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		pkg, err := p.Get(ctx, v.PackageID)
		if err != nil {
			return err
		}
		if _, err := p.s.Exec(
			ctx,
			"put package version "+v.Version,
			Upsert("package_versions",
				[]string{"package_id", "version", "created_at", "last_downloaded_at", "metadata"},
				[]string{"package_id", "version"}),
			v.PackageID,
			v.Version,
			dialect.Micros(v.Created),
			dialect.Micros(v.LastDownloaded),
			dialect.RawJSON(v.Metadata),
		); err != nil {
			return err
		}
		if err := p.reorder(ctx, pkg); err != nil {
			return err
		}
		out, err = p.Version(ctx, v.PackageID, v.Version)
		return err
	})
	return out, err
}

// reorder recomputes the order keys of every version of pkg with its type's
// comparator and writes the ones that changed.
func (p packages) reorder(ctx context.Context, pkg types.Package) error {
	vs, err := p.Versions(ctx, pkg.ID)
	if err != nil {
		return err
	}
	old := make(map[int64]int64, len(vs))
	for _, v := range vs {
		old[v.ID] = v.OrderKey
	}
	types.AssignOrderKeys(types.ComparatorFor(pkg.Type), vs)
	for _, v := range vs {
		if old[v.ID] == v.OrderKey {
			continue
		}
		if _, err := p.s.Exec(
			ctx,
			"order package versions",
			"UPDATE package_versions SET order_key = ? WHERE id = ?",
			v.OrderKey,
			v.ID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (p packages) Version(
	ctx context.Context,
	packageID int64,
	version string,
) (types.PackageVersion, error) {
	out, err := scanVersion(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+versionCols+" FROM package_versions WHERE package_id = ? AND version = ?",
	), packageID, version))
	return out, p.s.Wrap(fmt.Sprintf("get package %d version %s", packageID, version), err)
}

func (p packages) versions(
	ctx context.Context,
	op, q string,
	args ...any,
) ([]types.PackageVersion, error) {
	var out []types.PackageVersion
	err := p.s.Rows(ctx, op, q, args, func(rs *sql.Rows) error {
		v, err := scanVersion(rs)
		out = append(out, v)
		return err
	})
	return out, err
}

func (p packages) Versions(ctx context.Context, packageID int64) ([]types.PackageVersion, error) {
	return p.versions(ctx, "list package versions", "SELECT "+versionCols+
		" FROM package_versions WHERE package_id = ? ORDER BY order_key, version", packageID)
}

func (p packages) DeleteVersion(ctx context.Context, id int64) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		var pkgID int64
		if err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
			"SELECT package_id FROM package_versions WHERE id = ?"), id).Scan(&pkgID); err != nil {
			return p.s.Wrap(fmt.Sprintf("delete package version %d", id), err)
		}
		if _, err := p.s.Exec(
			ctx,
			"delete package version",
			"DELETE FROM package_versions WHERE id = ?",
			id,
		); err != nil {
			return err
		}
		pkg, err := p.Get(ctx, pkgID)
		if err != nil {
			return err
		}
		return p.reorder(ctx, pkg)
	})
}

func (p packages) LinkFile(ctx context.Context, f types.VersionFile) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		for _, check := range []struct {
			what string
			q    string
			id   int64
		}{
			{"package version", "SELECT 1 FROM package_versions WHERE id = ?", f.VersionID},
			{"item", "SELECT 1 FROM items WHERE id = ?", f.ItemID},
		} {
			ok, err := p.s.Exists(ctx, "link file", check.q, check.id)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("link file: %s %d: %w", check.what, check.id, store.ErrNotFound)
			}
		}
		_, err := p.s.Exec(ctx, "link file", InsertIgnore(
			"package_version_files",
			[]string{
				"version_id",
				"item_id",
			},
			[]string{"version_id", "item_id"},
		), f.VersionID, f.ItemID)
		return err
	})
}

func (p packages) Files(ctx context.Context, versionID int64) ([]int64, error) {
	var out []int64
	err := p.s.Rows(
		ctx,
		"version files",
		"SELECT item_id FROM package_version_files WHERE version_id = ? ORDER BY item_id",
		[]any{versionID},
		func(rs *sql.Rows) error {
			var id int64
			err := rs.Scan(&id)
			out = append(out, id)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

func (p packages) VersionOf(ctx context.Context, itemID int64) (types.PackageVersion, error) {
	out, err := scanVersion(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT v.id, v.package_id, v.version, v.order_key, v.created_at, v.last_downloaded_at, v.metadata"+
			" FROM package_versions v JOIN package_version_files f ON f.version_id = v.id"+
			" WHERE f.item_id = ? ORDER BY v.id LIMIT 1",
	),
		itemID))
	return out, p.s.Wrap(fmt.Sprintf("version of item %d", itemID), err)
}

func (p packages) RecordVersionDownload(ctx context.Context, versionID int64, at time.Time) error {
	return p.s.ExecOne(ctx, fmt.Sprintf("record download of version %d", versionID),
		"UPDATE package_versions SET last_downloaded_at = ? WHERE id = ?", dialect.Micros(at),
		versionID)
}
