package itemkit

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Access asks the request's permission decider (decision 0006) the
// questions handlers have about repositories and their items. Paths are
// repository-relative, as permission target patterns are.
type Access struct{ Decider accessapi.Decider }

// Target is the permission target a repository's configuration names.
func Target(c repo.Config) accessapi.Repo {
	return accessapi.Repo{Key: c.Key, Class: string(c.Class)}
}

// File reports whether the caller holds every action in want on a file.
func (a Access) File(c repo.Config, path string, want types.Actions) bool {
	return a.Decider.Allows(types.ResourceArtifact, Target(c), path, want)
}

// Folder reports it for a folder, which is also allowed when a permitted
// path lies below it, so that listings show the way down.
func (a Access) Folder(c repo.Config, path string, want types.Actions) bool {
	return a.Decider.AllowsFolder(types.ResourceArtifact, Target(c), path, want)
}

// Item is file or folder by kind.
func (a Access) Item(c repo.Config, path string, isFolder bool, want types.Actions) bool {
	if isFolder {
		return a.Folder(c, path, want)
	}
	return a.File(c, path, want)
}

// Visible reports whether the caller sees a repository in a listing and may
// read its configuration.
func (a Access) Visible(c repo.Config) bool { return a.Decider.IsRepoVisible(Target(c)) }

// AccessFor returns the decider of p's permissions: d, the decider that
// Authorize returned with p, or, when d is nil, az's evaluation of p. The
// middleware authorises every request, so a handler passes the request's
// decider for its principal (accessapi.DeciderFrom), and a principal the
// middleware did not see, which has none, is evaluated by Authorizer.For.
// The error is For's, unwrapped: it already names the read that failed, and
// callers wrap it with their own operation.
func AccessFor(
	ctx context.Context,
	az accessapi.Authorizer,
	p kernel.Principal,
	d accessapi.Decider,
) (Access, error) {
	if d != nil {
		return Access{d}, nil
	}
	d, err := az.For(ctx, p)
	if err != nil {
		return Access{}, err //nolint:wrapcheck // For names the read that failed
	}
	return Access{d}, nil
}
