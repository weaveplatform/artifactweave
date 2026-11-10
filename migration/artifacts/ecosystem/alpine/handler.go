package alpine

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
)

// KindIndex is the work kind that regenerates a local or federated
// repository's indexes (Work), keyed by the repository. IndexName is the
// file name of an architecture folder's index,
// <branch>/<repository>/<architecture>/APKINDEX.tar.gz. PropName,
// PropVersion, PropBranch, PropComponent and PropArchitecture are the item
// properties Deployed and Reindex set on every package from its .PKGINFO
// and path; PropComponent holds the path's second folder, which Alpine
// calls the repository (main or community) and this package the
// component. The work queue and the items keep these values, so they never
// change.
const (
	KindIndex        = "alpine.index"
	IndexName        = "APKINDEX.tar.gz"
	PropName         = "alpine.name"
	PropVersion      = "alpine.version"
	PropBranch       = "alpine.branch"
	PropComponent    = "alpine.repository"
	PropArchitecture = "alpine.architecture"
)

// Serves reports whether t is the Alpine package type.
func Serves(t repo.PackageType) bool { return t == repo.Alpine }

// Handler is the Alpine format's hooks: it checks every APK v2 package
// written to an Alpine repository, keeps the package index and alpine.*
// properties in step with the packages, and derives a local or federated
// repository's indexes as queued work (KindIndex), signed when the
// repository names a key pair.
type Handler struct{ generic.Handler }

type inspection struct {
	sha         string
	path        string
	packageInfo Package
}

// Inspect reads and checks the stored package a write puts at a .apk path,
// before the write's transaction, and returns its metadata for Admit and
// Deployed, so a package is read once per write. A checksum deploy and any
// other path inspect nothing. A write with no content digest, or whose
// package is invalid or does not match its path, is refused with 400.
func (Handler) Inspect(
	ctx context.Context, env format.Env, _ repo.Config, w format.Write,
) (any, error) {
	if !strings.HasSuffix(w.Path, ".apk") || w.Op == format.OpChecksum {
		return inspection{}, nil
	}
	pkg, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, packageInfo: pkg}, nil
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy. No write may put an APKINDEX.tar.gz into a
// local or federated repository, which derives its own (409). A .apk path
// must be <branch>/<repository>/<architecture>/<name>-<version>.apk
// (400), and its package must pass the checks Inspect makes, which Admit
// repeats unless Inspect already read the same content at the same path.
// Any other file passes.
func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("alpine: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && strings.HasSuffix(w.Path, "/"+IndexName) {
		return w, format.Rejectf(http.StatusConflict, "Alpine index is generated from packages")
	}
	if !strings.HasSuffix(w.Path, ".apk") {
		return w, nil
	}
	if _, _, _, _, ok := ParsePath(w.Path); !ok {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Alpine package path must be branch/repository/architecture/name-version.apk",
		)
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 ||
		in.path != w.Path {
		if _, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path); err != nil {
			return w, err
		}
	}
	return w, nil
}

// Deployed sets a stored package's alpine.* properties, brings its version
// in the package index up to date and, for a local or federated
// repository's own write, queues the index rebuild. A version's files are
// every package in the repository whose .PKGINFO gives that name and
// version, so Deployed reads them all rather than trusting their paths.
// Folders and other files change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || !strings.HasSuffix(it.FullPath(), ".apk") {
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
// when the delete commits.
func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || !strings.HasSuffix(it.FullPath(), ".apk") {
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
		if !strings.HasSuffix(it.FullPath(), ".apk") {
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

func derives(c repo.Config) bool {
	return c.Class == repo.Local || c.Class == repo.Federated
}

func queue(ctx context.Context, env format.Env, c repo.Config, cached bool) error {
	if cached || !derives(c) {
		return nil
	}
	return env.Enqueue(ctx, KindIndex, c.Key, nil) //nolint:wrapcheck
}

// WorkKinds returns KindIndex, the only work the format queues.
func (Handler) WorkKinds() []string { return []string{KindIndex} }

// Owed makes a read of a local or federated repository's index wait for
// its pending rebuild, so a client never reads an index that lags a
// completed write. A remote's cache owes nothing.
func (Handler) Owed(c repo.Config, cached bool, path string) []format.WorkRef {
	if !cached && derives(c) && strings.HasSuffix(path, "/"+IndexName) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

// Classify classes every APKINDEX.tar.gz as derived, so replication and
// federation skip it and each side generates its own, and every other path
// as unique.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if strings.HasSuffix(path, "/"+IndexName) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// Reindex rebuilds repository c's package index from its packages (a
// remote's from its cache): a package per name and a version per version,
// whose files are its packages and whose creation time is the oldest
// file's. It reads every package, failing on the first that is missing or
// invalid, and restores the alpine.* properties. A local or federated
// repository's index rebuild is then queued.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]model.Item{}
	for _, it := range items {
		if !strings.HasSuffix(it.FullPath(), ".apk") {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if err := setProperties(ctx, env.Store, it, pkg); err != nil {
			return err
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
			return strings.Compare(a.Version, b.Version)
		})
		wanted = append(wanted, p)
	}
	slices.SortFunc(wanted, func(a, b pkgindex.Indexed) int {
		return strings.Compare(a.Name, b.Name)
	})
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("alpine: reconcile packages: %w", err)
	}
	return queue(ctx, env, c, pkgindex.Stored(c).Cached)
}

func packageFor(ctx context.Context, env format.Env, it model.Item) (Package, error) {
	if in, ok := env.Inspection.(inspection); ok &&
		in.sha == it.Actual.SHA256 && in.path == it.FullPath() {
		return in.packageInfo, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
}

func readStored(ctx context.Context, blobs format.Blobs, sha, path string) (Package, error) {
	if sha == "" {
		return Package{}, format.Rejectf(http.StatusBadRequest, "Alpine package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("alpine: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	pkg, err := ReadPackage(ctx, rc, info.Size, path)
	if err != nil {
		return Package{}, format.Rejectf(http.StatusBadRequest, "Invalid Alpine package: %v", err)
	}
	return pkg, nil
}

func setProperties(ctx context.Context, st format.Store, it model.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("alpine: properties: %w", err)
	}
	want := model.Properties{
		PropName: {pkg.Name}, PropVersion: {pkg.Version},
		PropBranch: {pkg.Branch}, PropComponent: {pkg.Component},
		PropArchitecture: {pkg.Architecture},
	}
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
		if !strings.HasSuffix(it.FullPath(), ".apk") {
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
		return nil // the item service prunes the last version on delete
	}
	v := indexedVersion(version, selected)
	p, err := env.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("alpine: package %s: %w", name, err)
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
		return nil, fmt.Errorf("alpine: files of %s: %w", c.Key, err)
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
