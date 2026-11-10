package rpm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// KindIndex is the work kind that regenerates a local or federated
// repository's repodata (Work), keyed by the repository. PropName,
// PropVersion, PropRelease and PropArch are the item properties Deployed
// and Reindex set on every package from its header. The work queue and the
// items keep these values, so they never change.
const (
	KindIndex   = "rpm.index"
	PropName    = "rpm.name"
	PropVersion = "rpm.version"
	PropRelease = "rpm.release"
	PropArch    = "rpm.architecture"
)

// Serves reports whether t is the RPM package type.
func Serves(t repo.PackageType) bool { return t == repo.RPM }

// Handler is the RPM format's hooks: it checks every RPM package written
// to an RPM repository, keeps the package index and rpm.* properties in
// step with the packages, and derives a local or federated repository's
// repodata as queued work (KindIndex), signed when the repository names a
// key pair.
type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

// Inspect reads and checks the stored package a write puts at a .rpm path,
// before the write's transaction, and returns its header metadata for
// Admit and Deployed, so a package is read once per write. A checksum
// deploy and any other path inspect nothing. A write with no content
// digest, or whose package is invalid or does not match its path, is
// refused with 400.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".rpm") {
		return inspection{}, nil
	}
	pkg, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, pkg: pkg}, nil
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy. No write may put generated repodata into a
// local or federated repository, which derives its own (409): a path that
// begins with repodata/repomd.xml in any folder, such as the manifest's
// .asc and .key, or a primary index (-primary.xml.gz or -primary.xml.zst)
// under repodata/. A .rpm path must be clean and relative with safe folder
// names (400), and its package must pass the checks Inspect makes, which
// Admit repeats unless Inspect already read the same content at the same
// path. Any other file passes.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("rpm: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && generated(w.Path) {
		return w, format.Rejectf(http.StatusConflict, "RPM repodata is generated from packages")
	}
	if !strings.HasSuffix(w.Path, ".rpm") {
		return w, nil
	}
	if !validPath(w.Path) {
		return w, format.Rejectf(http.StatusBadRequest, "Invalid RPM package path")
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 ||
		in.path != w.Path {
		if _, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path); err != nil {
			return w, err
		}
	}
	return w, nil
}

// Deployed sets a stored package's rpm.* properties and rebuilds the
// repository's whole package index from its packages, reading each, as
// Reindex does; with calculateYumMetadata on, a local or federated
// repository then queues its repodata rebuild. Folders and other files
// change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || !strings.HasSuffix(it.FullPath(), ".rpm") {
		return nil
	}
	pkg, err := packageFor(ctx, env, it)
	if err != nil {
		return err
	}
	if err := setProperties(ctx, env.Store, it, pkg); err != nil {
		return err
	}
	return reindex(ctx, env, c, autoCalculate(c))
}

// Deleting does nothing: Deleted rebuilds the package index, and queues
// the repodata rebuild, once per delete, after the packages are gone.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted rebuilds the repository's package index from its remaining
// packages when a delete or move removed at least one package and, with
// calculateYumMetadata on, queues a local or federated repository's
// repodata rebuild. Removing only other files changes nothing.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []model.Item,
) error {
	for _, it := range removed {
		if strings.HasSuffix(it.FullPath(), ".rpm") {
			return reindex(ctx, env, c, autoCalculate(c))
		}
	}
	return nil
}

// WorkKinds returns KindIndex, the only work the format queues.
func (Handler) WorkKinds() []string { return []string{KindIndex} }

// Owed makes a read of a local or federated repository's generated
// repodata wait for its pending rebuild, so a client never reads repodata
// that lags a completed write. A remote's cache owes nothing.
func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if !cached && derives(c) && generated(p) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

// Classify classes generated repodata as derived, so replication and
// federation skip it and each side generates its own, and every other path
// as unique.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if generated(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// Reindex rebuilds repository c's package index from its packages (a
// remote's from its cache): a package per name, created with its oldest
// file, and a version per <version>-<release>.<architecture>, whose files
// are its packages. It reads every package, failing on the first that is
// missing or invalid, and restores the rpm.* properties. A local or
// federated repository's repodata rebuild is then queued whether or not
// calculateYumMetadata is on: the Yum calculation operation is how a
// repository without automatic calculation gets its repodata.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c, true)
}

