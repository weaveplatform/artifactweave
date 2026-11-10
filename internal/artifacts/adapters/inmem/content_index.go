package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// limitOr applies the default limit for "at most limit" queries.
func limitOr(limit int) int {
	if limit <= 0 {
		return kernel.DefaultLimit
	}
	return min(limit, kernel.MaxLimit)
}

// --- Trash -----------------------------------------------------------------

// Trash returns the trash store: the trash can that deletes fill and
// restores and expiry empty (decision 0010).
func (s *Store) Trash() port.TrashStore { return trash{s} }

type trash struct{ s *Store }

func (t trash) t() *memtx.Table[int64, model.TrashEntry] {
	return memtx.TableOf[int64, model.TrashEntry](t.s.Store, "trash")
}

func (t trash) Add(ctx context.Context, e model.TrashEntry) (model.TrashEntry, error) {
	it, err := canonical(e.Record.Item)
	if err != nil {
		return e, err
	}
	e.Record.Item = it
	err = t.s.Do(ctx, func() error {
		e.ID = t.s.NextID("trash")
		t.t().Put(e.ID, e)
		e, _ = t.t().Get(e.ID)
		return nil
	})
	return e, err
}

func (t trash) Get(ctx context.Context, id int64) (model.TrashEntry, error) {
	var out model.TrashEntry
	err := t.s.Do(ctx, func() error {
		var ok bool
		if out, ok = t.t().Get(id); !ok {
			return memtx.NotFound("trash entry", id)
		}
		return nil
	})
	return out, err
}

// deletedKey is the SQL sort value of a deletion time, with an unset time
// first, as COALESCE(deleted_at, 0) sorts it.
func deletedKey(e model.TrashEntry) int64 {
	if e.DeletedAt.IsZero() {
		return 0
	}
	return e.DeletedAt.UnixMicro()
}

func (t trash) Find(ctx context.Context, repoKey, fullPath string) ([]model.TrashEntry, error) {
	parent, name, _, err := kernel.SplitItemPath(fullPath)
	if err != nil {
		return nil, nil //nolint:nilerr // a path that cannot name an item has no entries
	}
	var out []model.TrashEntry
	err = t.s.Do(ctx, func() error {
		out = t.t().All(func(_ int64, e model.TrashEntry) bool {
			it := e.Record.Item
			return it.RepoKey == repoKey && it.Path == parent && it.Name == name
		}, func(a, b model.TrashEntry) int {
			return cmp.Or(cmp.Compare(deletedKey(b), deletedKey(a)), cmp.Compare(b.ID, a.ID))
		})
		return nil
	})
	return out, err
}

