package debian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// KindIndex is the work kind that rebuilds a local or federated
// repository's apt metadata (Work), keyed by the repository.
// KindReindexNoProps is the work kind of the reindex operation asked with
// writeProps=0: Work rebuilds the package index without rewriting the
// deb.name and deb.version properties, then queues KindIndex. Debian needs
// a kind of its own because the item service runs the shared
// format.KindReindexNoProps only for Opkg. The work queue keeps these
// values, so they never change.
const (
	KindIndex          = "debian.index"
	KindReindexNoProps = "debian.reindex-no-props"
)

var errInvalidCoordinates = errors.New("debian: invalid package coordinates")

type (
	snapshotWriteKey  struct{}
	snapshotDeleteKey struct{}
)

// WithSnapshotWrite marks server-owned metadata publication. A client cannot
// supply this context value through the HTTP or package protocol surface.
func WithSnapshotWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, snapshotWriteKey{}, true)
}

// WithSnapshotDelete permits deleting a complete snapshot folder.
func WithSnapshotDelete(ctx context.Context) context.Context {
	return context.WithValue(ctx, snapshotDeleteKey{}, true)
}

func snapshotPath(p string) bool {
	return p == "snapshots" || strings.HasPrefix(p, "snapshots/")
}

// Serves reports whether t is the Debian package type.
func Serves(t repo.PackageType) bool { return t == repo.Debian }

// Handler is the Debian format's hooks: it checks every binary (.deb,
// .ddeb) and source (.dsc) package written to a Debian repository and the
// coordinates it is deployed with, keeps the package index and deb.name and
// deb.version properties in step with the binaries, keeps snapshots
// immutable, and derives a local or federated repository's apt metadata as
// queued work (KindIndex), signed when the repository names a key pair.
type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	binary    Binary
}

type sourceInspection struct {
	sha, path string
	source    Source
}

// Inspect reads and checks the stored content a write puts at a .dsc,
// .deb or .ddeb path, before the write's transaction, and returns the
// parsed source or binary for Admit and Deployed, so a package is read
// once per write. A checksum deploy and any other path inspect nothing. A
// write with no content digest, or whose package is invalid, is refused
// with 400.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum {
		return inspection{}, nil
	}
	if isSourceDescriptor(w.Path) {
		source, err := readStoredSource(ctx, env.Blobs, w.Checksums.SHA256)
		if err != nil {
			return nil, err
		}
		return sourceInspection{sha: w.Checksums.SHA256, path: w.Path, source: source}, nil
	}
	if !isBinary(w.Path) {
		return inspection{}, nil
	}
	binary, err := readStored(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, binary: binary}, nil
}

// Admit refuses (409) a write under snapshots/ that the server's snapshot
// publication (WithSnapshotWrite) did not make, then applies the generic
// deploy policy, wrapping its refusal, and passes a checksum deploy. No
// write may put apt metadata under dists/ (anything in a binary-<arch> or
// source folder, a Release, InRelease or Release.gpg) into a local or
// federated repository, which derives its own (409). A .dsc needs a valid
// path in a repository without the trivial layout, and dsc.distribution and
// dsc.component properties with valid values unless a remote caches it
// without them (400). A .deb, or a .ddeb where ddebSupported is on, needs a
// valid path, and its deb.distribution, deb.component and deb.architecture
// properties must be all absent or all valid, each architecture the
// package's own unless it is Architecture: all (400). Admit repeats
// Inspect's checks unless Inspect already read the same content at the same
// path. Any other file passes.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if snapshotPath(w.Path) && ctx.Value(snapshotWriteKey{}) != true {
		return w, format.Rejectf(http.StatusConflict, "Debian snapshot metadata is immutable")
	}
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("debian: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && isGenerated(w.Path) {
		return w, format.Rejectf(http.StatusConflict, "Debian metadata is generated from packages")
	}
	if isSourceDescriptor(w.Path) {
		if !validIndexPath(w.Path) || c.DebianTrivialLayout != nil && *c.DebianTrivialLayout {
			return w, format.Rejectf(
				http.StatusBadRequest,
				"Invalid Debian source package path or layout",
			)
		}
		var source Source
		if in, ok := env.Inspection.(sourceInspection); ok &&
			in.sha == w.Checksums.SHA256 && in.path == w.Path {
			source = in.source
		} else {
			source, err = readStoredSource(ctx, env.Blobs, w.Checksums.SHA256)
			if err != nil {
				return w, err
			}
		}
		if err := sourceCoordinates(w.Properties, source, w.Cached); err != nil {
			return w, format.Rejectf(http.StatusBadRequest, "%v", err)
		}
		return w, nil
	}
	if !isBinary(w.Path) {
		return w, nil
	}
	if strings.HasSuffix(w.Path, ".ddeb") && (c.DdebSupported == nil || !*c.DdebSupported) {
		return w, format.Rejectf(http.StatusBadRequest, "Debian debug packages are disabled")
	}
	if !validPackagePath(w.Path) {
		return w, format.Rejectf(http.StatusBadRequest, "Invalid Debian package path")
	}
	var binary Binary
	if in, ok := env.Inspection.(inspection); ok &&
		in.sha == w.Checksums.SHA256 && in.path == w.Path {
		binary = in.binary
	} else {
		binary, err = readStored(ctx, env.Blobs, w.Checksums.SHA256)
		if err != nil {
			return w, err
		}
	}
	if err := coordinates(w.Properties, binary); err != nil {
		return w, format.Rejectf(http.StatusBadRequest, "%v", err)
	}
	return w, nil
}

