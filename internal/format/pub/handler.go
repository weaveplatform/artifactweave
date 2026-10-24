package pub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	PropName    = "pub.name"
	PropVersion = "pub.version"
	PropSHA256  = "pub.sha256"
)

func Serves(t repo.PackageType) bool { return t == repo.Pub }

type Handler struct{ generic.Handler }

type inspection struct {
	sha  string
	meta Metadata
}

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

func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("pub: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if w.Cached && cachedMetadataPath(w.Path) {
		return w, nil
	}
	name, version, ok := ParsePath(w.Path)
	if !ok {
		return w, format.Reject(http.StatusBadRequest, "invalid Dart pub archive path")
	}
	in, ok := env.Inspection.(inspection)
	if !ok || in.sha != w.Checksums.SHA256 {
		in.meta, err = readStored(ctx, env.Blobs, w.Checksums.SHA256)
		if err != nil {
			return w, err
		}
	}
	if in.meta.Name != name || in.meta.Version != version {
		return w, format.Reject(
			http.StatusConflict,
			"Dart pub archive identity does not match path",
		)
	}
	return w, nil
}

// A published version is immutable through both pub publish and the generic
// item route. Deletes and restores still use their normal lifecycle paths.
func (Handler) GuardWrite(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) error {
	if w.Op == format.OpChecksum || w.Cached {
		return nil
	}
	_, err := env.Store.Items().Get(ctx, c.Key, false, w.Path)
	switch {
	case err == nil:
		return format.Reject(http.StatusConflict, "Dart pub version already published")
	case errors.Is(err, store.ErrNotFound):
		return nil
	default:
		return fmt.Errorf("pub: check existing version: %w", err)
	}
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
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
		return fmt.Errorf("pub: stored archive identity: %w", ErrArchive)
	}
	meta.Path, meta.SHA256 = it.FullPath(), it.Actual.SHA256
	if err := setProperties(ctx, env.Store, it, meta); err != nil {
		return err
	}
	p, err := env.Store.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: it.Created,
	})
	if err != nil {
		return fmt.Errorf("pub: package %s: %w", name, err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("pub: metadata: %w", err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, pkgindex.IndexedVersion{
		Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex names the version
}

func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error  { return nil }
func (Handler) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

func cachedMetadataPath(p string) bool {
	if !strings.HasPrefix(p, "api/packages/") {
		return false
	}
	name := strings.TrimPrefix(p, "api/packages/")
	return ValidName(name)
}

func readStored(ctx context.Context, blobs format.Blobs, sha string) (Metadata, error) {
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Metadata{}, fmt.Errorf("pub: open archive: %w", err)
	}
	defer rc.Close()
	meta, err := ReadArchive(rc, info.Size)
	if err != nil {
		return Metadata{}, fmt.Errorf("pub: read archive: %w", err)
	}
	return meta, nil
}

func metadataOf(ctx context.Context, env format.Env, it types.Item) (Metadata, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == it.Actual.SHA256 {
		return in.meta, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256)
}

func setProperties(ctx context.Context, st store.Store, it types.Item, meta Metadata) error {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("pub: get item properties: %w", err)
	}
	want := types.Properties{
		PropName: {meta.Name}, PropVersion: {meta.Version}, PropSHA256: {meta.SHA256},
	}
	set := types.Properties{}
	for key, value := range want {
		if len(current[key]) != 1 || current[key][0] != value[0] {
			set[key] = value
		}
	}
	if len(set) == 0 {
		return nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, nil, it.Updated); err != nil {
		return fmt.Errorf("pub: set item properties: %w", err)
	}
	return nil
}

// Reindex reconstructs all package versions from their admitted archives.
// The item tree, including the remote cache tree, remains authoritative.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	byName := map[string]*pkgindex.Indexed{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(
			ctx, c.Key, pkgindex.Stored(c).Cached, "", page,
		)
		if err != nil {
			return fmt.Errorf("pub: list archives: %w", err)
		}
		for _, it := range items {
			name, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			meta, err := metadataOf(ctx, env, it)
			if err != nil || meta.Name != name || meta.Version != version {
				return fmt.Errorf("pub: reindex archive %s: %w", it.FullPath(), ErrArchive)
			}
			meta.Path, meta.SHA256 = it.FullPath(), it.Actual.SHA256
			if !env.SkipProperties {
				if err := setProperties(ctx, env.Store, it, meta); err != nil {
					return err
				}
			}
			data, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("pub: marshal reindex metadata: %w", err)
			}
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
			return fmt.Errorf("pub: reindex authority: %w", err)
		}
	}
	return pkgindex.Reconcile(ctx, env.Store, c, want) //nolint:wrapcheck
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.WriteGuard = Handler{}
	_ format.Reindexer  = Handler{}
)
