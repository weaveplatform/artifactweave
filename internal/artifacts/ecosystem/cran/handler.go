package cran

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// KindIndex is the work kind that rebuilds a local or federated
// repository's PACKAGES and PACKAGES.gz (Work), keyed by the repository.
// PropName, PropVersion and PropFeed are the item properties Deployed and
// Reindex set on every package from its path, which its DESCRIPTION must
// match. The work queue and the items keep these values, so they never
// change.
const (
	KindIndex   = "cran.index"
	PropName    = "cran.name"
	PropVersion = "cran.version"
	PropFeed    = "cran.feed"
)

// Handler is the CRAN format's hooks: it checks every R package archive
// written to a CRAN repository against its path and DESCRIPTION, keeps the
// package index and the cran.* properties in step with the packages, and
// derives a local or federated repository's PACKAGES and PACKAGES.gz for
// each feed as queued work (KindIndex).
type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

// Inspect reads and checks the stored content a write puts at a package
// path, before the write's transaction, and returns the package with its
// DESCRIPTION fields for Admit and Deployed, so an archive is read once
// per write. A checksum deploy and any other path inspect nothing. A write
// with no content digest, or whose archive is invalid or does not match
// its path, is refused with 400.
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

// Admit applies the generic deploy policy, wrapping its refusal, and
// passes a checksum deploy. A local or federated repository derives its
// own PACKAGES and PACKAGES.gz, so a write of either to one of its feeds is
// refused (409). A file with an R archive suffix (.tar.gz, .tar.bz2,
// .tar.xz, .tgz or .zip) at a path that is not a package path is refused
// (400), and a package must be a valid archive that matches its path
// (400): Admit repeats Inspect's check unless Inspect already read the
// same content at the same path. Any other file passes.
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
		return w, format.Rejectf(http.StatusConflict, "CRAN indexes are derived from packages")
	}
	if !IsPackage(w.Path) {
		if strings.HasSuffix(w.Path, ".tar.gz") || strings.HasSuffix(w.Path, ".tar.bz2") ||
			strings.HasSuffix(w.Path, ".tar.xz") || strings.HasSuffix(w.Path, ".tgz") ||
			strings.HasSuffix(w.Path, ".zip") {
			return w, format.Rejectf(http.StatusBadRequest, "Invalid CRAN package path")
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

// Deployed sets a stored package's cran.name, cran.version and cran.feed
// properties, brings its version in the package index up to date and, for
// a local or federated repository's own write, queues the index rebuild. A
// version's files are every package in the repository with that name and
// version, in any feed or folder, so Deployed reads them all rather than
// trusting their paths. Folders and other files change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
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

// Deleting queues the index rebuild when a local or federated repository's
// own package is about to be deleted, so the rebuild is asked for exactly
// when the delete commits. Folders and other files change nothing.
func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || !IsPackage(it.FullPath()) {
		return nil
	}
	return queue(ctx, env, c, it.Cached)
}

// Deleted re-syncs each removed package's version in the package index,
// once per name and version, from that version's remaining packages. A
// version with none left is not its concern: the item service has already
// pruned it.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []model.Item,
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

// WorkKinds returns KindIndex, the one kind Work runs.
func (Handler) WorkKinds() []string { return []string{KindIndex} }

// Owed makes a read of a local or federated repository's PACKAGES or
// PACKAGES.gz wait for its pending rebuild, so a client never reads an
// index that lags a completed write. A remote's cache owes nothing.
func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if _, _, ok := IndexPath(p); ok && !cached && derives(c) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

// Classify classes each feed's PACKAGES and PACKAGES.gz as derived, so
// replication and federation skip them and each side generates its own,
// and every other path as unique.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if _, _, ok := IndexPath(p); ok {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// Reindex rebuilds repository c's package index from its packages (a
// remote's from its cache): a package per name and a version per version,
// whose files are its archives in every feed and whose creation time is
// the oldest file's, versions in R's order (CompareVersion). It reads
// every package, failing on the first that is missing or invalid, and
// restores the cran.* properties unless env.SkipProperties is set. A
// local or federated repository's index rebuild is then queued.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]model.Item{}
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
			by[pkg.Name] = map[string][]model.Item{}
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

// Work runs KindIndex for the repository work.Key names: it derives each
// feed's PACKAGES and PACKAGES.gz from the packages stored directly in
// that feed, listing the newest version of each with its DESCRIPTION's
// dependency and build fields and the MD5 sum of its stored bytes
// (EncodeIndex), and removes the indexes of a feed left with no package.
// It ignores other kinds, a repository that no longer exists and one that
// is not a local or federated CRAN repository, and fails on the first
// package it cannot read.
func (Handler) Work(ctx context.Context, env format.Env, work work.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
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

func packageFor(ctx context.Context, env format.Env, it model.Item) (Package, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == it.Actual.SHA256 &&
		in.path == it.FullPath() {
		return in.pkg, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
}

func readStored(ctx context.Context, blobs format.Blobs, sha, p string) (Package, error) {
	if sha == "" {
		return Package{}, format.Rejectf(http.StatusBadRequest, "CRAN package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("cran: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	pkg, err := ReadPackage(ctx, rc, info.Size, p)
	if err != nil {
		return Package{}, format.Rejectf(http.StatusBadRequest, "Invalid CRAN package: %v", err)
	}
	return pkg, nil
}

func setProperties(ctx context.Context, st format.Store, it model.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("cran: item properties: %w", err)
	}
	want := model.Properties{PropName: {pkg.Name}, PropVersion: {pkg.Version}, PropFeed: {pkg.Feed}}
	set := model.Properties{}
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
	var selected []model.Item
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
	p, err := env.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("cran: package %s: %w", name, err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, v)
	return err //nolint:wrapcheck
}

func indexedVersion(version string, items []model.Item) pkgindex.IndexedVersion {
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

func files(ctx context.Context, st format.Store, c repo.Config) ([]model.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("cran: files of %s: %w", c.Key, err)
	}
	out := make([]model.Item, 0, len(hits))
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
