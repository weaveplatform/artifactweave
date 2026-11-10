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
// repository's feed metadata (Work), keyed by the repository. PropName,
// PropVersion, PropArchitecture and PropFeed are the item properties
// Deployed and Reindex set on every IPK from its control file and its
// folder, the feed. The work queue and the items keep these values, so
// they never change.
const (
	KindIndex        = "opkg.index"
	PropName         = "opkg.name"
	PropVersion      = "opkg.version"
	PropArchitecture = "opkg.architecture"
	PropFeed         = "opkg.feed"
)

// Serves reports whether t is the Opkg package type.
func Serves(t repo.PackageType) bool { return t == repo.Opkg }

// Handler is the Opkg format's hooks: it checks every IPK written to an
// Opkg repository, keeps the package index and opkg.* properties in step
// with the IPKs, and derives a local or federated repository's feed
// metadata as queued work (KindIndex), signed when the repository names a
// key pair.
type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

// Inspect reads and checks the stored IPK a write puts at a .ipk path,
// before the write's transaction, and returns its control metadata for
// Admit and Deployed, so an archive is read once per write. A checksum
// deploy and any other path inspect nothing. A write with no content
// digest, or whose IPK is invalid or does not match its path, is refused
// with 400.
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

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy. No write may put feed metadata (Packages,
// Packages.gz, Release or Packages.sig, in any folder) into a local or
// federated repository, which derives its own (409). A .ipk path must be
// valid (ValidPath, 400), and its IPK must pass the checks Inspect makes,
// which Admit repeats unless Inspect already read the same content at the
// same path. Any other file passes.
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

// Deployed sets a stored IPK's opkg.* properties, brings its version in
// the package index up to date and, for a local or federated repository's
// own write, queues the feed rebuild. A version's files are every IPK in
// the repository whose control file gives that name and version, in any
// feed, so Deployed reads them all rather than trusting their paths.
// Folders and other files change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
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

// Deleting queues the feed rebuild when a local or federated repository's
// own IPK is about to be deleted, so the rebuild is asked for exactly when
// the delete commits.
func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || !strings.HasSuffix(it.FullPath(), ".ipk") {
		return nil
	}
	return queue(ctx, env, c, it.Cached)
}

// Deleted re-syncs each removed IPK's version in the package index, once
// per name and version, from that version's remaining IPKs. A version with
// none left is not its concern: the item service has already pruned it.
func (Handler) Deleted(
	ctx context.Context, env format.Env, c repo.Config, removed []model.Item,
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

// WorkKinds returns KindIndex, the only work the format queues.
func (Handler) WorkKinds() []string { return []string{KindIndex} }

// Owed makes a read of a local or federated repository's feed metadata
// wait for its pending rebuild, so a client never reads a feed that lags a
// completed write. A remote's cache owes nothing.
func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if !cached && derives(c) && IsIndex(p) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

// Classify classes feed metadata as derived, so replication and
// federation skip it and each side generates its own, and every other path
// as unique.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if IsIndex(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// Reindex rebuilds repository c's package index from its IPKs (a
// remote's from its cache): a package per name and a version per version,
// whose files are its IPKs and whose creation time is the oldest file's.
// It reads every IPK, failing on the first that is missing or invalid, and
// restores the opkg.* properties unless Env.SkipProperties is set, as the
// reindex operation's writeProps=0 asks. A local or federated repository's
// feed rebuild is then queued.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]model.Item{}
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
		return fmt.Errorf("opkg: reconcile packages: %w", err)
	}
	return queue(ctx, env, c, pkgindex.Stored(c).Cached)
}

// Work runs one KindIndex item: it regenerates the feed metadata of the
// local or federated Opkg repository the item names, and ignores any other
// kind and a repository that is gone or no longer qualifies. Each feed's
// Packages, Packages.gz and Release are derived from the IPKs in its
// folder, with sizes and digests from the stored bytes, and Packages is
// signed (SignFeed) when the repository names a key pair; metadata no
// longer produced, such as that of a feed whose last IPK is gone, is
// removed.
// More than 100,000 IPKs fail the run. It reads the repository afresh, so
// a repeated run gives the same result.
func (Handler) Work(ctx context.Context, env format.Env, work work.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
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
		if err := SignFeed(ctx, env.Keys, c, generated); err != nil {
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

func signingKey(
	ctx context.Context,
	keys accessapi.Keys,
	c repo.Config,
) (accessapi.Keypair, error) {
	ref := c.PrimaryKeyPairRef
	if c.Class == repo.Virtual {
		ref = c.KeyPair
	}
	if ref == "" {
		return accessapi.Keypair{}, nil
	}
	pair, err := keys.Keypair(ctx, ref)
	if err != nil {
		return pair, fmt.Errorf("opkg: signing key: %w", err)
	}
	if pair.Type != accessapi.KeypairGPG {
		return pair, fmt.Errorf("%w: index signing requires gpg", errInvalidIndex)
	}
	return pair, nil
}

// SignFeed signs the uncompressed Packages bytes, which the opkg client
// verifies before trusting the package digests inside the feed, with the
// repository's GPG key pair read through keys: keyPair for a virtual
// repository, primaryKeyPairRef for any other. A repository that names no
// pair leaves files unsigned without reading keys.
func SignFeed(
	ctx context.Context,
	keys accessapi.Keys,
	c repo.Config,
	files map[string][]byte,
) error {
	pair, err := signingKey(ctx, keys, c)
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

func packageFor(ctx context.Context, env format.Env, it model.Item) (Package, error) {
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

func setProperties(ctx context.Context, st format.Store, it model.Item, pkg Package) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("opkg: item properties: %w", err)
	}
	want := model.Properties{
		PropName: {pkg.Name}, PropVersion: {pkg.Version},
		PropArchitecture: {pkg.Architecture}, PropFeed: {Feed(it.FullPath())},
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
	p, err := env.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("opkg: package %s: %w", name, err)
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
		return nil, fmt.Errorf("opkg: files of %s: %w", c.Key, err)
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