func (t trash) List(
	ctx context.Context, repoKey, prefix string, p kernel.Page,
) ([]model.TrashEntry, string, error) {
	keep := func(model.TrashEntry) bool { return true }
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		parent, name, _, err := kernel.SplitItemPath(prefix)
		if err != nil {
			return nil, "", nil //nolint:nilerr // a prefix that cannot name an item matches nothing
		}
		keep = func(e model.TrashEntry) bool {
			it := e.Record.Item
			return (it.Path == parent && it.Name == name) || below(it.Path, prefix)
		}
	}
	var (
		out  []model.TrashEntry
		next string
	)
	err := t.s.Do(ctx, func() error {
		rows := t.t().All(func(_ int64, e model.TrashEntry) bool {
			return (repoKey == "" || e.Record.Item.RepoKey == repoKey) && keep(e)
		}, func(a, b model.TrashEntry) int {
			x, y := a.Record.Item, b.Record.Item
			return cmp.Or(cmp.Compare(x.RepoKey, y.RepoKey), cmp.Compare(x.Path, y.Path),
				cmp.Compare(x.Name, y.Name), cmp.Compare(a.ID, b.ID))
		})
		var err error
		out, next, err = memtx.Paginate(rows, p, false, func(e model.TrashEntry) []any {
			return []any{e.Record.Item.RepoKey, e.Record.Item.Path, e.Record.Item.Name, e.ID}
		})
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (t trash) Delete(ctx context.Context, id int64) error {
	return t.s.Do(ctx, func() error {
		if !t.t().Del(id) {
			return memtx.NotFound("trash entry", id)
		}
		return nil
	})
}

func (t trash) Expired(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]model.TrashEntry, error) {
	var out []model.TrashEntry
	err := t.s.Do(ctx, func() error {
		cutoff := before.UnixMicro()
		out = t.t().All(func(_ int64, e model.TrashEntry) bool {
			return !e.DeletedAt.IsZero() && e.DeletedAt.UnixMicro() < cutoff
		}, func(a, b model.TrashEntry) int {
			return cmp.Or(cmp.Compare(deletedKey(a), deletedKey(b)), cmp.Compare(a.ID, b.ID))
		})
		out = out[:min(len(out), limitOr(limit))]
		return nil
	})
	return out, err
}

// --- Package index ---------------------------------------------------------

// Packages returns the package store: the package index of decision 0018.
func (s *Store) Packages() port.PackageStore { return packages{s} }

type packages struct{ s *Store }

func (p packages) t() *memtx.Table[int64, model.Package] {
	return memtx.TableOf[int64, model.Package](p.s.Store, "packages")
}

func (p packages) versions() *memtx.Table[int64, model.PackageVersion] {
	return memtx.TableOf[int64, model.PackageVersion](p.s.Store, "package_versions")
}

func (p packages) files() *memtx.Table[model.VersionFile, bool] {
	return memtx.TableOf[model.VersionFile, bool](p.s.Store, "package_version_files")
}

// find returns the package (repoKey, name); call it under the lock.
func (p packages) find(repoKey, name string) (model.Package, bool) {
	rows := p.t().
		All(func(_ int64, v model.Package) bool { return v.RepoKey == repoKey && v.Name == name }, nil)
	if len(rows) == 0 {
		return model.Package{}, false
	}
	return rows[0], true
}

func (p packages) Ensure(ctx context.Context, pkg model.Package) (model.Package, error) {
	if pkg.RepoKey == "" || pkg.Name == "" {
		return pkg, fmt.Errorf(
			"ensure package: repository and name are required: %w",
			kernel.ErrInvalid,
		)
	}
	var out model.Package
	err := p.s.Do(ctx, func() error {
		if cur, ok := p.find(pkg.RepoKey, pkg.Name); ok {
			out = cur
			return nil
		}
		pkg.ID = p.s.NextID("packages")
		p.t().Put(pkg.ID, pkg)
		out, _ = p.t().Get(pkg.ID)
		return nil
	})
	return out, err
}

func (p packages) Get(ctx context.Context, id int64) (model.Package, error) {
	var out model.Package
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.t().Get(id); !ok {
			return memtx.NotFound("package", id)
		}
		return nil
	})
	return out, err
}

func (p packages) Find(ctx context.Context, repoKey, name string) (model.Package, error) {
	var out model.Package
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.find(repoKey, name); !ok {
			return memtx.NotFound("package", repoKey+":"+name)
		}
		return nil
	})
	return out, err
}

