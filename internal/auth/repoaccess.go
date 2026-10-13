package auth

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RepoReader returns whether p may read a repository (RepoChecker with
// READ). A list checks every repository against one permission snapshot.
func RepoReader(
	ctx context.Context,
	perms store.PermissionStore,
	p Principal,
) (func(repo.Config) bool, error) {
	can, err := RepoChecker(ctx, perms, p)
	if err != nil {
		return nil, err
	}
	return func(c repo.Config) bool { return can(c, types.ActionRead) }, nil
}

// RepoChecker returns whether p holds an action on a repository, at
// repository level: an admin holds every action; anyone else needs the
// action on the artifact resource of a permission granted to their user or
// one of their groups whose targets name the repository, ANY, or ANY LOCAL
// or ANY REMOTE for its class. Include and exclude patterns are not
// evaluated: the action anywhere in a repository is enough, and path-level
// checks arrive with full permission evaluation in P4. The permissions are
// read once, so every check uses one snapshot.
func RepoChecker(
	ctx context.Context,
	perms store.PermissionStore,
	p Principal,
) (func(repo.Config, types.Actions) bool, error) {
	if p.Admin {
		return func(repo.Config, types.Actions) bool { return true }, nil
	}
	granted, err := perms.ForPrincipal(ctx, p.Name, p.Groups)
	if err != nil {
		return nil, fmt.Errorf("auth: permissions of %s: %w", p.Name, err)
	}
	type grant struct {
		target  string
		actions types.Actions
	}
	var grants []grant
	for _, perm := range granted {
		res, ok := perm.Resources[types.ResourceArtifact]
		if !ok {
			continue
		}
		actions := held(res.Actions, p)
		if actions == 0 {
			continue
		}
		for target := range res.Targets {
			grants = append(grants, grant{target: target, actions: actions})
		}
	}
	return func(c repo.Config, want types.Actions) bool {
		for _, g := range grants {
			if g.actions.Has(want) && covers(g.target, c) {
				return true
			}
		}
		return false
	}, nil
}

// held is every action g grants p, directly or through a group.
func held(g types.Grants, p Principal) types.Actions {
	a := g.Users[p.Name]
	for _, group := range p.Groups {
		a |= g.Groups[group]
	}
	return a
}

// covers reports whether a permission target includes c. Federated
// repositories are local repositories with members (research 03), so ANY
// LOCAL covers them.
func covers(target string, c repo.Config) bool {
	switch target {
	case types.TargetAny:
		return true
	case types.TargetAnyLocal:
		return c.Class == repo.Local || c.Class == repo.Federated
	case types.TargetAnyRemote:
		return c.Class == repo.Remote
	default:
		return target == c.Key
	}
}
