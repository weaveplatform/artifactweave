package access

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authz"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var _ accessapi.Grants = grants{}

// grants scans the stored permissions about content. The two scans are the
// ones the storage permissions view (migration/artifacts/adapters/http/storage.go) and the
// Hugging Face repository migration
// (migration/artifacts/ecosystem/huggingface/http) ran themselves, with their
// order and error texts.
type grants struct{ st Store }

func (b grants) ItemGrants(
	ctx context.Context,
	targets []string,
	path string,
	folder bool,
) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
	users, groups := map[string]accessapi.Actions{}, map[string]accessapi.Actions{}
	seen := map[string]bool{}
	for _, t := range targets {
		perms, err := b.st.Permissions().WithTarget(ctx, t)
		if err != nil {
			return nil, nil, fmt.Errorf("permissions with target %s: %w", t, err)
		}
		for _, perm := range perms {
			res, ok := perm.Resources[accessapi.ResourceArtifact]
			if seen[perm.Name] || !ok {
				continue
			}
			tg, named := res.Targets[t]
			if !named || !authz.TargetAllows(tg, path, folder) {
				continue
			}
			// The first target whose patterns admit the path decides, so a
			// permission naming several of them counts once.
			seen[perm.Name] = true
			for u, a := range res.Actions.Users {
				users[u] |= a
			}
			for g, a := range res.Actions.Groups {
				groups[g] |= a
			}
		}
	}
	return users, groups, nil
}

func (b grants) AccessChange(
	ctx context.Context,
	repoKey, class string,
	pairs []accessapi.PathPair,
	limit int,
) (string, error) {
	page := kernel.Page{Limit: limit}
	seen := 0
	for {
		perms, next, err := b.st.Permissions().List(ctx, page)
		if err != nil {
			return "", err //nolint:wrapcheck // the store's errors are the contract's; callers wrap them
		}
		for _, perm := range perms {
			// Counted before it is compared: the permission past the limit
			// refuses the scan whether or not it would change access.
			seen++
			if seen > limit {
				return "", accessapi.ErrScanLimit
			}
			// Each permission's whole target union for the repository is
			// compared, so every grant it makes, to users, groups and the
			// anonymous user, exclusions included, applies after the move
			// exactly as before.
			resource := perm.Resources[accessapi.ResourceArtifact]
			var covering []model.Target
			for key, rule := range resource.Targets {
				if model.MatchesRepository(key, repoKey, class) {
					covering = append(covering, rule)
				}
			}
			for _, pair := range pairs {
				if admits(covering, pair.Source, pair.Folder) !=
					admits(covering, pair.Destination, pair.Folder) {
					return perm.Name, nil
				}
			}
		}
		if next == "" {
			return "", nil
		}
		page.After = next
	}
}

// admits reports whether any of targets admits path.
func admits(targets []model.Target, path string, folder bool) bool {
	for _, t := range targets {
		if authz.TargetAllows(t, path, folder) {
			return true
		}
	}
	return false
}
