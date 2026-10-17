package maven

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// files is the AQL criterion for c's files.
func files(c repo.Config) aql.And {
	return aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: c.Key}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}
}

// under is the criterion that an item's full path starts with one of the
// prefixes. Both $match patterns may also match deeper or longer paths,
// which Read then turns away.
func under(prefixes []string) aql.Or {
	either := make(aql.Or, 0, 2*len(prefixes))
	for _, p := range prefixes {
		dir, rest := types.RootPath, p
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir, rest = p[:i], p[i+1:]
		}
		either = append(either,
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: p + "*"},
			aql.And{
				&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpEq, Text: dir},
				&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: rest + "*"},
			},
		)
	}
	return either
}

// versionFiles returns the files of co's version in c: those under the
// prefixes the layout renders for its organisation, module and base
// revision that read as the same package and version. A layout that keeps
// a version in a folder of its own searches that folder; one that does not
// (Maven 1, whose files sit together by type) searches more and keeps less.
func versionFiles(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	r reader,
	cached bool,
	co Coordinates,
) ([]types.Item, error) {
	prefixes := r.l.Prefixes(layout.Values{
		layout.TokenOrg: co.Org, layout.TokenModule: co.Module, layout.TokenBaseRev: co.BaseRev,
	})
	q := &aql.Query{Criteria: append(files(c), under(prefixes))}
	hits, err := st.Items().Search(ctx, q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("maven: files of %s %s: %w", co.Package, co.Version, err)
	}
	var out []types.Item
	for _, h := range hits {
		got, ok := r.read(h.Item.FullPath())
		if ok && h.Item.Cached == cached && got.Package == co.Package && got.Version == co.Version {
			out = append(out, h.Item)
		}
	}
	return out, nil
}

// indexedVersion is version v as the index records it: dated by its
// earliest file, which an overwrite keeps, and linking every file.
func indexedVersion(v string, its []types.Item) pkgindex.IndexedVersion {
	out := pkgindex.IndexedVersion{Version: v}
	for _, it := range its {
		out.Created = earliest(out.Created, it.Created)
		out.Files = append(out.Files, it.ID)
	}
	slices.Sort(out.Files)
	return out
}

// syncVersion makes co's version in c what the tree says: indexed with its
// files while it has any, and gone, with its package once that has no
// version left, when it has none.
func syncVersion(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	r reader,
	cached bool,
	co Coordinates,
) error {
	its, err := versionFiles(ctx, st, c, r, cached, co)
	if err != nil {
		return err
	}
	if len(its) == 0 {
		return dropVersion(ctx, st, c, co)
	}
	v := indexedVersion(co.Version, its)
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: co.Package, Type: c.PackageType, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("maven: package %s: %w", co.Package, err)
	}
	_, err = pkgindex.Sync(ctx, st, p, v)
	return err //nolint:wrapcheck // pkgindex wraps its own
}

// dropVersion removes co's version from c's index, and its package when no
// version is left. The item service prunes a version whose last file it
// removed before Deleted runs, so either may already be gone.
func dropVersion(ctx context.Context, st store.Store, c repo.Config, co Coordinates) error {
	p, err := st.Packages().Find(ctx, c.Key, co.Package)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("maven: package %s: %w", co.Package, err)
	}
	pv, err := st.Packages().Version(ctx, p.ID, co.Version)
	switch {
	case err == nil:
		if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
			return fmt.Errorf("maven: drop %s %s: %w", co.Package, co.Version, err)
		}
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("maven: version %s of %s: %w", co.Version, co.Package, err)
	}
	rest, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("maven: versions of %s: %w", co.Package, err)
	}
	if len(rest) > 0 {
		return nil
	}
	if err := st.Packages().Delete(ctx, p.ID); err != nil {
		return fmt.Errorf("maven: drop package %s: %w", co.Package, err)
	}
	return nil
}

// reindex brings c's package index back in line with its tree, which stays
// the source of truth (decision 0018): every file the layout reads joins
// its version, and pkgindex.Reconcile drops what the tree no longer backs,
// so after the hooks' own maintenance nothing changes. A package indexed
// afresh is dated by its earliest version. Without a layout nothing is read,
// so the index is emptied.
func reindex(ctx context.Context, st store.Store, c repo.Config, r reader) error {
	err := st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, &aql.Query{Criteria: files(c)}, aql.Options{})
		if err != nil {
			return fmt.Errorf("maven: files of %s: %w", c.Key, err)
		}
		byVersion := map[string]map[string][]types.Item{}
		for _, h := range hits {
			co, ok := r.read(h.Item.FullPath())
			if !ok {
				continue
			}
			if byVersion[co.Package] == nil {
				byVersion[co.Package] = map[string][]types.Item{}
			}
			byVersion[co.Package][co.Version] = append(byVersion[co.Package][co.Version], h.Item)
		}
		want := make([]pkgindex.Indexed, 0, len(byVersion))
		for _, name := range slices.Sorted(maps.Keys(byVersion)) {
			p := pkgindex.Indexed{Name: name}
			for _, ver := range slices.Sorted(maps.Keys(byVersion[name])) {
				v := indexedVersion(ver, byVersion[name][ver])
				p.Versions = append(p.Versions, v)
				p.Created = earliest(p.Created, v.Created)
			}
			want = append(want, p)
		}
		return pkgindex.Reconcile(ctx, st, c, want) //nolint:wrapcheck // pkgindex wraps its own
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}
