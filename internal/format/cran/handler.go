package cran

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	KindIndex   = "cran.index"
	PropName    = "cran.name"
	PropVersion = "cran.version"
	PropFeed    = "cran.feed"
)

type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !IsPackage(w.Path) {
		return inspection{}, nil
	}
	pkg, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, pkg: pkg}, nil
}

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("cran: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if _, _, ok := IndexPath(w.Path); ok && derives(c) && !w.Cached {
		return w, format.Reject(http.StatusConflict, "CRAN indexes are derived from packages")
	}
	if !IsPackage(w.Path) {
		if strings.HasSuffix(w.Path, ".tar.gz") || strings.HasSuffix(w.Path, ".tar.bz2") ||
			strings.HasSuffix(w.Path, ".tar.xz") || strings.HasSuffix(w.Path, ".tgz") ||
			strings.HasSuffix(w.Path, ".zip") {
			return w, format.Reject(http.StatusBadRequest, "Invalid CRAN package path")
		}
		return w, nil
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 ||
		in.path != w.Path {
		if _, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path); err != nil {
			return w, err
		}
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || !IsPackage(it.FullPath()) {
		return nil
	}
	pkg, err := packageFor(ctx, env, it)
	if err != nil {
		return err
	}
	if err := setProperties(ctx, env.Store, it, pkg); err != nil {
		return err
	}
	if err := syncVersion(ctx, env, c, pkg.Name, pkg.Version); err != nil {
		return err
	}
	return queue(ctx, env, c, it.Cached)
}

func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || !IsPackage(it.FullPath()) {
		return nil
	}
	return queue(ctx, env, c, it.Cached)
}

func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	seen := map[string]bool{}
	for _, it := range removed {
		if !IsPackage(it.FullPath()) {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		key := pkg.Name + "\x00" + pkg.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := syncVersion(ctx, env, c, pkg.Name, pkg.Version); err != nil {
			return err
		}
	}
	return nil
}

func derives(c repo.Config) bool { return c.Class == repo.Local || c.Class == repo.Federated }

func queue(ctx context.Context, env format.Env, c repo.Config, cached bool) error {
	if cached || !derives(c) {
		return nil
	}
	return env.Enqueue(ctx, KindIndex, c.Key, nil) //nolint:wrapcheck
}

func (Handler) WorkKinds() []string { return []string{KindIndex} }

func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if _, _, ok := IndexPath(p); ok && !cached && derives(c) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if _, _, ok := IndexPath(p); ok {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]types.Item{}
	for _, it := range items {
		if !IsPackage(it.FullPath()) {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if !env.SkipProperties {
			if err := setProperties(ctx, env.Store, it, pkg); err != nil {
				return err
			}
		}
		if by[pkg.Name] == nil {
			by[pkg.Name] = map[string][]types.Item{}
		}
		by[pkg.Name][pkg.Version] = append(by[pkg.Name][pkg.Version], it)
	}
	wanted := make([]pkgindex.Indexed, 0, len(by))
	for name, versions := range by {
		p := pkgindex.Indexed{Name: name}
		for version, its := range versions {
			v := indexedVersion(version, its)
			p.Versions = append(p.Versions, v)
			if p.Created.IsZero() || v.Created.Before(p.Created) {
				p.Created = v.Created
			}
		}
		slices.SortFunc(p.Versions, func(a, b pkgindex.IndexedVersion) int {
			return CompareVersion(a.Version, b.Version)
		})
		wanted = append(wanted, p)
	}
	slices.SortFunc(
		wanted,
		func(a, b pkgindex.Indexed) int { return strings.Compare(a.Name, b.Name) },
	)
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("cran: reconcile packages: %w", err)
	}
	return queue(ctx, env, c, pkgindex.Stored(c).Cached)
}

func (Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("cran: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c):
		return nil
	}
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	byFeed := map[string][]Package{}
	old := map[string]bool{}
	for _, it := range items {
		p := it.FullPath()
		if _, _, ok := IndexPath(p); ok {
			old[p] = true
			continue
		}
		if !IsPackage(p) {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if strings.Count(strings.TrimPrefix(p, pkg.Feed+"/"), "/") != 0 {
			// Archive/<name> files remain addressable but are not advertised
			// as the latest version in the terminal PACKAGES index.
			continue
		}
		pkg.SHA256, pkg.MD5, pkg.Size = it.Actual.SHA256, it.Actual.MD5, it.Size
		byFeed[pkg.Feed] = append(byFeed[pkg.Feed], pkg)
	}
	for feed, packages := range byFeed {
		generated, err := EncodeIndex(packages, feed)
		if err != nil {
			return err
		}
		for name, body := range generated {
			p := feed + "/" + name
			if _, err := env.Derive(ctx, format.Derived{
				Repo: c, Path: p, Body: bytes.NewReader(body), By: "_system_",
			}); err != nil {
				return fmt.Errorf("cran: derive %s: %w", p, err)
			}
			delete(old, p)
		}
	}
	for p := range old {
		if err := env.Underive(ctx, c, false, p); err != nil {
			return fmt.Errorf("cran: remove stale %s: %w", p, err)
		}
	}
	return nil
}

func packageFor(ctx context.Context, env format.Env, it types.Item) (Package, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == it.Actual.SHA256 &&
		in.path == it.FullPath() {
		return in.pkg, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
}

func readStored(ctx context.Context, blobs format.Blobs, sha, p string) (Package, error) {
	if sha == "" {
		return Package{}, format.Reject(http.StatusBadRequest, "CRAN package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("cran: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	pkg, err := ReadPackage(ctx, rc, info.Size, p)
	if err != nil {
		return Package{}, format.Reject(http.StatusBadRequest, "Invalid CRAN package: %v", err)
	}
	return pkg, nil
}

func setProperties(ctx context.Context, st store.Store, it types.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("cran: item properties: %w", err)
	}
	want := types.Properties{PropName: {pkg.Name}, PropVersion: {pkg.Version}, PropFeed: {pkg.Feed}}
	set := types.Properties{}
	for key, values := range want {
		if !slices.Equal(props[key], values) {
			set[key] = values
		}
	}
	if len(set) == 0 {
		return nil
	}
	return st.Items().PatchProperties(ctx, it.ID, set, nil, it.Updated) //nolint:wrapcheck
}

func syncVersion(ctx context.Context, env format.Env, c repo.Config, name, version string) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	var selected []types.Item
	for _, it := range items {
		if !IsPackage(it.FullPath()) {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if pkg.Name == name && pkg.Version == version {
			selected = append(selected, it)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	v := indexedVersion(version, selected)
	p, err := env.Store.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("cran: package %s: %w", name, err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, v)
	return err //nolint:wrapcheck
}

func indexedVersion(version string, items []types.Item) pkgindex.IndexedVersion {
	v := pkgindex.IndexedVersion{Version: version}
	for _, it := range items {
		if v.Created.IsZero() || it.Created.Before(v.Created) {
			v.Created = it.Created
		}
		v.Files = append(v.Files, it.ID)
	}
	slices.Sort(v.Files)
	return v
}

func files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("cran: files of %s: %w", c.Key, err)
	}
	out := make([]types.Item, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Item)
	}
	return out, nil
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.Worker     = Handler{}
	_ format.Classifier = Handler{}
	_ format.Reindexer  = Handler{}
)
