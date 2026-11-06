package accessapitest

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
)

var _ accessapi.Grants = (*Grants)(nil)

// Grants is a Grants whose answers its function fields script, each with the
// signature of the method it answers, so a test states the scan's outcome,
// such as accessapi.ErrScanLimit or the name of a permission that a move
// would change, without storing the permissions that lead to it. Where a
// field is nil it answers as a store without permissions: no one holds
// anything, and no permission changes access. It holds no state, so it may
// be used from several goroutines as long as its functions may.
type Grants struct {
	// ItemGrantsFunc answers ItemGrants.
	ItemGrantsFunc func(
		ctx context.Context,
		targets []string,
		path string,
		folder bool,
	) (users, groups map[string]accessapi.Actions, err error)
	// AccessChangeFunc answers AccessChange.
	AccessChangeFunc func(
		ctx context.Context,
		repoKey, class string,
		pairs []accessapi.PathPair,
		limit int,
	) (permission string, err error)
}

// ItemGrants answers with ItemGrantsFunc, or with two empty maps when it is
// nil.
func (g *Grants) ItemGrants(
	ctx context.Context,
	targets []string,
	path string,
	folder bool,
) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
	if g.ItemGrantsFunc == nil {
		return map[string]accessapi.Actions{}, map[string]accessapi.Actions{}, nil
	}
	return g.ItemGrantsFunc(ctx, targets, path, folder)
}

// AccessChange answers with AccessChangeFunc, or "" when it is nil.
func (g *Grants) AccessChange(
	ctx context.Context,
	repoKey, class string,
	pairs []accessapi.PathPair,
	limit int,
) (string, error) {
	if g.AccessChangeFunc == nil {
		return "", nil
	}
	return g.AccessChangeFunc(ctx, repoKey, class, pairs, limit)
}
