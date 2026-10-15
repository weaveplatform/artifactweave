package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type items struct{ s *Store }

// itemKey is an item's natural key, indexed so lookups by path do not scan.
type itemKey struct {
	repo   string
	cached bool
	path   string
	name   string
}

func keyOf(it types.Item) itemKey { return itemKey{it.RepoKey, it.Cached, it.Path, it.Name} }

func (i items) t() *table[int64, types.Item] { return tableOf[int64, types.Item](i.s, "items") }

func (i items) byPath() *table[itemKey, int64] { return tableOf[itemKey, int64](i.s, "items_by_path") }

func (i items) props() *table[int64, types.Properties] {
	return tableOf[int64, types.Properties](i.s, "item_properties")
}

func (i items) stats() *table[int64, types.ItemStats] {
	return tableOf[int64, types.ItemStats](i.s, "item_stats")
}

// canonical derives Path, Name and Depth from the item's full path.
func canonical(it types.Item) (types.Item, error) {
	if it.RepoKey == "" {
		return it, fmt.Errorf("item: repository key is required: %w", store.ErrInvalid)
	}
	parent, name, depth, err := types.SplitItemPath(it.FullPath())
	if err != nil {
		return it, fmt.Errorf("item: %w: %w", store.ErrInvalid, err)
	}
	it.Path, it.Name, it.Depth = parent, name, depth
	return it, nil
}

// folderPath normalises a folder argument: "" and "." are the root.
func folderPath(folder string) string {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return types.RootPath
	}
	return folder
}

// below reports whether an item with parent path sits anywhere under folder.
func below(path, folder string) bool {
	return folder == types.RootPath || path == folder || strings.HasPrefix(path, folder+"/")
}

func (i items) Put(ctx context.Context, it types.Item) (types.Item, error) {
	it, err := canonical(it)
	if err != nil {
		return it, err
	}
	err = i.s.do(ctx, func() error {
		if id, ok := i.byPath().get(keyOf(it)); ok {
			it.ID = id
		} else {
			it.ID = i.s.nextID("items")
			i.byPath().put(keyOf(it), it.ID)
		}
		i.t().put(it.ID, it)
		it, _ = i.t().get(it.ID)
		return nil
	})
	return it, err
}

// lookup returns the item at fullPath; call it under the lock.
func (i items) lookup(repoKey string, cached bool, fullPath string) (types.Item, error) {
	parent, name, _, err := types.SplitItemPath(fullPath)
	if err == nil {
		if id, ok := i.byPath().get(itemKey{repoKey, cached, parent, name}); ok {
			it, _ := i.t().get(id)
			return it, nil
		}
	}
	return types.Item{}, notFound("item", repoKey+":"+fullPath)
}

func (i items) Get(
	ctx context.Context,
	repoKey string,
	cached bool,
	fullPath string,
) (types.Item, error) {
	var out types.Item
	err := i.s.do(ctx, func() error {
		var err error
		out, err = i.lookup(repoKey, cached, fullPath)
		return err
	})
	return out, err
}

func (i items) GetByID(ctx context.Context, id int64) (types.Item, error) {
	var out types.Item
	err := i.s.do(ctx, func() error {
		var ok bool
		if out, ok = i.t().get(id); !ok {
			return notFound("item", id)
		}
		return nil
	})
	return out, err
}

// page lists the items matching keep, sorted by cmp and paged by key.
func (i items) page(
	ctx context.Context, p store.Page, keep func(types.Item) bool, order func(a, b types.Item) int,
	key func(types.Item) []any,
) ([]types.Item, string, error) {
	var (
		out  []types.Item
		next string
	)
	err := i.s.do(ctx, func() error {
		rows := i.t().all(func(_ int64, it types.Item) bool { return keep(it) }, order)
		var err error
		out, next, err = paginate(rows, p, false, key)
		return err
	})
	return out, next, err
}

func (i items) Children(
	ctx context.Context, repoKey string, cached bool, folder string, p store.Page,
) ([]types.Item, string, error) {
	folder = folderPath(folder)
	return i.page(
		ctx,
		p,
		func(it types.Item) bool { return it.RepoKey == repoKey && it.Cached == cached && it.Path == folder },
		func(a, b types.Item) int { return cmp.Compare(a.Name, b.Name) },
		func(it types.Item) []any { return []any{it.Name} },
	)
}

func (i items) Descendants(
	ctx context.Context, repoKey string, cached bool, folder string, p store.Page,
) ([]types.Item, string, error) {
	folder = folderPath(folder)
	return i.page(
		ctx,
		p,
		func(it types.Item) bool {
			return it.RepoKey == repoKey && it.Cached == cached && below(it.Path, folder)
		},
		func(a, b types.Item) int { return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Name, b.Name)) },
		func(it types.Item) []any { return []any{it.Path, it.Name} },
	)
}

