package chef

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// The item properties Deployed and Reindex keep on every stored cookbook
// archive, from its verified metadata.json. The items keep these names, so
// they never change.
const (
	// PropName is the property that holds an archive's cookbook name.
	PropName = "chef.name"
	// PropVersion is the property that holds an archive's cookbook version.
	PropVersion = "chef.version"
	// PropSHA256 is the property that holds an archive's SHA-256.
	PropSHA256 = "chef.sha256"
)

// Serves reports whether t is the Chef package type.
func Serves(t repo.PackageType) bool { return t == repo.Chef }

// Handler is the Chef format's hooks: it admits a cookbook archive only
// when its metadata.json matches its path, indexes each stored archive as a
// version of its cookbook, and keeps a remote's cached Supermarket metadata
// from leaving its repository. It embeds the generic hooks, so every path
// is classed as generic content is.
type Handler struct{ generic.Handler }

type inspection struct {
	sha  string
	meta Metadata
}

// Inspect reads and validates the stored archive of a write to a .tar.gz
// path before the write's transaction, and returns its metadata for Admit
// and Deployed, so an archive is read once per write. A checksum deploy and
// any other file inspect nothing. An invalid archive is refused with 400;
// an archive that cannot be opened fails the write.
func (Handler) Inspect(
	ctx context.Context, env format.Env, _ repo.Config, w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".tar.gz") {
		return nil, nil //nolint:nilnil // this write has no archive to inspect
	}
	meta, err := readStored(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, meta: meta}, nil
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy and a remote's cached Supermarket metadata
// (api/v1/...). Any other write must go to a local or federated repository
// or be a remote's cached content (400), at a cookbook archive path (400),
// and the archive's metadata.json must name the cookbook and version its
// path does (409). It reuses Inspect's reading when it covers the same
// content, and reads the stored archive itself otherwise.
func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("chef: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if w.Cached && cachedMetadataPath(w.Path) {
		return w, nil
	}
	if c.Class != repo.Local && c.Class != repo.Federated && (c.Class != repo.Remote || !w.Cached) {
		return w, format.Rejectf(http.StatusBadRequest, "unsupported Chef repository class")
	}
	name, version, ok := ParsePath(w.Path)
	if !ok {
		return w, format.Rejectf(http.StatusBadRequest, "invalid Chef cookbook archive path")
	}
	in, ok := env.Inspection.(inspection)
	if !ok || in.sha != w.Checksums.SHA256 {
		in.meta, err = readStored(ctx, env.Blobs, w.Checksums.SHA256)
		if err != nil {
			return w, err
		}
	}
	if in.meta.Name != name || in.meta.Version != version {
		return w, format.Rejectf(
			http.StatusConflict,
			"Chef cookbook archive identity does not match path",
		)
	}
	return w, nil
}

// Deployed indexes a stored cookbook archive: it sets the archive's chef.*
// properties, creates the cookbook's package when it is new, and links the
// version to the archive, with its Metadata as the version's metadata.
// Folders and files at other paths change nothing. An archive whose
// metadata.json no longer matches its path fails with ErrArchive.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder {
		return nil
	}
	name, version, ok := ParsePath(it.FullPath())
	if !ok {
		return nil
	}
	meta, err := metadataOf(ctx, env, it)
	if err != nil {
		return err
	}
	if meta.Name != name || meta.Version != version {
		return fmt.Errorf("chef: stored archive identity: %w", ErrArchive)
	}
	meta.Path, meta.SHA256 = it.FullPath(), it.Actual.SHA256
	if err := setProperties(ctx, env.Store, it, meta); err != nil {
		return err
	}
	p, err := env.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: it.Created,
	})
	if err != nil {
		return fmt.Errorf("chef: package %s: %w", name, err)
	}
	data, _ := json.Marshal(meta) // Metadata contains only JSON-native values.
	_, err = pkgindex.Sync(ctx, env.Store, p, pkgindex.IndexedVersion{
		Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex names the version
}

// Deleting does nothing: the item service drops a removed archive's
// version link with it.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted does nothing: a version is one archive, so the item service's
// pruning of versions left without files is all a delete needs.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []model.Item) error { return nil }

func cachedMetadataPath(p string) bool {
	return strings.HasPrefix(p, "api/v1/")
}

func readStored(ctx context.Context, blobs format.Blobs, sha string) (Metadata, error) {
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Metadata{}, fmt.Errorf("chef: open archive: %w", err)
	}
	defer rc.Close()
	meta, err := ReadArchive(rc, info.Size)
	if err != nil {
		return Metadata{}, format.Rejectf(http.StatusBadRequest, "invalid Chef cookbook archive")
	}
	return meta, nil
}

