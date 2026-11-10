package cocoapods

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

// Serves reports whether t is the CocoaPods package type.
func Serves(t repo.PackageType) bool { return t == repo.CocoaPods }

// Handler is the format of CocoaPods repositories, which the item service
// calls on every write and delete in them, whichever API carried it. It
// admits a hosted pod archive only once it has read it whole, and keeps
// the package index: a pod is a package, and each archive's version is a
// version with the Archive as its metadata. The embedded generic format
// applies every package type's deploy policy.
type Handler struct{ generic.Handler }

type inspection struct {
	sha     string
	archive Archive
	err     error
}

// Inspect reads a deployed hosted archive before the write's transaction,
// so Admit decides without parsing under the write lock. It answers nil
// for a write that is not to a hosted archive path or has no SHA-256, and
// an error only when the stored content cannot be opened; an archive that
// fails ReadArchive is handed to Admit to refuse.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	name, version, ok := ParsePath(w.Path)
	if !ok || w.Checksums.SHA256 == "" {
		return nil, nil //nolint:nilnil // no CocoaPods archive to inspect
	}
	rc, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("cocoapods: open archive: %w", err)
	}
	defer rc.Close()
	a, err := ReadArchive(rc, info.Size, name, version)
	return &inspection{sha: w.Checksums.SHA256, archive: a, err: err}, nil
}

// Admit applies the generic deploy policy, then refuses a write under the
// generated CDN metadata (.pod/cdn/, 409), a .tar.gz outside the hosted
// path grammar (400) and an archive that does not read as the pod and
// version of its path (400). Checksum uploads and remote cache writes pass
// after the generic checks: an HTTP source archive has its own validator
// and is not a hosted archive.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("cocoapods: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	// Remote HTTP source archives have their own CDN-bound validator. The
	// hosted path grammar describes only user-published pod archives.
	if w.Cached {
		return w, nil
	}
	if strings.HasPrefix(w.Path, ".pod/cdn/") {
		return w, format.Rejectf(http.StatusConflict, "CocoaPods CDN metadata is generated")
	}
	if !strings.HasSuffix(w.Path, ".tar.gz") {
		return w, nil
	}
	name, version, ok := ParsePath(w.Path)
	if !ok {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"CocoaPods archive path must be name/version/name-version.tar.gz",
		)
	}
	if in, ok := env.Inspection.(*inspection); ok && in.sha == w.Checksums.SHA256 {
		if in.err == nil && in.archive.Name == name && in.archive.Version == version {
			return w, nil
		}
		return w, format.Rejectf(http.StatusBadRequest, "Invalid CocoaPods archive")
	}
	if _, err := archiveOf(ctx, env, w.Checksums.SHA256, name, version); err != nil {
		return w, format.Rejectf(http.StatusBadRequest, "Invalid CocoaPods archive: %v", err)
	}
	return w, nil
}

// Deployed indexes a stored hosted archive's version under its pod, with
// the Archive and the archive's SHA-256 as the version's metadata. Folders,
// cached items and other paths are not indexed.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	name, version, ok := ParsePath(it.FullPath())
	if !ok || it.Folder || it.Cached {
		return nil
	}
	a, err := archiveOf(ctx, env, it.Actual.SHA256, name, version)
	if err != nil {
		return err
	}
	a.SHA256 = it.Actual.SHA256
	p, err := env.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: it.Created,
	})
	if err != nil {
		return fmt.Errorf("cocoapods: package index: %w", err)
	}
	meta, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("cocoapods: metadata: %w", err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, pkgindex.IndexedVersion{
		Version: version, Created: it.Created, Metadata: meta, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex reports the version
}

// Deleting does nothing: the item service drops a removed archive's version
// link with it.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted does nothing: a version is one archive, so the item service's
// pruning of versions left without files is all a delete needs.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []model.Item) error { return nil }

func archiveOf(ctx context.Context, env format.Env, sha, name, version string) (Archive, error) {
	rc, info, err := env.Blobs.Open(ctx, sha)
	if err != nil {
		return Archive{}, fmt.Errorf("cocoapods: open stored archive: %w", err)
	}
	defer rc.Close()
	a, err := ReadArchive(rc, info.Size, name, version)
	if err != nil {
		return Archive{}, fmt.Errorf("cocoapods: parse stored archive: %w", err)
	}
	return a, nil
}

// Reindex rebuilds the package index of repository c from its stored
// hosted archives, reading each one again, and reconciles the index to
// them. An archive that no longer reads as its path's pod and version
// fails the reindex rather than being left out.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var want []pkgindex.Indexed
	byName := map[string]int{}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "", page)
		if err != nil {
			return fmt.Errorf("cocoapods: list archives: %w", err)
		}
		for _, it := range items {
			name, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			a, err := archiveOf(ctx, env, it.Actual.SHA256, name, version)
			if err != nil {
				return err
			}
			a.SHA256 = it.Actual.SHA256
			meta, err := json.Marshal(a)
			if err != nil {
				return fmt.Errorf("cocoapods: metadata: %w", err)
			}
			i, found := byName[name]
			if !found {
				i = len(want)
				byName[name] = i
				want = append(want, pkgindex.Indexed{Name: name, Created: it.Created})
			}
			want[i].Versions = append(want[i].Versions, pkgindex.IndexedVersion{
				Version: version, Created: it.Created, Metadata: meta, Files: []int64{it.ID},
			})
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if err := pkgindex.Reconcile(
		ctx,
		env.Store,
		c,
		want,
	); err != nil {
		return fmt.Errorf("cocoapods: reconcile index: %w", err)
	}
	return nil
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