// Deployed sets a stored binary's deb.name and deb.version properties,
// brings its version in the package index up to date and, for a local or
// federated repository's own write, queues the metadata rebuild. A
// version's files are every binary in the repository whose control data
// gives that name and version, so Deployed reads them all rather than
// trusting their paths. A source package file (a .dsc, .diff.gz or
// .tar.* archive) only queues the rebuild, which lists the source once
// all its parts are stored. Folders and other files change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder {
		return nil
	}
	if isSourceRelated(it.FullPath()) {
		return queue(ctx, env, c, it.Cached)
	}
	if !isBinary(it.FullPath()) {
		return nil
	}
	b, err := packageFor(ctx, env, it)
	if err != nil {
		return err
	}
	if err := setProperties(ctx, env.Store, it, b); err != nil {
		return err
	}
	if err := syncVersion(ctx, env, c, b.Name, b.Version); err != nil {
		return err
	}
	return queue(ctx, env, c, it.Cached)
}

// Deleting refuses (409) to delete anything under snapshots/ except as
// part of a whole snapshot folder (WithSnapshotDelete), and queues the
// metadata rebuild when a local or federated repository's own binary or
// source package file is about to be deleted, so the rebuild is asked for
// exactly when the delete commits.
func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if snapshotPath(it.FullPath()) && ctx.Value(snapshotDeleteKey{}) != true {
		return format.Rejectf(http.StatusConflict, "Delete the complete Debian snapshot folder")
	}
	if it.Folder || !isBinary(it.FullPath()) && !isSourceRelated(it.FullPath()) {
		return nil
	}
	return queue(ctx, env, c, it.Cached)
}

// Deleted re-syncs each removed binary's version in the package index,
// once per name and version, from that version's remaining binaries. A
// version with none left is not its concern: the item service has already
// pruned it.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	seen := map[string]bool{}
	for _, it := range removed {
		if !isBinary(it.FullPath()) {
			continue
		}
		b, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		key := b.Name + "\x00" + b.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := syncVersion(ctx, env, c, b.Name, b.Version); err != nil {
			return err
		}
	}
	return nil
}

// WorkKinds returns KindIndex and KindReindexNoProps, the kinds Work runs.
func (Handler) WorkKinds() []string { return []string{KindIndex, KindReindexNoProps} }

// Owed makes a read of a local or federated repository's apt metadata wait
// for its pending rebuild, so a client never reads metadata that lags a
// completed write. A remote's cache owes nothing.
func (Handler) Owed(c repo.Config, cached bool, path string) []format.WorkRef {
	if !cached && derives(c) && isGenerated(path) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

// Classify classes apt metadata under dists/ as derived, so replication
// and federation skip it and each side generates its own, and every other
// path as unique, a snapshot's copied metadata included, since it cannot
// be generated again.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if isGenerated(path) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// Reindex rebuilds repository c's package index from its binaries (a
// remote's from its cache): a package per name and a version per version,
// whose files are its binaries and whose creation time is the oldest
// file's. It reads every binary, failing on the first that is missing or
// invalid, and restores the deb.name and deb.version properties. A local
// or federated repository's metadata rebuild is then queued.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c, true)
}

func reindex(ctx context.Context, env format.Env, c repo.Config, writeProps bool) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]types.Item{}
	for _, it := range items {
		if !isBinary(it.FullPath()) {
			continue
		}
		b, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if writeProps {
			if err := setProperties(ctx, env.Store, it, b); err != nil {
				return err
			}
		}
		if by[b.Name] == nil {
			by[b.Name] = map[string][]types.Item{}
		}
		by[b.Name][b.Version] = append(by[b.Name][b.Version], it)
	}
	wanted := make([]pkgindex.Indexed, 0, len(by))
	for name, versions := range by {
		p := pkgindex.Indexed{Name: name}
		for version, items := range versions {
			v := indexedVersion(version, items)
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
	slices.SortFunc(
		wanted,
		func(a, b pkgindex.Indexed) int { return strings.Compare(a.Name, b.Name) },
	)
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("debian: reconcile packages: %w", err)
	}
	return queue(ctx, env, c, pkgindex.Stored(c).Cached)
}

func isBinary(path string) bool {
	return strings.HasSuffix(path, ".deb") || strings.HasSuffix(path, ".ddeb")
}

func isSourceDescriptor(path string) bool { return strings.HasSuffix(path, ".dsc") }

func isSourceRelated(path string) bool {
	return isSourceDescriptor(path) || strings.HasSuffix(path, ".diff.gz") ||
		strings.Contains(path, ".tar.")
}