func metadataOf(ctx context.Context, env format.Env, it model.Item) (Metadata, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == it.Actual.SHA256 {
		return in.meta, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256)
}

func setProperties(ctx context.Context, st format.Store, it model.Item, meta Metadata) error {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("chef: get item properties: %w", err)
	}
	want := model.Properties{
		PropName: {meta.Name}, PropVersion: {meta.Version}, PropSHA256: {meta.SHA256},
	}
	set := model.Properties{}
	for key, value := range want {
		if len(current[key]) != 1 || current[key][0] != value[0] {
			set[key] = value
		}
	}
	if len(set) == 0 {
		return nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, nil, it.Updated); err != nil {
		return fmt.Errorf("chef: set item properties: %w", err)
	}
	return nil
}

// Reindex reconstructs all package versions from their admitted archives.
// The item tree, including the remote cache tree, remains authoritative.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	byName := map[string]*pkgindex.Indexed{}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(
			ctx, c.Key, pkgindex.Stored(c).Cached, "", page,
		)
		if err != nil {
			return fmt.Errorf("chef: list archives: %w", err)
		}
		for _, it := range items {
			name, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			meta, err := metadataOf(ctx, env, it)
			if err != nil || meta.Name != name || meta.Version != version {
				return fmt.Errorf("chef: reindex archive %s: %w", it.FullPath(), ErrArchive)
			}
			meta.Path, meta.SHA256 = it.FullPath(), it.Actual.SHA256
			if !env.SkipProperties {
				if err := setProperties(ctx, env.Store, it, meta); err != nil {
					return err
				}
			}
			data, _ := json.Marshal(meta) // Metadata contains only JSON-native values.
			p := byName[name]
			if p == nil {
				p = &pkgindex.Indexed{Name: name, Created: it.Created}
				byName[name] = p
			}
			p.Versions = append(p.Versions, pkgindex.IndexedVersion{
				Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
			})
		}
		if next == "" {
			break
		}
		page.After = next
	}
	want := make([]pkgindex.Indexed, 0, len(byName))
	for _, p := range byName {
		want = append(want, *p)
	}
	if env.CheckReindex != nil {
		if err := env.CheckReindex(ctx); err != nil {
			return fmt.Errorf("chef: reindex authority: %w", err)
		}
	}
	return pkgindex.Reconcile(ctx, env.Store, c, want) //nolint:wrapcheck
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)

// GuardTransferSource keeps unfiltered remote catalog metadata behind the
// native permission-aware API when a caller copies, moves or reuses content.
func (Handler) GuardTransferSource(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	it model.Item,
) error {
	if IsMetadata(it.FullPath()) {
		return format.Rejectf(
			http.StatusForbidden,
			"Chef metadata must be read through the native cookbook endpoint",
		)
	}
	return nil
}

// IsMetadata reports whether path lies in the tree where a remote caches
// Supermarket metadata (api/v1/). Those documents list versions outside any
// one caller's permissions, so only the native protocol, which filters
// them for the caller, may disclose them.
func IsMetadata(path string) bool { return strings.HasPrefix(path, "api/v1/") }
