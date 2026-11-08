package server

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// access asks the request's permission decider (decision 0006) the
// questions handlers have about repositories and their items. Paths are
// repository-relative, as permission target patterns are.
type access struct{ d accessapi.Decider }

// target is the permission target a repository's configuration names.
func target(c repo.Config) accessapi.Repo {
	return accessapi.Repo{Key: c.Key, Class: string(c.Class)}
}

// file reports whether the caller holds every action in want on a file.
func (a access) file(c repo.Config, path string, want types.Actions) bool {
	return a.d.Allows(types.ResourceArtifact, target(c), path, want)
}

// folder reports it for a folder, which is also allowed when a permitted
// path lies below it, so that listings show the way down.
func (a access) folder(c repo.Config, path string, want types.Actions) bool {
	return a.d.AllowsFolder(types.ResourceArtifact, target(c), path, want)
}

// item is file or folder by kind.
func (a access) item(c repo.Config, path string, isFolder bool, want types.Actions) bool {
	if isFolder {
		return a.folder(c, path, want)
	}
	return a.file(c, path, want)
}

// visible reports whether the caller sees a repository in a listing and may
// read its configuration.
func (a access) visible(c repo.Config) bool { return a.d.IsRepoVisible(target(c)) }

// accessFor returns the decider of p's permissions: d, the decider that
// Authorize returned with p, or, when d is nil, az's evaluation of p. The
// middleware authorises every request, so a handler passes the request's
// decider for its principal (accessapi.DeciderFrom), and a principal the
// middleware did not see, which has none, is evaluated by Authorizer.For.
// The error is For's, unwrapped: it already names the read that failed, and
// callers wrap it with their own operation.
func accessFor(
	ctx context.Context,
	az accessapi.Authorizer,
	p kernel.Principal,
	d accessapi.Decider,
) (access, error) {
	if d != nil {
		return access{d}, nil
	}
	d, err := az.For(ctx, p)
	if err != nil {
		return access{}, err //nolint:wrapcheck // For names the read that failed
	}
	return access{d}, nil
}
