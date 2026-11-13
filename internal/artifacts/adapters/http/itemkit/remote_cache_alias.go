package itemkit

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// aliasRemoteMetadata reports whether p, asked for on remote repository
// c's -cache alias (cached), is a document c's cache holds without an
// item: a Debian Release, by-hash file, installer index or component file,
// or an RPM repodata file, which c's source request asks for as metadata.
// The remote source keeps such a file as a remote cache entry, so the
// alias's item lookup never found it and answered 404 where the remote
// served it (#1382). JFrog's Remote Repositories page says what is
// "already stored in the cache" is read at
// <remote-repository-name>-cache/<artifact-path> "to avoid remote update
// checks", so the caller reads the entry through the remote source as a
// cache-only read (the retrieval contract's R8), without asking the
// upstream, as a Maven remote's alias reads its maven-metadata.xml
// (getRemoteItem). With no such document held, the item lookup still
// answers, as it does for any file the alias does not hold. A checksum
// file is left to it too, as the alias answers it for every other cached
// file.
func aliasRemoteMetadata(
	ctx context.Context, st Store, c repo.Config, cached bool, p string,
) (bool, error) {
	if !cached || c.Class != repo.Remote || p == "" {
		return false, nil
	}
	if _, _, ok := item.Sidecar(p); ok {
		return false, nil
	}
	var prepare func(source.Request) (source.Request, error)
	switch {
	case debian.Serves(c.PackageType):
		prepare = DebianSourceRequest
	case rpm.Serves(c.PackageType):
		prepare = RPMSourceRequest
	default:
		return false, nil
	}
	q, err := prepare(source.Request{Repo: c, Resource: source.Resource{Path: p}})
	if err != nil || q.Resource.Kind != source.ResourceKindMetadata {
		return false, nil //nolint:nilerr // a path the source refuses is the item lookup's
	}
	page := kernel.Page{}
	for {
		entries, next, err := st.RemoteCache().Exact(ctx, c.Key, p, page)
		if err != nil {
			return false, fmt.Errorf("cached document %s: %w", p, err)
		}
		for _, e := range entries {
			if e.ItemID == 0 && e.Blob.Checksums.SHA256 != "" {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		page.After = next
	}
}
