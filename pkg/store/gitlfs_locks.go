package store

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// GitLFSLockFilter restricts a lock listing to one repository and optional
// exact path or ID. RepoKey is required.
type GitLFSLockFilter struct{ RepoKey, Path, ID string }

// GitLFSLockStore keeps active Git worktree locks separately from LFS objects.
// Create is exclusive on (repository, path); Delete returns the removed row.
type GitLFSLockStore interface {
	Create(context.Context, types.GitLFSLock) error
	Get(context.Context, string, string) (types.GitLFSLock, error)
	List(context.Context, GitLFSLockFilter, Page) ([]types.GitLFSLock, string, error)
	Delete(context.Context, string, string) (types.GitLFSLock, error)
}
