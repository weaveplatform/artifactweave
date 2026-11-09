package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ProjectStore persists projects with their roles, environments, members
// and repository assignments. Global roles and environments are those with
// an empty ProjectKey. Writes that name a project (a project role or
// environment, a membership, an assignment's owner) return ErrNotFound when
// that project does not exist.
type ProjectStore interface {
	// Create stores p; ErrConflict when the key exists.
	Create(ctx context.Context, p types.Project) error
	// Get returns the project.
	Get(ctx context.Context, key string) (types.Project, error)
	// Update replaces the project.
	Update(ctx context.Context, p types.Project) error
	// Delete removes the project with its roles, environments, members,
	// the assignments of repositories it owns and its shares of others.
	Delete(ctx context.Context, key string) error
	// List returns every project ordered by key.
	List(ctx context.Context) ([]types.Project, error)

	// PutRole creates or replaces the role (r.ProjectKey, r.Name).
	PutRole(ctx context.Context, r types.Role) error
	// Role returns one role.
	Role(ctx context.Context, projectKey, name string) (types.Role, error)
	// Roles returns a project's roles, or the global ones for "", by name.
	Roles(ctx context.Context, projectKey string) ([]types.Role, error)
	// DeleteRole removes a role.
	DeleteRole(ctx context.Context, projectKey, name string) error

	// CreateEnvironment stores e. Environment names are unique across the
	// instance, global and project alike, so ErrConflict when the name is
	// taken anywhere.
	CreateEnvironment(ctx context.Context, e types.Environment) error
	// Environments returns a project's environments, or the global ones for
	// "", by name.
	Environments(ctx context.Context, projectKey string) ([]types.Environment, error)
	// RenameEnvironment renames an environment and every reference to it in
	// roles and repository assignments; ErrConflict when to is taken.
	RenameEnvironment(ctx context.Context, projectKey, from, to string) error
	// DeleteEnvironment removes an environment.
	DeleteEnvironment(ctx context.Context, projectKey, name string) error

	// PutMember creates or replaces a membership.
	PutMember(ctx context.Context, m types.ProjectMember) error
	// Member returns one membership.
	Member(
		ctx context.Context, projectKey string, typ types.PrincipalType, name string,
	) (types.ProjectMember, error)
	// Members returns a project's members of one principal type by name.
	Members(
		ctx context.Context, projectKey string, typ types.PrincipalType,
	) ([]types.ProjectMember, error)
	// DeleteMember removes a membership.
	DeleteMember(ctx context.Context, projectKey string, typ types.PrincipalType, name string) error

	// PutAssignment creates or replaces a repository's assignment and
	// shares. SharedWith comes back sorted and without duplicates.
	PutAssignment(ctx context.Context, a types.RepositoryAssignment) error
	// Assignment returns a repository's assignment.
	Assignment(ctx context.Context, repoKey string) (types.RepositoryAssignment, error)
	// Assignments returns the assignments of repositories assigned to or
	// shared with projectKey (including shared with all), by repository
	// key.
	Assignments(ctx context.Context, projectKey string) ([]types.RepositoryAssignment, error)
	// DeleteAssignment removes a repository's assignment and shares.
	DeleteAssignment(ctx context.Context, repoKey string) error
}