func reindex(ctx context.Context, env format.Env, c repo.Config, queue bool) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	wanted := map[string]map[string]*pkgindex.IndexedVersion{}
	created := map[string]model.Item{}
	for _, it := range items {
		if !strings.HasSuffix(it.FullPath(), ".rpm") {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if err := setProperties(ctx, env.Store, it, pkg); err != nil {
			return err
		}
		version := pkg.Version + "-" + pkg.Release + "." + pkg.Architecture
		if wanted[pkg.Name] == nil {
			wanted[pkg.Name] = map[string]*pkgindex.IndexedVersion{}
			created[pkg.Name] = it
		}
		if wanted[pkg.Name][version] == nil {
			wanted[pkg.Name][version] = &pkgindex.IndexedVersion{
				Version: version,
				Created: it.Created,
			}
		}
		v := wanted[pkg.Name][version]
		v.Files = append(v.Files, it.ID)
		if it.Created.Before(v.Created) {
			v.Created = it.Created
		}
		if it.Created.Before(created[pkg.Name].Created) {
			created[pkg.Name] = it
		}
	}
	indexed := make([]pkgindex.Indexed, 0, len(wanted))
	for name, versions := range wanted {
		p := pkgindex.Indexed{Name: name, Created: created[name].Created}
		for _, v := range versions {
			slices.Sort(v.Files)
			p.Versions = append(p.Versions, *v)
		}
		slices.SortFunc(p.Versions, func(a, b pkgindex.IndexedVersion) int {
			return strings.Compare(a.Version, b.Version)
		})
		indexed = append(indexed, p)
	}
	slices.SortFunc(
		indexed,
		func(a, b pkgindex.Indexed) int { return strings.Compare(a.Name, b.Name) },
	)
	if err := pkgindex.Reconcile(ctx, env.Store, c, indexed); err != nil {
		return fmt.Errorf("rpm: reconcile package index: %w", err)
	}
	if derives(c) && queue {
		if err := env.Enqueue(ctx, KindIndex, c.Key, nil); err != nil {
			return fmt.Errorf("rpm: enqueue repodata rebuild: %w", err)
		}
	}
	return nil
}

