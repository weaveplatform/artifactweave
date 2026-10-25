package cocoapods

import (
	"context"
	"encoding/json"
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

func Serves(t repo.PackageType) bool { return t == repo.CocoaPods }

type Handler struct{ generic.Handler }

type inspection struct {
	sha     string
	archive Archive
	err     error
}

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
		return w, format.Reject(http.StatusConflict, "CocoaPods CDN metadata is generated")
	}
	if !strings.HasSuffix(w.Path, ".tar.gz") {
		return w, nil
	}
	name, version, ok := ParsePath(w.Path)
	if !ok {
		return w, format.Reject(
			http.StatusBadRequest,
			"CocoaPods archive path must be name/version/name-version.tar.gz",
		)
	}
	if in, ok := env.Inspection.(*inspection); ok && in.sha == w.Checksums.SHA256 {
		if in.err == nil && in.archive.Name == name && in.archive.Version == version {
			return w, nil
		}
		return w, format.Reject(http.StatusBadRequest, "Invalid CocoaPods archive")
	}
	if _, err := archiveOf(ctx, env, w.Checksums.SHA256, name, version); err != nil {
		return w, format.Reject(http.StatusBadRequest, "Invalid CocoaPods archive: %v", err)
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	name, version, ok := ParsePath(it.FullPath())
	if !ok || it.Folder || it.Cached {
		return nil
	}
	a, err := archiveOf(ctx, env, it.Actual.SHA256, name, version)
	if err != nil {
		return err
	}
	a.SHA256 = it.Actual.SHA256
	p, err := env.Store.Packages().Ensure(ctx, types.Package{
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

func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error  { return nil }
func (Handler) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

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

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var want []pkgindex.Indexed
	byName := map[string]int{}
	page := store.Page{Limit: store.MaxLimit}
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
