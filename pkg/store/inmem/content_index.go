package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
)

// --- Uploads ---------------------------------------------------------------

type uploads struct{ s *Store }

func (u uploads) t() *table[string, store.Upload] {
	return tableOf[string, store.Upload](u.s, "uploads")
}

func (u uploads) Put(ctx context.Context, up store.Upload) error {
	if up.ID == "" {
		return fmt.Errorf("put upload: id is required: %w", store.ErrInvalid)
	}
	return u.s.do(ctx, func() error {
		if len(up.State) == 0 {
			up.State = nil
		}
		u.t().put(up.ID, up)
		return nil
	})
}

func (u uploads) Get(ctx context.Context, id string) (store.Upload, error) {
	var out store.Upload
	err := u.s.do(ctx, func() error {
		var ok bool
		if out, ok = u.t().get(id); !ok {
			return notFound("upload", id)
		}
		return nil
	})
	return out, err
}

func (u uploads) Delete(ctx context.Context, id string) error {
	return u.s.do(ctx, func() error {
		if !u.t().del(id) {
			return notFound("upload", id)
		}
		return nil
	})
}

// lastActive is when a session was last touched for staleness, in the SQL
// column's terms: Updated, else Started, else the epoch.
func lastActive(up store.Upload) int64 {
	switch {
	case !up.Updated.IsZero():
		return up.Updated.UnixMicro()
	case !up.Started.IsZero():
		return up.Started.UnixMicro()
	default:
		return 0
	}
}

func (u uploads) Stale(ctx context.Context, before time.Time, limit int) ([]store.Upload, error) {
	var out []store.Upload
	err := u.s.do(ctx, func() error {
		cutoff := before.UnixMicro()
		out = u.t().all(func(_ string, up store.Upload) bool { return lastActive(up) < cutoff },
			func(a, b store.Upload) int {
				return cmp.Or(cmp.Compare(lastActive(a), lastActive(b)), cmp.Compare(a.ID, b.ID))
			})
		out = out[:min(len(out), limitOr(limit))]
		return nil
	})
	return out, err
}

// limitOr applies the default limit for "at most limit" queries.
func limitOr(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}

// --- Trash -----------------------------------------------------------------

type trash struct{ s *Store }

func (t trash) t() *table[int64, types.TrashEntry] {
	return tableOf[int64, types.TrashEntry](t.s, "trash")
}

func (t trash) Add(ctx context.Context, e types.TrashEntry) (types.TrashEntry, error) {
	it, err := canonical(e.Record.Item)
	if err != nil {
		return e, err
	}
	e.Record.Item = it
	err = t.s.do(ctx, func() error {
		e.ID = t.s.nextID("trash")
		t.t().put(e.ID, e)
		e, _ = t.t().get(e.ID)
		return nil
	})
	return e, err
}

func (t trash) Get(ctx context.Context, id int64) (types.TrashEntry, error) {
	var out types.TrashEntry
	err := t.s.do(ctx, func() error {
		var ok bool
		if out, ok = t.t().get(id); !ok {
			return notFound("trash entry", id)
		}
		return nil
	})
	return out, err
}

// deletedKey is the SQL sort value of a deletion time, with an unset time
// first, as COALESCE(deleted_at, 0) sorts it.
func deletedKey(e types.TrashEntry) int64 {
	if e.DeletedAt.IsZero() {
		return 0
	}
	return e.DeletedAt.UnixMicro()
}

func (t trash) Find(ctx context.Context, repoKey, fullPath string) ([]types.TrashEntry, error) {
	parent, name, _, err := types.SplitItemPath(fullPath)
	if err != nil {
		return nil, nil //nolint:nilerr // a path that cannot name an item has no entries
	}
	var out []types.TrashEntry
	err = t.s.do(ctx, func() error {
		out = t.t().all(func(_ int64, e types.TrashEntry) bool {
			it := e.Record.Item
			return it.RepoKey == repoKey && it.Path == parent && it.Name == name
		}, func(a, b types.TrashEntry) int {
			return cmp.Or(cmp.Compare(deletedKey(b), deletedKey(a)), cmp.Compare(b.ID, a.ID))
		})
		return nil
	})
	return out, err
}

