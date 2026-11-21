package api

import "context"

// The built-in environments. They are not stored: they cannot be renamed or
// deleted, and a repository belongs to DEV unless told otherwise.
const (
	EnvironmentDev  = "DEV"
	EnvironmentProd = "PROD"
)

// BuiltinEnvironments are the global environments every instance has.
var BuiltinEnvironments = []string{EnvironmentDev, EnvironmentProd}

// Projects reads projects, their environments and their members (decision
// 0013), for the modules that check a repository's project and
// environments. Each call uses ctx for the store it reads, so a call inside
// a store transaction takes part in it.
type Projects interface {
	// Guard serializes a current authority snapshot with project, membership,
	// role and credential changes until the caller's transaction commits. Call
	// it before repository, source, work or item locks. It joins the shared
	// store transaction; outside a transaction it provides no publication fence.
	Guard(ctx context.Context) error
	// BindRepository synchronizes canonical ownership/environments in the
	// caller's transaction. fresh discards shares from an earlier repository
	// lifetime; updates retain current shares. Call after taking Guard.
	BindRepository(ctx context.Context, key, project string, environments []string, fresh bool) error
	// RemoveRepository removes binding and shares, tolerating an absent binding.
	// It joins the guarded transaction that deletes the repository configuration.
	RemoveRepository(ctx context.Context, key string) error
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
	// DisplayName reads descriptive metadata, not authority. Callers must
	// authorize the project first and hold Guard when freezing a run snapshot.
	// Missing projects and store failures retain their usual classification.
	DisplayName(ctx context.Context, project string) (string, error)
	// Incarnation returns a project's opaque lifetime identity, not authority
	// to act. A recreated key has a different identity. Hold Guard in the
	// caller's transaction when binding or checking durable background work.
	// A missing project returns kernel.ErrNotFound.
	Incarnation(ctx context.Context, project string) (string, error)
}