func (p packages) List(
	ctx context.Context,
	repoKey string,
	pg kernel.Page,
) ([]model.Package, string, error) {
	var (
		out  []model.Package
		next string
	)
	err := p.s.Do(ctx, func() error {
		rows := p.t().All(func(_ int64, v model.Package) bool { return v.RepoKey == repoKey },
			func(a, b model.Package) int { return cmp.Compare(a.Name, b.Name) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			pg,
			false,
			func(v model.Package) []any { return []any{v.Name} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (p packages) Delete(ctx context.Context, id int64) error {
	return p.s.Do(ctx, func() error {
		if !p.t().Del(id) {
			return memtx.NotFound("package", id)
		}
		for _, v := range p.versions().All(func(_ int64, v model.PackageVersion) bool { return v.PackageID == id }, nil) {
			p.removeVersion(v.ID)
		}
		return nil
	})
}

// removeVersion deletes a version and its file links; call it under the
// lock.
func (p packages) removeVersion(id int64) {
	p.versions().Del(id)
	p.files().DeleteWhere(func(f model.VersionFile, _ bool) bool { return f.VersionID == id })
}

// sorted returns a package's versions ordered by order key, then version.
func (p packages) sorted(packageID int64) []model.PackageVersion {
	return p.versions().
		All(func(_ int64, v model.PackageVersion) bool { return v.PackageID == packageID },
			func(a, b model.PackageVersion) int {
				return cmp.Or(
					cmp.Compare(a.OrderKey, b.OrderKey),
					cmp.Compare(a.Version, b.Version),
				)
			})
}

// reorder recomputes the order keys of a package's versions; call it under
// the lock.
func (p packages) reorder(pkg model.Package) {
	vs := p.sorted(pkg.ID)
	model.AssignOrderKeys(model.ComparatorFor(pkg.Type), vs)
	for _, v := range vs {
		p.versions().Put(v.ID, v)
	}
}

func (p packages) PutVersion(
	ctx context.Context,
	v model.PackageVersion,
) (model.PackageVersion, error) {
	if v.Version == "" {
		return v, fmt.Errorf("put package version: version is required: %w", kernel.ErrInvalid)
	}
	var out model.PackageVersion
	err := p.s.Do(ctx, func() error {
		pkg, ok := p.t().Get(v.PackageID)
		if !ok {
			return memtx.NotFound("package", v.PackageID)
		}
		v.ID, v.OrderKey = 0, 0
		for _, cur := range p.sorted(pkg.ID) {
			if cur.Version == v.Version {
				v.ID, v.OrderKey = cur.ID, cur.OrderKey
			}
		}
		if v.ID == 0 {
			v.ID = p.s.NextID("package_versions")
		}
		if len(v.Metadata) == 0 {
			v.Metadata = nil
		}
		p.versions().Put(v.ID, v)
		p.reorder(pkg)
		out, _ = p.versions().Get(v.ID)
		return nil
	})
	return out, err
}

func (p packages) Version(
	ctx context.Context,
	packageID int64,
	version string,
) (model.PackageVersion, error) {
	var out model.PackageVersion
	err := p.s.Do(ctx, func() error {
		for _, v := range p.sorted(packageID) {
			if v.Version == version {
				out = v
				return nil
			}
		}
		return memtx.NotFound("package version", fmt.Sprintf("%d/%s", packageID, version))
	})
	return out, err
}

func (p packages) Versions(ctx context.Context, packageID int64) ([]model.PackageVersion, error) {
	var out []model.PackageVersion
	err := p.s.Do(ctx, func() error {
		out = p.sorted(packageID)
		return nil
	})
	return out, err
}

func (p packages) DeleteVersion(ctx context.Context, id int64) error {
	return p.s.Do(ctx, func() error {
		v, ok := p.versions().Get(id)
		if !ok {
			return memtx.NotFound("package version", id)
		}
		p.removeVersion(id)
		if pkg, ok := p.t().Get(v.PackageID); ok {
			p.reorder(pkg)
		}
		return nil
	})
}

func (p packages) LinkFile(ctx context.Context, f model.VersionFile) error {
	return p.s.Do(ctx, func() error {
		if !p.versions().Has(f.VersionID) {
			return memtx.NotFound("link file: package version", f.VersionID)
		}
		if !memtx.TableOf[int64, model.Item](p.s.Store, "items").Has(f.ItemID) {
			return memtx.NotFound("link file: item", f.ItemID)
		}
		p.files().Put(f, true)
		return nil
	})
}

func (p packages) Files(ctx context.Context, versionID int64) ([]int64, error) {
	var out []int64
	err := p.s.Do(ctx, func() error {
		for f := range p.files().Rows {
			if f.VersionID == versionID {
				out = append(out, f.ItemID)
			}
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

func (p packages) VersionOf(ctx context.Context, itemID int64) (model.PackageVersion, error) {
	var out model.PackageVersion
	err := p.s.Do(ctx, func() error {
		var ids []int64
		for f := range p.files().Rows {
			if f.ItemID == itemID {
				ids = append(ids, f.VersionID)
			}
		}
		if len(ids) == 0 {
			return memtx.NotFound("version of item", itemID)
		}
		out, _ = p.versions().Get(slices.Min(ids))
		return nil
	})
	return out, err
}

func (p packages) RecordVersionDownload(ctx context.Context, versionID int64, at time.Time) error {
	return p.s.Do(ctx, func() error {
		v, ok := p.versions().Get(versionID)
		if !ok {
			return memtx.NotFound("package version", versionID)
		}
		v.LastDownloaded = at
		p.versions().Put(versionID, v)
		return nil
	})
}
