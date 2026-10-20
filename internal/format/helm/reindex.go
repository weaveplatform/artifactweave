package helm

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Reindex rebuilds the repository's Helm index from every chart archive in
// it, a remote repository's from its cache, and queues its index.yaml.
// Which repositories the JFrog reindex operation accepts is the API's rule,
// not the index's (internal/server, reindexOps).
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c, "")
}

// ReindexPath rebuilds the part of the index that the chart archives under
// p give: a folder, read to its last level, or one archive, as Helm's
// partial reindex names them (kubernetes-helm-chart-repositories, "Helm
// Charts Partial Re-Indexing"). A version some of whose archives lie
// elsewhere keeps them, as the index holds them. An empty path is the whole
// repository.
func (Handler) ReindexPath(ctx context.Context, env format.Env, c repo.Config, p string) error {
	return reindex(ctx, env, c, strings.Trim(p, "/"))
}

// found is a chart archive a reindex read, with the chart it holds.
type found struct {
	it    types.Item
	chart *Chart
}

// reindex brings the index of c, or the part of it the archives under
// under give, in line with the archives and sets their properties. The
// archives are read first, outside any transaction, so no decompression
// holds the store's write lock; the transaction then reads the tree again
// and reads only content that changed in between.
func reindex(ctx context.Context, env format.Env, c repo.Config, under string) error {
	st := env.Store
	hits, err := archivesUnder(ctx, st, c, under)
	if err != nil {
		return err
	}
	read := map[string]*Chart{}
	for _, it := range hits {
		if _, err := readOnce(ctx, env, read, it); err != nil {
			return err
		}
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		hits, err := archivesUnder(ctx, st, c, under)
		if err != nil {
			return err
		}
		var charts []found
		for _, it := range hits {
			ch, err := readOnce(ctx, env, read, it)
			if err != nil {
				return err
			}
			if ch == nil {
				continue
			}
			if err := setProperties(ctx, st, it, ch); err != nil {
				return err
			}
			charts = append(charts, found{it: it, chart: ch})
		}
		if under == "" {
			err = pkgindex.Reconcile(ctx, st, c, grouped(charts))
		} else {
			err = reindexPart(ctx, env, c, hits, charts)
		}
		if err != nil {
			return err //nolint:wrapcheck // pkgindex wraps its own; reindexPart is ours
		}
		return queueIndex(ctx, env, c, pkgindex.Stored(c).Cached)
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// archivesUnder lists the chart-named files of c that the index reads
// (pkgindex.Stored) at under or below it, all of them for "".
func archivesUnder(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	under string,
) ([]types.Item, error) {
	criteria := aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		aql.Or{
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: "*.tgz"},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: "*.tar.gz"},
		},
	}
	if under != "" {
		dir, name := path.Split(under)
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" {
			dir = types.RootPath
		}
		// AQL has no escape, so a * or ? in the path widens the match;
		// within narrows it again.
		criteria = append(criteria, aql.Or{
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpEq, Text: under},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: under + "/*"},
			aql.And{
				&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpEq, Text: dir},
				&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: name},
			},
		})
	}
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: criteria}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("helm: charts of %s: %w", c.Key, err)
	}
	out := make([]types.Item, 0, len(hits))
	for _, h := range hits {
		if p := h.Item.FullPath(); IsChartPath(p) && within(p, under) {
			out = append(out, h.Item)
		}
	}
	return out, nil
}

// within reports whether p is under, or lies below it.
func within(p, under string) bool {
	return under == "" || p == under || strings.HasPrefix(p, under+"/")
}

// readOnce reads the chart archive it, once per content: nil when it holds
// no chart.
func readOnce(
	ctx context.Context,
	env format.Env,
	read map[string]*Chart,
	it types.Item,
) (*Chart, error) {
	if ch, ok := read[it.Actual.SHA256]; ok {
		return ch, nil
	}
	ch, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256)
	if err != nil {
		return nil, err
	}
	read[it.Actual.SHA256] = ch
	return ch, nil
}

// versionKey names a version: a chart name and version.
type versionKey struct{ name, version string }