// Work runs one KindIndex item: it regenerates the repodata of the local
// or federated RPM repository the item names, and ignores any other kind
// and a repository that is gone or no longer qualifies. Each metadata root
// (the folder yumRootDepth levels down) gets a primary index and
// repomd.xml from the packages below it, with digests from the stored
// bytes, signed with the repository's primaryKeyPairRef when it names one
// (repomd.xml.asc, with the public key as repomd.xml.key). repomd.xml is
// written last, so a reader never sees a manifest whose primary index or
// signature is missing. A root that still holds repodata but no packages
// gets empty repodata, and generated files no longer produced are removed.
// More than 100,000 packages fail the run. It reads the repository afresh,
// so a repeated run gives the same result.
func (Handler) Work(ctx context.Context, env format.Env, work work.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("rpm: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c):
		return nil
	}
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	byRoot := map[string][]Package{}
	old := map[string]bool{}
	count := 0
	for _, it := range items {
		p := it.FullPath()
		if generated(p) {
			old[p] = true
			root, _, _ := strings.Cut(p, "repodata/")
			if _, exists := byRoot[root]; !exists {
				byRoot[root] = nil
			}
			continue
		}
		if !strings.HasSuffix(p, ".rpm") {
			continue
		}
		root, ok := metadataRoot(p, c.YumRootDepth)
		if !ok {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		pkg.SHA256 = it.Actual.SHA256
		byRoot[root] = append(byRoot[root], pkg)
		count++
		if count > maxPackages {
			return fmt.Errorf("%w: too many packages in repository", errInvalidPackage)
		}
	}
	for root, packages := range byRoot {
		meta, err := EncodeMetadata(packages, root, env.Now())
		if err != nil {
			return err
		}
		if err := signMetadata(ctx, env.Keys, c, meta.Files); err != nil {
			return err
		}
		paths := make([]string, 0, len(meta.Files))
		for p := range meta.Files {
			paths = append(paths, p)
		}
		slices.SortFunc(paths, func(a, b string) int {
			// Publish the manifest last, after both referenced metadata and
			// detached signature are available to readers.
			if a == "repodata/repomd.xml" {
				return 1
			}
			if b == "repodata/repomd.xml" {
				return -1
			}
			return strings.Compare(a, b)
		})
		for _, p := range paths {
			full := root + p
			if _, err := env.Derive(ctx, format.Derived{
				Repo: c, Path: full, Body: bytes.NewReader(meta.Files[p]), By: "_system_",
			}); err != nil {
				return fmt.Errorf("rpm: derive %s: %w", full, err)
			}
			delete(old, full)
		}
	}
	for p := range old {
		if err := env.Underive(ctx, c, false, p); err != nil {
			return fmt.Errorf("rpm: remove stale %s: %w", p, err)
		}
	}
	return nil
}

func metadataRoot(p string, configured *int) (string, bool) {
	depth := 0
	if configured != nil {
		depth = *configured
	}
	parts := strings.Split(p, "/")
	if depth < 0 || depth >= len(parts) {
		return "", false
	}
	if depth == 0 {
		return "", true
	}
	return strings.Join(parts[:depth], "/") + "/", true
}

func generated(p string) bool {
	if strings.HasPrefix(p, "repodata/repomd.xml") ||
		strings.Contains(p, "/repodata/repomd.xml") {
		return true
	}
	return strings.Contains(p, "repodata/") && IsPrimaryMetadataPath(p)
}

func derives(c repo.Config) bool { return c.Class == repo.Local || c.Class == repo.Federated }

func autoCalculate(c repo.Config) bool {
	return c.CalculateYumMetadata != nil && *c.CalculateYumMetadata
}

func signMetadata(
	ctx context.Context,
	keys accessapi.Keys,
	c repo.Config,
	files map[string][]byte,
) error {
	if c.PrimaryKeyPairRef == "" {
		return nil
	}
	pair, err := keys.Keypair(ctx, c.PrimaryKeyPairRef)
	if err != nil {
		return fmt.Errorf("rpm: metadata signing key: %w", err)
	}
	if pair.Type != accessapi.KeypairGPG {
		return fmt.Errorf("%w: rpm metadata signing requires gpg", errInvalidPackage)
	}
	manifest := files["repodata/repomd.xml"]
	signature, err := SignRepomd(manifest, pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return err
	}
	files["repodata/repomd.xml.asc"] = signature
	files["repodata/repomd.xml.key"] = []byte(pair.PublicKey)
	return nil
}

func packageFor(ctx context.Context, env format.Env, it model.Item) (Package, error) {
	if in, ok := env.Inspection.(inspection); ok &&
		in.sha == it.Actual.SHA256 && in.path == it.FullPath() {
		return in.pkg, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
}

func readStored(ctx context.Context, blobs format.Blobs, sha, p string) (Package, error) {
	if sha == "" {
		return Package{}, format.Rejectf(http.StatusBadRequest, "RPM package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("rpm: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	pkg, err := ReadPackage(ctx, rc, info.Size, p)
	if err != nil {
		return Package{}, format.Rejectf(http.StatusBadRequest, "Invalid RPM package: %v", err)
	}
	return pkg, nil
}

func setProperties(ctx context.Context, st format.Store, it model.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("rpm: item properties: %w", err)
	}
	want := model.Properties{
		PropName: {pkg.Name}, PropVersion: {pkg.Version},
		PropRelease: {pkg.Release}, PropArch: {pkg.Architecture},
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

func files(ctx context.Context, st format.Store, c repo.Config) ([]model.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("rpm: files of %s: %w", c.Key, err)
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