func isGenerated(path string) bool {
	return strings.HasPrefix(path, "dists/") &&
		(strings.Contains(path, "/binary-") || strings.Contains(path, "/source/") ||
			strings.HasSuffix(path, "/Release") ||
			strings.HasSuffix(path, "/InRelease") || strings.HasSuffix(path, "/Release.gpg"))
}

func derives(c repo.Config) bool { return c.Class == repo.Local || c.Class == repo.Federated }

func queue(ctx context.Context, env format.Env, c repo.Config, cached bool) error {
	if cached || !derives(c) {
		return nil
	}
	return env.Enqueue(ctx, KindIndex, c.Key, nil) //nolint:wrapcheck
}

type readerAt struct{ io.ReadSeeker }

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, err //nolint:wrapcheck // adapting the blob read interface
	}
	return io.ReadFull(r, p) //nolint:wrapcheck // adapting the blob read interface
}

func readStored(ctx context.Context, blobs format.Blobs, sha string) (Binary, error) {
	if sha == "" {
		return Binary{}, format.Rejectf(http.StatusBadRequest, "Debian package content is missing")
	}
	r, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Binary{}, fmt.Errorf("debian: open package: %w", err)
	}
	defer func() { _ = r.Close() }()
	b, err := ReadBinary(ctx, readerAt{r}, info.Size)
	if err != nil {
		return Binary{}, format.Rejectf(http.StatusBadRequest, "Invalid Debian package: %v", err)
	}
	return b, nil
}

func readStoredSource(ctx context.Context, blobs format.Blobs, sha string) (Source, error) {
	if sha == "" {
		return Source{}, format.Rejectf(http.StatusBadRequest, "Debian source content is missing")
	}
	r, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Source{}, fmt.Errorf("debian: open source package: %w", err)
	}
	defer func() { _ = r.Close() }()
	source, err := ReadSource(ctx, r, info.Size)
	if err != nil {
		return Source{}, format.Rejectf(http.StatusBadRequest, "Invalid Debian source: %v", err)
	}
	return source, nil
}

func sourceFor(ctx context.Context, env format.Env, it types.Item) (Source, error) {
	if in, ok := env.Inspection.(sourceInspection); ok &&
		in.sha == it.Actual.SHA256 && in.path == it.FullPath() {
		return in.source, nil
	}
	return readStoredSource(ctx, env.Blobs, it.Actual.SHA256)
}

func sourceCoordinates(props types.Properties, source Source, cached bool) error {
	dists, components := props["dsc.distribution"], props["dsc.component"]
	if len(dists)+len(components) == 0 && cached {
		return nil
	}
	if len(dists) == 0 || len(components) == 0 || source.Name == "" {
		return fmt.Errorf("%w: missing source distribution or component", errInvalidCoordinates)
	}
	for _, values := range [][]string{dists, components} {
		for _, value := range values {
			if !architecture.MatchString(value) {
				return fmt.Errorf("%w: invalid source coordinate %q", errInvalidCoordinates, value)
			}
		}
	}
	return nil
}

func packageFor(ctx context.Context, env format.Env, it types.Item) (Binary, error) {
	if in, ok := env.Inspection.(inspection); ok &&
		in.sha == it.Actual.SHA256 && in.path == it.FullPath() {
		return in.binary, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256)
}

func coordinates(props types.Properties, b Binary) error {
	distributions, components, arches := props[PropDistribution], props[PropComponent], props[PropArchitecture]
	if len(distributions)+len(components)+len(arches) == 0 {
		return nil // stored without Debian coordinates; not indexed by apt
	}
	if len(distributions) == 0 || len(components) == 0 || len(arches) == 0 {
		return fmt.Errorf(
			"%w: incomplete distribution, component or architecture",
			errInvalidCoordinates,
		)
	}
	for _, values := range [][]string{distributions, components, arches} {
		for _, value := range values {
			if !architecture.MatchString(value) {
				return fmt.Errorf("%w: invalid value %q", errInvalidCoordinates, value)
			}
		}
	}
	for _, arch := range arches {
		if b.Architecture != "all" && b.Architecture != arch {
			return fmt.Errorf(
				"%w: package architecture %q differs from layout %q",
				errInvalidCoordinates,
				b.Architecture,
				arch,
			)
		}
	}
	return nil
}

func setProperties(ctx context.Context, st format.Store, it types.Item, b Binary) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("debian: item properties: %w", err)
	}
	want := types.Properties{PropName: {b.Name}, PropVersion: {b.Version}}
	set := types.Properties{}
	for key, value := range want {
		if !slices.Equal(props[key], value) {
			set[key] = value
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
		if !isBinary(it.FullPath()) {
			continue
		}
		b, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if b.Name == name && b.Version == version {
			selected = append(selected, it)
		}
	}
	if len(selected) == 0 {
		return nil // the item service prunes the last version on delete
	}
	v := indexedVersion(version, selected)
	p, err := env.Store.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("debian: package %s: %w", name, err)
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

func files(ctx context.Context, st format.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("debian: repository files: %w", err)
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
