package api

import "context"

// Projects reads projects, their environments and their members (decision
// 0013), for the modules that check a repository's project and
// environments. Each call uses ctx for the store it reads, so a call inside
// a store transaction takes part in it.
type Projects interface {
	// Environments returns the names of a project's environments, or of the
	// global environments for project "", ordered by name and spelled as
	// stored; a project that does not exist has none. The built-in DEV and
	// PROD are not among them.
	Environments(ctx context.Context, project string) ([]string, error)
	// Member returns nil when user is a member of project; an error matching
	// kernel.ErrNotFound when they are not, or the project does not exist.
	Member(ctx context.Context, project, user string) error
	// Exists returns nil when project exists; an error matching
	// kernel.ErrNotFound when it does not.
	Exists(ctx context.Context, project string) error
}
