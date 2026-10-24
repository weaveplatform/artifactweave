package rpm

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
	KindIndex   = "rpm.index"
	PropName    = "rpm.name"
	PropVersion = "rpm.version"
	PropRelease = "rpm.release"
	PropArch    = "rpm.architecture"
)

func Serves(t repo.PackageType) bool { return t == repo.RPM }

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
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".rpm") {
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
		return w, fmt.Errorf("rpm: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && generated(w.Path) {
		return w, format.Reject(http.StatusConflict, "RPM repodata is generated from packages")
	}
	if !strings.HasSuffix(w.Path, ".rpm") {
		return w, nil
	}
	if !validPath(w.Path) {
		return w, format.Reject(http.StatusBadRequest, "Invalid RPM package path")
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

func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	for _, it := range removed {
		if strings.HasSuffix(it.FullPath(), ".rpm") {
			return reindex(ctx, env, c, autoCalculate(c))
		}
	}
	return nil
}

func (Handler) WorkKinds() []string { return []string{KindIndex} }

func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if !cached && derives(c) && generated(p) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if generated(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c, true)
}

func reindex(ctx context.Context, env format.Env, c repo.Config, queue bool) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	wanted := map[string]map[string]*pkgindex.IndexedVersion{}
	created := map[string]types.Item{}
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

func (Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
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
		if err := signMetadata(ctx, env.Store, c, meta.Files); err != nil {
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
	st store.Store,
	c repo.Config,
	files map[string][]byte,
) error {
	if c.PrimaryKeyPairRef == "" {
		return nil
	}
	pair, err := st.Keys().Keypair(ctx, c.PrimaryKeyPairRef)
	if err != nil {
		return fmt.Errorf("rpm: metadata signing key: %w", err)
	}
	if pair.Type != types.KeypairGPG {
		return fmt.Errorf("%w: RPM metadata signing requires GPG", errInvalidPackage)
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

func packageFor(ctx context.Context, env format.Env, it types.Item) (Package, error) {
	if in, ok := env.Inspection.(inspection); ok &&
		in.sha == it.Actual.SHA256 && in.path == it.FullPath() {
		return in.pkg, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
}

func readStored(ctx context.Context, blobs format.Blobs, sha, p string) (Package, error) {
	if sha == "" {
		return Package{}, format.Reject(http.StatusBadRequest, "RPM package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("rpm: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	pkg, err := ReadPackage(ctx, rc, info.Size, p)
	if err != nil {
		return Package{}, format.Reject(http.StatusBadRequest, "Invalid RPM package: %v", err)
	}
	return pkg, nil
}

func setProperties(ctx context.Context, st store.Store, it types.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("rpm: item properties: %w", err)
	}
	want := types.Properties{
		PropName: {pkg.Name}, PropVersion: {pkg.Version},
		PropRelease: {pkg.Release}, PropArch: {pkg.Architecture},
	}
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

func files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("rpm: files of %s: %w", c.Key, err)
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
