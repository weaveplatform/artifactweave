package itemkit

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
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
func (a Access) File(c repo.Config, path string, want accessapi.Actions) bool {
	return a.Decider.Allows(accessapi.ResourceArtifact, Target(c), path, want)
}

// Folder reports it for a folder, which is also allowed when a permitted
// path lies below it, so that listings show the way down.
func (a Access) Folder(c repo.Config, path string, want accessapi.Actions) bool {
	return a.Decider.AllowsFolder(accessapi.ResourceArtifact, Target(c), path, want)
}

// Item is file or folder by kind.
func (a Access) Item(c repo.Config, path string, isFolder bool, want accessapi.Actions) bool {
	if isFolder {
		return a.Folder(c, path, want)
	}
	return a.File(c, path, want)
}

// Admits reports whether the caller passes the read gate of a request for
// path in c, asked before anything is looked up: Read on the path in c at
// folder level, the exact right being asked once the item is found.
//
// A virtual repository is never asked itself. No permission target can
// name one (decision 0006), and 7.x decides a read through it by the
// member that serves it (R3; ArtifactoryVirtualDownloadStrategy.
// processStandard), so a path in it passes here and the walk asks each
// member (lookupMember). Its root passes when the root of any member,
// nested virtual repositories flattened, does
// (AuthorizationServiceBase.computeUserHasPermissionsOnRepositoryRoot
// 1047-1078, 7.161.26). Package handlers apply the same rule, an unreadable
// member counting there as not found (R3). An administrator passes without
// a member, as there, so the root of a virtual repository with none
// answers it too.
func (a Access) Admits(ctx context.Context, rs virtual.Repos, c repo.Config, path string) (bool, error) {
	if c.Class != repo.Virtual {
		return a.Folder(c, path, accessapi.ActionRead), nil
	}
	if path != "" || a.Decider.IsAdmin() {
		return true, nil
	}
	members, err := virtual.Members(ctx, rs, c, virtual.Options{Order: virtual.Stored})
	if err != nil {
		return false, fmt.Errorf("members of %s: %w", c.Key, err)
	}
	for _, m := range members {
		if a.Folder(m.Repo, "", accessapi.ActionRead) {
			return true, nil
		}
	}
	return false, nil
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