func (t trash) List(
	ctx context.Context, repoKey, prefix string, p store.Page,
) ([]types.TrashEntry, string, error) {
	keep := func(types.TrashEntry) bool { return true }
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		parent, name, _, err := types.SplitItemPath(prefix)
		if err != nil {
			return nil, "", nil //nolint:nilerr // a prefix that cannot name an item matches nothing
		}
		keep = func(e types.TrashEntry) bool {
			it := e.Record.Item
			return (it.Path == parent && it.Name == name) || below(it.Path, prefix)
		}
	}
	var (
		out  []types.TrashEntry
		next string
	)
	err := t.s.do(ctx, func() error {
		rows := t.t().all(func(_ int64, e types.TrashEntry) bool {
			return (repoKey == "" || e.Record.Item.RepoKey == repoKey) && keep(e)
		}, func(a, b types.TrashEntry) int {
			x, y := a.Record.Item, b.Record.Item
			return cmp.Or(cmp.Compare(x.RepoKey, y.RepoKey), cmp.Compare(x.Path, y.Path),
				cmp.Compare(x.Name, y.Name), cmp.Compare(a.ID, b.ID))
		})
		var err error
		out, next, err = paginate(rows, p, false, func(e types.TrashEntry) []any {
			return []any{e.Record.Item.RepoKey, e.Record.Item.Path, e.Record.Item.Name, e.ID}
		})
		return err
	})
	return out, next, err
}

func (t trash) Delete(ctx context.Context, id int64) error {
	return t.s.do(ctx, func() error {
		if !t.t().del(id) {
			return notFound("trash entry", id)
		}
		return nil
	})
}

func (t trash) Expired(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]types.TrashEntry, error) {
	var out []types.TrashEntry
	err := t.s.do(ctx, func() error {
		cutoff := before.UnixMicro()
		out = t.t().all(func(_ int64, e types.TrashEntry) bool {
			return !e.DeletedAt.IsZero() && e.DeletedAt.UnixMicro() < cutoff
		}, func(a, b types.TrashEntry) int {
			return cmp.Or(cmp.Compare(deletedKey(a), deletedKey(b)), cmp.Compare(a.ID, b.ID))
		})
		out = out[:min(len(out), limitOr(limit))]
		return nil
	})
	return out, err
}

// --- Package index ---------------------------------------------------------

type packages struct{ s *Store }

func (p packages) t() *table[int64, types.Package] {
	return tableOf[int64, types.Package](p.s, "packages")
}

func (p packages) versions() *table[int64, types.PackageVersion] {
	return tableOf[int64, types.PackageVersion](p.s, "package_versions")
}

func (p packages) files() *table[types.VersionFile, bool] {
	return tableOf[types.VersionFile, bool](p.s, "package_version_files")
}

// find returns the package (repoKey, name); call it under the lock.
func (p packages) find(repoKey, name string) (types.Package, bool) {
	rows := p.t().
		all(func(_ int64, v types.Package) bool { return v.RepoKey == repoKey && v.Name == name }, nil)
	if len(rows) == 0 {
		return types.Package{}, false
	}
	return rows[0], true
}

func (p packages) Ensure(ctx context.Context, pkg types.Package) (types.Package, error) {
	if pkg.RepoKey == "" || pkg.Name == "" {
		return pkg, fmt.Errorf(
			"ensure package: repository and name are required: %w",
			store.ErrInvalid,
		)
	}
	var out types.Package
	err := p.s.do(ctx, func() error {
		if cur, ok := p.find(pkg.RepoKey, pkg.Name); ok {
			out = cur
			return nil
		}
		pkg.ID = p.s.nextID("packages")
		p.t().put(pkg.ID, pkg)
		out, _ = p.t().get(pkg.ID)
		return nil
	})
	return out, err
}

func (p packages) Get(ctx context.Context, id int64) (types.Package, error) {
	var out types.Package
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.t().get(id); !ok {
			return notFound("package", id)
		}
		return nil
	})
	return out, err
}

func (p packages) Find(ctx context.Context, repoKey, name string) (types.Package, error) {
	var out types.Package
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.find(repoKey, name); !ok {
			return notFound("package", repoKey+":"+name)
		}
		return nil
	})
	return out, err
}

func (p packages) List(
	ctx context.Context,
	repoKey string,
	pg store.Page,
) ([]types.Package, string, error) {
	var (
		out  []types.Package
		next string
	)
	err := p.s.do(ctx, func() error {
		rows := p.t().all(func(_ int64, v types.Package) bool { return v.RepoKey == repoKey },
			func(a, b types.Package) int { return cmp.Compare(a.Name, b.Name) })
		var err error
		out, next, err = paginate(
			rows,
			pg,
			false,
			func(v types.Package) []any { return []any{v.Name} },
		)
		return err
	})
	return out, next, err
}

func (p packages) Delete(ctx context.Context, id int64) error {
	return p.s.do(ctx, func() error {
		if !p.t().del(id) {
			return notFound("package", id)
		}
		for _, v := range p.versions().all(func(_ int64, v types.PackageVersion) bool { return v.PackageID == id }, nil) {
			p.removeVersion(v.ID)
		}
		return nil
	})
}

