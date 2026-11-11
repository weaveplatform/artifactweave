package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Items returns the item store: items in the canonical form of decision
// 0004, with their properties and download statistics, and the AQL item
// search (decision 0012).
func (s *Store) Items() port.ItemStore { return items{s} }

type items struct{ s *Store }

// itemKey is an item's natural key, indexed so lookups by path do not scan.
type itemKey struct {
	repo     string
	isCached bool
	path     string
	name     string
}

func keyOf(it model.Item) itemKey { return itemKey{it.RepoKey, it.Cached, it.Path, it.Name} }

func (i items) t() *memtx.Table[int64, model.Item] {
	return memtx.TableOf[int64, model.Item](i.s.Store, "items")
}

func (i items) byPath() *memtx.Table[itemKey, int64] {
	return memtx.TableOf[itemKey, int64](i.s.Store, "items_by_path")
}

func (i items) props() *memtx.Table[int64, model.Properties] {
	return memtx.TableOf[int64, model.Properties](i.s.Store, "item_properties")
}

func (i items) stats() *memtx.Table[int64, model.ItemStats] {
	return memtx.TableOf[int64, model.ItemStats](i.s.Store, "item_stats")
}

// canonical derives Path, Name and Depth from the item's full path.
func canonical(it model.Item) (model.Item, error) {
	if it.RepoKey == "" {
		return it, fmt.Errorf("item: repository key is required: %w", kernel.ErrInvalid)
	}
	parent, name, depth, err := kernel.SplitItemPath(it.FullPath())
	if err != nil {
		return it, fmt.Errorf("item: %w: %w", kernel.ErrInvalid, err)
	}
	it.Path, it.Name, it.Depth = parent, name, depth
	return it, nil
}

// folderPath normalises a folder argument: "" and "." are the root.
func folderPath(folder string) string {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return kernel.RootPath
	}
	return folder
}

// below reports whether an item with parent path sits anywhere under folder.
func below(path, folder string) bool {
	return folder == kernel.RootPath || path == folder || strings.HasPrefix(path, folder+"/")
}

func (i items) Put(ctx context.Context, it model.Item) (model.Item, error) {
	return i.put(ctx, it, false)
}

func (i items) Create(ctx context.Context, it model.Item) (model.Item, error) {
	return i.put(ctx, it, true)
}

func (i items) put(ctx context.Context, it model.Item, create bool) (model.Item, error) {
	it, err := canonical(it)
	if err != nil {
		return it, err
	}
	err = i.s.Do(ctx, func() error {
		// Only the property methods write PropertiesModified, as the SQL
		// stores leave its column out of Put.
		it.PropertiesModified = time.Time{}
		if id, ok := i.byPath().Get(keyOf(it)); ok {
			if create {
				return fmt.Errorf("create item %s: %w", it.FullPath(), kernel.ErrConflict)
			}
			it.ID = id
			cur, _ := i.t().Get(id)
			it.PropertiesModified = cur.PropertiesModified
		} else {
			it.ID = i.s.NextID("items")
			i.byPath().Put(keyOf(it), it.ID)
		}
		i.t().Put(it.ID, it)
		it, _ = i.t().Get(it.ID)
		return nil
	})
	return it, err
}

// lookup returns the item at fullPath; call it under the lock.
func (i items) lookup(repoKey string, cached bool, fullPath string) (model.Item, error) {
	parent, name, _, err := kernel.SplitItemPath(fullPath)
	if err == nil {
		if id, ok := i.byPath().Get(itemKey{repoKey, cached, parent, name}); ok {
			it, _ := i.t().Get(id)
			return it, nil
		}
	}
	//nolint:wrapcheck // wraps kernel.ErrNotFound
	return model.Item{}, memtx.NotFound("item", repoKey+":"+fullPath)
}

func (i items) Get(
	ctx context.Context,
	repoKey string,
	cached bool,
	fullPath string,
) (model.Item, error) {
	var out model.Item
	err := i.s.Do(ctx, func() error {
		var err error
		out, err = i.lookup(repoKey, cached, fullPath)
		return err
	})
	return out, err
}

func (i items) GetByID(ctx context.Context, id int64) (model.Item, error) {
	var out model.Item
	err := i.s.Do(ctx, func() error {
		var ok bool
		if out, ok = i.t().Get(id); !ok {
			return memtx.NotFound("item", id)
		}
		return nil
	})
	return out, err
}

