package debian

import (
	"context"
	"errors"
	"fmt"
	"io"
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

const KindIndex = "debian.index"

var errInvalidCoordinates = errors.New("debian: invalid package coordinates")

func Serves(t repo.PackageType) bool { return t == repo.Debian }

type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	binary    Binary
}

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if !isBinary(w.Path) || w.Op == format.OpChecksum {
		return inspection{}, nil
	}
	binary, err := readStored(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, binary: binary}, nil
}

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("debian: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && isGenerated(w.Path) {
		return w, format.Reject(http.StatusConflict, "Debian metadata is generated from packages")
	}
	if !isBinary(w.Path) {
		return w, nil
	}
	if strings.HasPrefix(w.Path, "/") || strings.Contains(w.Path, "\\") {
		return w, format.Reject(http.StatusBadRequest, "Invalid Debian package path")
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
		return w, format.Reject(http.StatusBadRequest, "%v", err)
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || !isBinary(it.FullPath()) {
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

func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || !isBinary(it.FullPath()) {
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

func (Handler) WorkKinds() []string { return []string{KindIndex} }

func (Handler) Owed(c repo.Config, cached bool, path string) []format.WorkRef {
	if !cached && derives(c) && isGenerated(path) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

func (Handler) Classify(_ repo.Config, path string) format.Class {
	if isGenerated(path) {
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
		if !isBinary(it.FullPath()) {
			continue
		}
		b, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		if err := setProperties(ctx, env.Store, it, b); err != nil {
			return err
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

func isGenerated(path string) bool {
	return strings.HasPrefix(path, "dists/") &&
		(strings.Contains(path, "/binary-") || strings.HasSuffix(path, "/Release") ||
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
		return Binary{}, format.Reject(http.StatusBadRequest, "Debian package content is missing")
	}
	r, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Binary{}, fmt.Errorf("debian: open package: %w", err)
	}
	defer func() { _ = r.Close() }()
	b, err := ReadBinary(ctx, readerAt{r}, info.Size)
	if err != nil {
		return Binary{}, format.Reject(http.StatusBadRequest, "Invalid Debian package: %v", err)
	}
	return b, nil
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

func setProperties(ctx context.Context, st store.Store, it types.Item, b Binary) error {
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

func files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
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