// removeVersion deletes a version and its file links; call it under the
// lock.
func (p packages) removeVersion(id int64) {
	p.versions().del(id)
	p.files().deleteWhere(func(f types.VersionFile, _ bool) bool { return f.VersionID == id })
}

// sorted returns a package's versions ordered by order key, then version.
func (p packages) sorted(packageID int64) []types.PackageVersion {
	return p.versions().
		all(func(_ int64, v types.PackageVersion) bool { return v.PackageID == packageID },
			func(a, b types.PackageVersion) int {
				return cmp.Or(
					cmp.Compare(a.OrderKey, b.OrderKey),
					cmp.Compare(a.Version, b.Version),
				)
			})
}

// reorder recomputes the order keys of a package's versions; call it under
// the lock.
func (p packages) reorder(pkg types.Package) {
	vs := p.sorted(pkg.ID)
	types.AssignOrderKeys(types.ComparatorFor(pkg.Type), vs)
	for _, v := range vs {
		p.versions().put(v.ID, v)
	}
}

func (p packages) PutVersion(
	ctx context.Context,
	v types.PackageVersion,
) (types.PackageVersion, error) {
	if v.Version == "" {
		return v, fmt.Errorf("put package version: version is required: %w", store.ErrInvalid)
	}
	var out types.PackageVersion
	err := p.s.do(ctx, func() error {
		pkg, ok := p.t().get(v.PackageID)
		if !ok {
			return notFound("package", v.PackageID)
		}
		v.ID, v.OrderKey = 0, 0
		for _, cur := range p.sorted(pkg.ID) {
			if cur.Version == v.Version {
				v.ID, v.OrderKey = cur.ID, cur.OrderKey
			}
		}
		if v.ID == 0 {
			v.ID = p.s.nextID("package_versions")
		}
		if len(v.Metadata) == 0 {
			v.Metadata = nil
		}
		p.versions().put(v.ID, v)
		p.reorder(pkg)
		out, _ = p.versions().get(v.ID)
		return nil
	})
	return out, err
}

func (p packages) Version(
	ctx context.Context,
	packageID int64,
	version string,
) (types.PackageVersion, error) {
	var out types.PackageVersion
	err := p.s.do(ctx, func() error {
		for _, v := range p.sorted(packageID) {
			if v.Version == version {
				out = v
				return nil
			}
		}
		return notFound("package version", fmt.Sprintf("%d/%s", packageID, version))
	})
	return out, err
}

func (p packages) Versions(ctx context.Context, packageID int64) ([]types.PackageVersion, error) {
	var out []types.PackageVersion
	err := p.s.do(ctx, func() error {
		out = p.sorted(packageID)
		return nil
	})
	return out, err
}

func (p packages) DeleteVersion(ctx context.Context, id int64) error {
	return p.s.do(ctx, func() error {
		v, ok := p.versions().get(id)
		if !ok {
			return notFound("package version", id)
		}
		p.removeVersion(id)
		if pkg, ok := p.t().get(v.PackageID); ok {
			p.reorder(pkg)
		}
		return nil
	})
}

func (p packages) LinkFile(ctx context.Context, f types.VersionFile) error {
	return p.s.do(ctx, func() error {
		if !p.versions().has(f.VersionID) {
			return notFound("link file: package version", f.VersionID)
		}
		if !tableOf[int64, types.Item](p.s, "items").has(f.ItemID) {
			return notFound("link file: item", f.ItemID)
		}
		p.files().put(f, true)
		return nil
	})
}

func (p packages) Files(ctx context.Context, versionID int64) ([]int64, error) {
	var out []int64
	err := p.s.do(ctx, func() error {
		for f := range p.files().rows {
			if f.VersionID == versionID {
				out = append(out, f.ItemID)
			}
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

func (p packages) VersionOf(ctx context.Context, itemID int64) (types.PackageVersion, error) {
	var out types.PackageVersion
	err := p.s.do(ctx, func() error {
		var ids []int64
		for f := range p.files().rows {
			if f.ItemID == itemID {
				ids = append(ids, f.VersionID)
			}
		}
		if len(ids) == 0 {
			return notFound("version of item", itemID)
		}
		out, _ = p.versions().get(slices.Min(ids))
		return nil
	})
	return out, err
}

func (p packages) RecordVersionDownload(ctx context.Context, versionID int64, at time.Time) error {
	return p.s.do(ctx, func() error {
		v, ok := p.versions().get(versionID)
		if !ok {
			return notFound("package version", versionID)
		}
		v.LastDownloaded = at
		p.versions().put(versionID, v)
		return nil
	})
}