// page lists the items matching keep, sorted by cmp and paged by key.
func (i items) page(
	ctx context.Context, p kernel.Page, keep func(model.Item) bool, order func(a, b model.Item) int,
	key func(model.Item) []any,
) ([]model.Item, string, error) {
	var (
		out  []model.Item
		next string
	)
	err := i.s.Do(ctx, func() error {
		rows := i.t().All(func(_ int64, it model.Item) bool { return keep(it) }, order)
		var err error
		out, next, err = memtx.Paginate(rows, p, false, key)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (i items) Children(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	folder = folderPath(folder)
	return i.page(
		ctx,
		p,
		func(it model.Item) bool {
			// The root's record has the root's path too, but is not inside it.
			return it.RepoKey == repoKey && it.Cached == cached && it.Path == folder && !it.IsRoot()
		},
		func(a, b model.Item) int { return cmp.Compare(a.Name, b.Name) },
		func(it model.Item) []any { return []any{it.Name} },
	)
}

func (i items) Descendants(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	folder = folderPath(folder)
	return i.page(
		ctx,
		p,
		func(it model.Item) bool {
			return it.RepoKey == repoKey && it.Cached == cached && below(it.Path, folder) &&
				!it.IsRoot()
		},
		func(a, b model.Item) int { return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Name, b.Name)) },
		func(it model.Item) []any { return []any{it.Path, it.Name} },
	)
}

func (i items) Delete(
	ctx context.Context, repoKey string, cached bool, fullPath string,
) (port.DeleteResult, error) {
	res := port.DeleteResult{BlobRefs: map[string]int64{}}
	err := i.s.Do(ctx, func() error {
		match := func(it model.Item) bool {
			return it.RepoKey == repoKey && it.Cached == cached && !it.IsRoot()
		}
		if folderPath(fullPath) != kernel.RootPath {
			target, err := i.lookup(repoKey, cached, fullPath)
			if err != nil {
				return err
			}
			full := target.FullPath()
			match = func(it model.Item) bool {
				return it.ID == target.ID ||
					(target.Folder && it.RepoKey == repoKey && it.Cached == cached && below(it.Path, full))
			}
		}
		for _, it := range i.t().All(func(_ int64, it model.Item) bool { return match(it) }, nil) {
			if it.Folder {
				res.Folders++
			} else {
				res.Files++
				res.Bytes += it.Size
				if it.Actual.SHA256 != "" {
					res.BlobRefs[it.Actual.SHA256]++
				}
			}
			i.remove(it)
		}
		return nil
	})
	if err != nil {
		return port.DeleteResult{}, err
	}
	return res, nil
}

// remove deletes an item with everything that hangs off it, as the SQL
// foreign keys cascade.
func (i items) remove(it model.Item) {
	i.t().Del(it.ID)
	i.byPath().Del(keyOf(it))
	i.props().Del(it.ID)
	i.stats().Del(it.ID)
	memtx.TableOf[model.VersionFile, bool](i.s.Store, "package_version_files").DeleteWhere(
		func(f model.VersionFile, _ bool) bool { return f.ItemID == it.ID })
}

func (i items) Root(ctx context.Context, repoKey string, cached bool) (model.Item, error) {
	var out model.Item
	err := i.s.Do(ctx, func() error {
		id, ok := i.byPath().Get(keyOf(model.RootItem(repoKey, cached)))
		if !ok {
			return memtx.NotFound("repository root", repoKey)
		}
		out, _ = i.t().Get(id)
		return nil
	})
	return out, err
}

func (i items) EnsureRoot(ctx context.Context, r model.Item) (model.Item, error) {
	if r.RepoKey == "" {
		return r, fmt.Errorf("repository root: repository key is required: %w", kernel.ErrInvalid)
	}
	root := model.RootItem(r.RepoKey, r.Cached)
	root.Created, root.CreatedBy = r.Created, r.CreatedBy
	root.Modified, root.ModifiedBy, root.Updated = r.Modified, r.ModifiedBy, r.Updated
	var out model.Item
	err := i.s.Do(ctx, func() error {
		id, ok := i.byPath().Get(keyOf(root))
		if !ok {
			id = i.s.NextID("items")
			root.ID = id
			i.byPath().Put(keyOf(root), id)
			i.t().Put(id, root)
		}
		out, _ = i.t().Get(id)
		return nil
	})
	return out, err
}

func (i items) DeleteRoot(ctx context.Context, repoKey string, cached bool) error {
	return i.s.Do(ctx, func() error {
		if id, ok := i.byPath().Get(keyOf(model.RootItem(repoKey, cached))); ok {
			it, _ := i.t().Get(id)
			i.remove(it)
		}
		return nil
	})
}

func (i items) FindByChecksum(
	ctx context.Context,
	c kernel.Checksums,
	p kernel.Page,
) ([]model.Item, string, error) {
	match, _, err := strongest(c)
	if err != nil {
		return nil, "", fmt.Errorf("find items: %w", err)
	}
	return i.page(ctx, p,
		func(it model.Item) bool { return !it.Folder && match(it.Actual) },
		func(a, b model.Item) int { return cmp.Compare(a.ID, b.ID) },
		func(it model.Item) []any { return []any{it.ID} })
}

func (i items) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	var out []aql.Hit
	err := i.s.Do(ctx, func() error {
		c := &aql.Corpus{Items: i.hits(true)}
		if q.Joins() {
			buildStore(i).addBuilds(c)
		}
		// Matching reads the stored rows in place; only the hits returned
		// are copied. Stats already points at a copy.
		out = aql.SelectItems(q, o, c)
		for n := range out {
			out[n].Properties = kernel.Clone(out[n].Properties)
		}
		return nil
	})
	return out, err
}

