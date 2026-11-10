package item

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// NonUniqueSnapshot is nonUniqueSnapshot, for the parity test in package
// item_test. That test imports the Maven slice, and once the slice imports
// core/remote, which imports this package, an in-package test could not.
var NonUniqueSnapshot = nonUniqueSnapshot

// At, NewQueue, RunCleanup and Sums are the in-package test fixtures, and
// CleanupCachedItem one step of the cache cleanup, for the cache restart
// tests in package item_test. Those tests open the composite SQL store
// through the composition root's store test support, which reaches this
// package through the repository service, so an in-package test could not
// import it.
var (
	At         = at
	NewQueue   = newQueue
	RunCleanup = runCleanup
	Sums       = sums
)

// CleanupCachedItem runs the cache cleanup of candidate, an item in the cache
// of the remote repository whose key and incarnation a cleanup page names,
// as the page reaches it.
func (s *Service) CleanupCachedItem(
	ctx context.Context,
	repository string,
	incarnation types.RepositoryIncarnation,
	candidate types.Item,
) error {
	return s.cleanupCachedItem(
		ctx,
		cacheCleanupCursor{Repository: repository, Incarnation: incarnation},
		candidate,
	)
}
