package opkg

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	KindIndex        = "opkg.index"
	PropName         = "opkg.name"
	PropVersion      = "opkg.version"
	PropArchitecture = "opkg.architecture"
	PropFeed         = "opkg.feed"
)

func Serves(t repo.PackageType) bool { return t == repo.Opkg }

type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

func (Handler) Inspect(
	ctx context.Context, env format.Env, _ repo.Config, w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".ipk") {
		return inspection{}, nil
	}
	pkg, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, pkg: pkg}, nil
}

func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("opkg: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && IsIndex(w.Path) {
		return w, format.Rejectf(
			http.StatusConflict,
			"Opkg feed metadata is generated from IPK packages",
		)
	}
	if !strings.HasSuffix(w.Path, ".ipk") {
		return w, nil
	}
	if !ValidPath(w.Path) {
		return w, format.Rejectf(http.StatusBadRequest, "Invalid Opkg package path")
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
	if it.Folder || !strings.HasSuffix(it.FullPath(), ".ipk") {
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
	if it.Folder || !strings.HasSuffix(it.FullPath(), ".ipk") {
		return nil
	}
	return queue(ctx, env, c, it.Cached)
}

func (Handler) Deleted(
	ctx context.Context, env format.Env, c repo.Config, removed []types.Item,
) error {
	seen := map[string]bool{}
	for _, it := range removed {
		if !strings.HasSuffix(it.FullPath(), ".ipk") {
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
	if !cached && derives(c) && IsIndex(p) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if IsIndex(p) {
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
		if !strings.HasSuffix(it.FullPath(), ".ipk") {
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
			return strings.Compare(a.Version, b.Version)
		})
		wanted = append(wanted, p)
	}
	slices.SortFunc(wanted, func(a, b pkgindex.Indexed) int {
		return strings.Compare(a.Name, b.Name)
	})
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("opkg: reconcile packages: %w", err)
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
		return fmt.Errorf("opkg: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c):
		return nil
	}
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	byFeed := map[string][]Package{}
	old := map[string]bool{}
	count := 0
	for _, it := range items {
		p := it.FullPath()
		if IsIndex(p) {
			old[p] = true
			continue
		}
		if !strings.HasSuffix(p, ".ipk") {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		pkg.SHA256, pkg.MD5, pkg.Size = it.Actual.SHA256, it.Actual.MD5, it.Size
		byFeed[Feed(p)] = append(byFeed[Feed(p)], pkg)
		count++
		if count > maxIndexEntries {
			return fmt.Errorf("%w: too many packages", errInvalidIndex)
		}
	}
	for feed, packages := range byFeed {
		generated, err := EncodeIndex(packages, feed)
		if err != nil {
			return err
		}
		if err := SignFeed(ctx, env.Store, c, generated); err != nil {
			return err
		}
		for name, body := range generated {
			p := IndexPath(feed, name)
			if _, err := env.Derive(ctx, format.Derived{
				Repo: c, Path: p, Body: bytes.NewReader(body), By: "_system_",
			}); err != nil {
				return fmt.Errorf("opkg: derive %s: %w", p, err)
			}
			delete(old, p)
		}
	}
	for p := range old {
		if err := env.Underive(ctx, c, false, p); err != nil {
			return fmt.Errorf("opkg: remove stale %s: %w", p, err)
		}
	}
	return nil
}

func signingKey(ctx context.Context, st store.Store, c repo.Config) (types.Keypair, error) {
	ref := c.PrimaryKeyPairRef
	if c.Class == repo.Virtual {
		ref = c.KeyPair
	}
	if ref == "" {
		return types.Keypair{}, nil
	}
	pair, err := st.Keys().Keypair(ctx, ref)
	if err != nil {
		return pair, fmt.Errorf("opkg: signing key: %w", err)
	}
	if pair.Type != types.KeypairGPG {
		return pair, fmt.Errorf("%w: index signing requires gpg", errInvalidIndex)
	}
	return pair, nil
}

// SignFeed signs the uncompressed Packages bytes, which the opkg client
// verifies before trusting the package digests inside the feed.
func SignFeed(ctx context.Context, st store.Store, c repo.Config, files map[string][]byte) error {
	pair, err := signingKey(ctx, st, c)
	if err != nil || pair.Name == "" {
		return err
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pair.PrivateKey))
	if err != nil || len(ring) == 0 || ring[0].PrivateKey == nil {
		return fmt.Errorf("%w: unusable gpg signing key", errInvalidIndex)
	}
	if ring[0].PrivateKey.Encrypted {
		if err := ring[0].DecryptPrivateKeys([]byte(pair.Passphrase)); err != nil {
			return fmt.Errorf("opkg: unlock signing key: %w", err)
		}
	}
	var signature bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&signature, ring[0],
		bytes.NewReader(files[PackagesName]),
		&packet.Config{DefaultHash: crypto.SHA256}); err != nil {
		return fmt.Errorf("opkg: sign Packages: %w", err)
	}
	files[SignatureName] = signature.Bytes()
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
		return Package{}, format.Rejectf(http.StatusBadRequest, "Opkg package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("opkg: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	pkg, err := ReadPackage(ctx, rc, info.Size, p)
	if err != nil {
		return Package{}, format.Rejectf(http.StatusBadRequest, "Invalid Opkg package: %v", err)
	}
	return pkg, nil
}

func setProperties(ctx context.Context, st store.Store, it types.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("opkg: item properties: %w", err)
	}
	want := types.Properties{
		PropName: {pkg.Name}, PropVersion: {pkg.Version},
		PropArchitecture: {pkg.Architecture}, PropFeed: {Feed(it.FullPath())},
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

func syncVersion(ctx context.Context, env format.Env, c repo.Config, name, version string) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	var selected []types.Item
	for _, it := range items {
		if !strings.HasSuffix(it.FullPath(), ".ipk") {
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
		return fmt.Errorf("opkg: package %s: %w", name, err)
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
		return nil, fmt.Errorf("opkg: files of %s: %w", c.Key, err)
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