func (i items) Usage(ctx context.Context) ([]port.RepoUsage, error) {
	var out []port.RepoUsage
	err := i.s.Do(ctx, func() error {
		totals := map[itemKey]*port.RepoUsage{}
		for _, it := range i.t().Rows {
			if it.IsRoot() {
				continue
			}
			k := itemKey{repo: it.RepoKey, isCached: it.Cached}
			u, ok := totals[k]
			if !ok {
				u = &port.RepoUsage{RepoKey: it.RepoKey, Cached: it.Cached}
				totals[k] = u
			}
			if it.Folder {
				u.Folders++
			} else {
				u.Files++
				u.Bytes += it.Size
			}
		}
		for _, u := range totals {
			out = append(out, *u)
		}
		slices.SortFunc(out, func(a, b port.RepoUsage) int {
			return cmp.Or(cmp.Compare(a.RepoKey, b.RepoKey), compareBool(a.Cached, b.Cached))
		})
		return nil
	})
	return out, err
}

// compareBool orders false before true, as SQL does.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// requireItem fails with ErrNotFound when no item has id; call it under the
// lock.
func (i items) requireItem(id int64) error {
	if !i.t().Has(id) {
		return memtx.NotFound("item", id) //nolint:wrapcheck // wraps kernel.ErrNotFound
	}
	return nil
}

// cleanProperties is port.CleanProperties to model.PropertyValueMax.
func cleanProperties(props model.Properties) model.Properties {
	return port.CleanProperties(props, model.PropertyValueMax)
}

func (i items) Properties(ctx context.Context, id int64) (model.Properties, error) {
	var out model.Properties
	err := i.s.Do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		out, _ = i.props().Get(id)
		if out == nil {
			out = model.Properties{}
		}
		return nil
	})
	return out, err
}

// writeProperties stores props as the item's properties at the time at,
// stamping the item when they changed (port.ItemStore); call it under the
// lock.
func (i items) writeProperties(id int64, props model.Properties, at time.Time) {
	before, _ := i.props().Get(id)
	props = cleanProperties(props)
	i.props().Put(id, props)
	if props.Equal(before) {
		return
	}
	it, _ := i.t().Get(id)
	it.PropertiesModified = at
	if len(props) == 0 {
		it.PropertiesModified = time.Time{}
	}
	i.t().Put(id, it)
}

func (i items) SetProperties(
	ctx context.Context,
	id int64,
	props model.Properties,
	at time.Time,
) error {
	if err := port.CheckPropertyKeys(props); err != nil {
		return fmt.Errorf("properties of item %d: %w", id, err)
	}
	return i.s.Do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		i.writeProperties(id, props, at)
		return nil
	})
}

func (i items) PatchProperties(
	ctx context.Context,
	id int64,
	set model.Properties,
	remove []string,
	at time.Time,
) error {
	if err := port.CheckPropertyKeys(set); err != nil {
		return fmt.Errorf("properties of item %d: %w", id, err)
	}
	return i.s.Do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		cur, _ := i.props().Get(id)
		if cur == nil {
			cur = model.Properties{}
		}
		for _, k := range remove {
			delete(cur, k)
		}
		for k, v := range set {
			cur[k] = v
		}
		i.writeProperties(id, cur, at)
		return nil
	})
}

func (i items) Stats(ctx context.Context, id int64) (model.ItemStats, error) {
	var out model.ItemStats
	err := i.s.Do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		out, _ = i.stats().Get(id)
		return nil
	})
	return out, err
}

func (i items) RecordDownload(ctx context.Context, id int64, d port.Download) error {
	return i.s.Do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		st, _ := i.stats().Get(id)
		if d.Remote {
			st.RemoteDownloadCount++
			st.RemoteLastDownloaded, st.RemoteLastDownloadedBy = d.At, d.By
			st.RemoteOrigin, st.RemotePath = d.Origin, d.Path
		} else {
			st.DownloadCount++
			st.LastDownloaded, st.LastDownloadedBy = d.At, d.By
		}
		i.stats().Put(id, st)
		return nil
	})
}

func (i items) PutStats(ctx context.Context, id int64, st model.ItemStats) error {
	return i.s.Do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		i.stats().Put(id, st)
		return nil
	})
}
