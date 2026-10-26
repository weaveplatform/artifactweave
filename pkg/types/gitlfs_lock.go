package types

import "time"

// GitLFSLock reserves a Git worktree path in one Git LFS repository.
// It is independent of the content-addressed LFS object index.
type GitLFSLock struct {
	ID       string
	RepoKey  string
	Path     string
	Owner    string
	LockedAt time.Time
}