// grouped is the index charts give: a package per chart name, dated by its
// first archive, and a version per name and version, which links every
// archive of it and is described by its newest.
func grouped(charts []found) []pkgindex.Indexed {
	byVersion := map[versionKey][]found{}
	for _, f := range charts {
		k := versionKey{f.chart.Name, f.chart.Version}
		byVersion[k] = append(byVersion[k], f)
	}
	byName := map[string]*pkgindex.Indexed{}
	var names []string
	for k, fs := range byVersion {
		slices.SortFunc(fs, func(a, b found) int { return newestFirst(a.it, b.it) })
		newest := fs[0]
		files := make([]int64, 0, len(fs))
		first := newest.it
		for _, f := range fs {
			files = append(files, f.it.ID)
			if deployed(f.it).Before(deployed(first)) {
				first = f.it
			}
		}
		slices.Sort(files)
		p := byName[k.name]
		if p == nil {
			p = &pkgindex.Indexed{Name: k.name, Created: deployed(first)}
			byName[k.name] = p
			names = append(names, k.name)
		}
		p.Created = earliest(p.Created, deployed(first))
		p.Versions = append(p.Versions, pkgindex.IndexedVersion{
			Version: k.version, Created: deployed(newest.it),
			Metadata: metadataOf(newest.chart, newest.it).encode(), Files: files,
		})
	}
	slices.Sort(names)
	out := make([]pkgindex.Indexed, 0, len(names))
	for _, n := range names {
		out = append(out, *byName[n])
	}
	return out
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// reindexPart settles each version an archive under the path belongs to,
// or belonged to: hits are the chart-named files there and charts those of
// them that hold a chart. A version keeps the archives it links elsewhere
// and takes those under the path that hold it; one left with none goes.
func reindexPart(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	hits []types.Item,
	charts []found,
) error {
	st := env.Store
	inside := map[int64]bool{}
	affected := map[versionKey][]found{}
	for _, it := range hits {
		inside[it.ID] = true
		pv, ok, err := pkgindex.VersionOf(ctx, st, it.ID)
		if err != nil {
			return err //nolint:wrapcheck // pkgindex wraps its own
		}
		if !ok {
			continue
		}
		p, err := st.Packages().Get(ctx, pv.PackageID)
		if err != nil {
			return fmt.Errorf("helm: package %d: %w", pv.PackageID, err)
		}
		if k := (versionKey{p.Name, pv.Version}); affected[k] == nil {
			affected[k] = []found{}
		}
	}
	for _, f := range charts {
		k := versionKey{f.chart.Name, f.chart.Version}
		affected[k] = append(affected[k], f)
	}
	keys := make([]versionKey, 0, len(affected))
	for k := range affected {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b versionKey) int {
		return strings.Compare(a.name+"\x00"+a.version, b.name+"\x00"+b.version)
	})
	for _, k := range keys {
		if err := reindexVersion(ctx, env, c, k, affected[k], inside); err != nil {
			return err
		}
	}
	return nil
}

// reindexVersion settles the version k: its archives are those under the
// path that hold it, here, and those it links outside the path.
func reindexVersion(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	k versionKey,
	here []found,
	inside map[int64]bool,
) error {
	st := env.Store
	ids := make([]int64, 0, len(here))
	known := make(map[int64]*Chart, len(here))
	for _, f := range here {
		ids = append(ids, f.it.ID)
		known[f.it.ID] = f.chart
	}
	pv, ok, err := stored(ctx, st, c, k)
	if err != nil {
		return err
	}
	var prev *Metadata
	if ok {
		linked, err := st.Packages().Files(ctx, pv.ID)
		if err != nil {
			return fmt.Errorf("helm: files of %s %s: %w", k.name, k.version, err)
		}
		for _, id := range linked {
			if !inside[id] {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return drop(ctx, st, pv)
		}
		if prev, err = previous(pv); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// A package already indexed keeps its date; a new one is dated by its
	// first archive, as Reconcile dates it.
	created := env.Now()
	for _, f := range here {
		created = earliest(created, deployed(f.it))
	}
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: k.name, Type: c.PackageType, Created: created,
	})
	if err != nil {
		return fmt.Errorf("helm: package %s: %w", k.name, err)
	}
	return settle(ctx, env, p, k.version, ids, known, prev)
}

// stored is the version k of c as the index holds it; ok is false when it
// holds none.
func stored(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	k versionKey,
) (types.PackageVersion, bool, error) {
	p, err := st.Packages().Find(ctx, c.Key, k.name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return types.PackageVersion{}, false, nil
	case err != nil:
		return types.PackageVersion{}, false, fmt.Errorf("helm: package %s: %w", k.name, err)
	}
	pv, err := st.Packages().Version(ctx, p.ID, k.version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return pv, false, nil
	case err != nil:
		return pv, false, fmt.Errorf("helm: version %s of %s: %w", k.version, k.name, err)
	}
	return pv, true, nil
}
