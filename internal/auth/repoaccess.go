package auth

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RepoReader returns whether p may read a repository, at repository level:
// an admin reads every repository; anyone else needs READ on the artifact
// resource of a permission granted to their user or one of their groups
// whose targets name the repository, ANY, or ANY LOCAL or ANY REMOTE for its
// class. Include and exclude patterns are not evaluated: READ anywhere in a
// repository is enough to see it, and path-level checks arrive with full
// permission evaluation in P4. The permissions are read once, so a list
// checks every repository against one snapshot.
func RepoReader(
	ctx context.Context,
	perms store.PermissionStore,
	p Principal,
) (func(repo.Config) bool, error) {
	if p.Admin {
		return func(repo.Config) bool { return true }, nil
	}
	granted, err := perms.ForPrincipal(ctx, p.Name, p.Groups)
	if err != nil {
		return nil, fmt.Errorf("auth: permissions of %s: %w", p.Name, err)
	}
	var targets []string
	for _, perm := range granted {
		res, ok := perm.Resources[types.ResourceArtifact]
		if !ok || !grantsRead(res.Actions, p) {
			continue
		}
		for target := range res.Targets {
			targets = append(targets, target)
		}
	}
	return func(c repo.Config) bool {
		for _, target := range targets {
			if covers(target, c) {
				return true
			}
		}
		return false
	}, nil
}

func grantsRead(g types.Grants, p Principal) bool {
	if g.Users[p.Name].Has(types.ActionRead) {
		return true
	}
	for _, group := range p.Groups {
		if g.Groups[group].Has(types.ActionRead) {
			return true
		}
	}
	return false
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