func (i items) Delete(
	ctx context.Context, repoKey string, cached bool, fullPath string,
) (store.DeleteResult, error) {
	res := store.DeleteResult{BlobRefs: map[string]int64{}}
	err := i.s.do(ctx, func() error {
		match := func(it types.Item) bool { return it.RepoKey == repoKey && it.Cached == cached }
		if folderPath(fullPath) != types.RootPath {
			target, err := i.lookup(repoKey, cached, fullPath)
			if err != nil {
				return err
			}
			full := target.FullPath()
			match = func(it types.Item) bool {
				return it.ID == target.ID ||
					(target.Folder && it.RepoKey == repoKey && it.Cached == cached && below(it.Path, full))
			}
		}
		for _, it := range i.t().all(func(_ int64, it types.Item) bool { return match(it) }, nil) {
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
		return store.DeleteResult{}, err
	}
	return res, nil
}

// remove deletes an item with everything that hangs off it, as the SQL
// foreign keys cascade.
func (i items) remove(it types.Item) {
	i.t().del(it.ID)
	i.byPath().del(keyOf(it))
	i.props().del(it.ID)
	i.stats().del(it.ID)
	tableOf[types.VersionFile, bool](i.s, "package_version_files").deleteWhere(
		func(f types.VersionFile, _ bool) bool { return f.ItemID == it.ID })
}

func (i items) FindByChecksum(
	ctx context.Context,
	c types.Checksums,
	p store.Page,
) ([]types.Item, string, error) {
	match, _, err := strongest(c)
	if err != nil {
		return nil, "", fmt.Errorf("find items: %w", err)
	}
	return i.page(ctx, p,
		func(it types.Item) bool { return !it.Folder && match(it.Actual) },
		func(a, b types.Item) int { return cmp.Compare(a.ID, b.ID) },
		func(it types.Item) []any { return []any{it.ID} })
}

func (i items) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	var out []aql.Hit
	err := i.s.do(ctx, func() error {
		rows := i.t().rows
		props := i.props().rows
		stats := i.stats().rows
		candidates := make([]aql.Hit, 0, len(rows))
		for id, it := range rows {
			h := aql.Hit{Item: it, Properties: props[id]}
			if st, ok := stats[id]; ok {
				h.Stats = &st
			}
			candidates = append(candidates, h)
		}
		// Matching reads the stored rows in place; only the hits returned
		// are copied. Stats already points at a copy.
		out = aql.Select(q, o, candidates)
		for n := range out {
			out[n].Properties = store.Clone(out[n].Properties)
		}
		return nil
	})
	return out, err
}

func (i items) Usage(ctx context.Context) ([]store.RepoUsage, error) {
	var out []store.RepoUsage
	err := i.s.do(ctx, func() error {
		totals := map[itemKey]*store.RepoUsage{}
		for _, it := range i.t().rows {
			k := itemKey{repo: it.RepoKey, cached: it.Cached}
			u, ok := totals[k]
			if !ok {
				u = &store.RepoUsage{RepoKey: it.RepoKey, Cached: it.Cached}
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
		slices.SortFunc(out, func(a, b store.RepoUsage) int {
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
	if !i.t().has(id) {
		return notFound("item", id)
	}
	return nil
}

// cleanProperties is how the stores keep properties: values sorted and
// unique, and keys without values dropped, because the property table has
// one row per value.
func cleanProperties(props types.Properties) types.Properties {
	out := types.Properties{}
	for k, v := range props {
		if len(v) > 0 {
			out.Set(k, v...)
		}
	}
	return out
}

func (i items) Properties(ctx context.Context, id int64) (types.Properties, error) {
	var out types.Properties
	err := i.s.do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		out, _ = i.props().get(id)
		if out == nil {
			out = types.Properties{}
		}
		return nil
	})
	return out, err
}

func (i items) SetProperties(ctx context.Context, id int64, props types.Properties) error {
	return i.s.do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		i.props().put(id, cleanProperties(props))
		return nil
	})
}

func (i items) PatchProperties(
	ctx context.Context,
	id int64,
	set types.Properties,
	remove []string,
) error {
	return i.s.do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		cur, _ := i.props().get(id)
		if cur == nil {
			cur = types.Properties{}
		}
		for _, k := range remove {
			delete(cur, k)
		}
		for k, v := range set {
			cur[k] = v
		}
		i.props().put(id, cleanProperties(cur))
		return nil
	})
}

func (i items) Stats(ctx context.Context, id int64) (types.ItemStats, error) {
	var out types.ItemStats
	err := i.s.do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		out, _ = i.stats().get(id)
		return nil
	})
	return out, err
}

func (i items) RecordDownload(ctx context.Context, id int64, d store.Download) error {
	return i.s.do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		st, _ := i.stats().get(id)
		if d.Remote {
			st.RemoteDownloadCount++
			st.RemoteLastDownloaded, st.RemoteLastDownloadedBy = d.At, d.By
			st.RemoteOrigin, st.RemotePath = d.Origin, d.Path
		} else {
			st.DownloadCount++
			st.LastDownloaded, st.LastDownloadedBy = d.At, d.By
		}
		i.stats().put(id, st)
		return nil
	})
}

func (i items) PutStats(ctx context.Context, id int64, st types.ItemStats) error {
	return i.s.do(ctx, func() error {
		if err := i.requireItem(id); err != nil {
			return err
		}
		i.stats().put(id, st)
		return nil
	})
}
