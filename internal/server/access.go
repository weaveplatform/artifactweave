package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// access asks the request's permission evaluator (decision 0006) the
// questions handlers have about repositories and their items. Paths are
// repository-relative, as permission target patterns are.
type access struct{ d *authz.Decider }

// target is the permission target a repository's configuration names.
func target(c repo.Config) authz.Repo { return authz.Repo{Key: c.Key, Class: string(c.Class)} }

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

// accessFor returns the evaluator of p's permissions: d, the decider
// Authorize returned with p, or, when d is nil, a fresh evaluation of p.
// The middleware authorises every request, so a handler passes the
// request's decider for its principal (auth.DeciderFrom), and a principal
// the middleware did not see, which has none, is evaluated here.
func accessFor(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	d *authz.Decider,
) (access, error) {
	if d != nil {
		return access{d}, nil
	}
	var on bool
	err := st.Settings().Get(ctx, types.SettingAnonymousAccess, &on)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return access{}, fmt.Errorf("anonymous access setting: %w", err)
	}
	d, err = authz.New(ctx, authz.Sources{
		Permissions: st.Permissions(), Groups: st.Groups(), AnonymousEnabled: on,
	}, auth.SubjectOf(p))
	if err != nil {
		return access{}, fmt.Errorf("evaluate permissions: %w", err)
	}
	return access{d}, nil
}
