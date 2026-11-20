package port

import (
	"context"
	"crypto/rand"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
)

// ProjectStore persists projects with their roles, environments, members
// and repository assignments. Global roles and environments are those with
// an empty ProjectKey. Writes that name a project (a project role or
// environment, a membership, an assignment's owner) return
// kernel.ErrNotFound when that project does not exist.
type ProjectStore interface {
	// Guard acquires the project administration snapshot inside a transaction,
	// without loading a collection. Acquire it before identity/artifact locks.
	Guard(ctx context.Context) error
	// Authority returns the project's lifetime and effective policy revision;
	// kernel.ErrNotFound when the project does not exist. Within a transaction
	// it takes the project snapshot guard before locking the project row. Read
	// all required project/assignment values and authorities in that transaction
	// for a coherent snapshot. Acquire this guard before artifact/source locks
	// when composing a project-aware publication transaction.
	Authority(ctx context.Context, projectKey string) (model.ProjectAuthority, error)
	// SharingAuthority returns the assignment/share revision; kernel.ErrNotFound
	// when the assignment does not exist. Within a transaction it locks that
	// assignment after taking the same project snapshot guard. Read the value
	// and authority in one transaction for a coherent snapshot. These records
	// do not authorize a caller by themselves.
	SharingAuthority(ctx context.Context, repoKey string) (model.SharingAuthority, error)
	// Create stores p; kernel.ErrConflict when the key exists.
	Create(ctx context.Context, p model.Project) error
	// Get returns the project.
	Get(ctx context.Context, key string) (model.Project, error)
	// Update replaces the project.
	Update(ctx context.Context, p model.Project) error
	// Storage returns private accounting and whether initialization occurred.
	// It returns ErrNotFound for a missing project. Compound reads/changes hold
	// Guard before artifact locks and keep initialization and delta publication
	// in the same transaction; this record alone reserves no capacity.
	Storage(ctx context.Context, key string) (model.ProjectStorage, bool, error)
	// SetStorage persists accounting with the caller's transaction. Bytes must
	// be nonnegative. A missing project is ErrNotFound; deleting a project drops
	// its accounting, so a recreated key always needs fresh initialization.
	SetStorage(ctx context.Context, key string, value model.ProjectStorage) error
	// Delete removes the project with its roles, environments, members,
	// the assignments of repositories it owns and its shares of others.
	Delete(ctx context.Context, key string) error
	// List returns every project ordered by key.
	List(ctx context.Context) ([]model.Project, error)
	// ForPrincipal returns distinct project keys with direct user or supplied
	// group assignments, ordered by key. The caller supplies current, authorized
	// groups; this discovery query grants no action and loads no role definitions.
	ForPrincipal(ctx context.Context, user string, groups []string) ([]string, error)
	// RoleProjects reads one page of distinct projects with direct user/group
	// assignments naming q.RoleName. Order is display name then key, bytewise,
	// reversed when Descending. More reports an additional matching project.
	RoleProjects(ctx context.Context, q ProjectRoleQuery) ([]model.Project, bool, error)

	// PutRole creates or replaces the role (r.ProjectKey, r.Name).
	PutRole(ctx context.Context, r model.Role) error
	// Role returns one role.
	Role(ctx context.Context, projectKey, name string) (model.Role, error)
	// Roles returns a project's roles, or the global ones for "", by name.
	Roles(ctx context.Context, projectKey string) ([]model.Role, error)
	// DeleteRole removes a role.
	DeleteRole(ctx context.Context, projectKey, name string) error

	// CreateEnvironment stores e. Environment names are unique across the
	// instance, global and project alike, so kernel.ErrConflict when the name
	// is taken anywhere.
	CreateEnvironment(ctx context.Context, e model.Environment) error
	// Environments returns a project's environments, or the global ones for
	// "", by name.
	Environments(ctx context.Context, projectKey string) ([]model.Environment, error)
	// RenameEnvironment renames an environment and every reference to it in
	// roles and repository assignments; kernel.ErrConflict when to is taken.
	RenameEnvironment(ctx context.Context, projectKey, from, to string) error
	// DeleteEnvironment removes an environment.
	DeleteEnvironment(ctx context.Context, projectKey, name string) error

	// PutMember creates or replaces a membership.
	PutMember(ctx context.Context, m model.ProjectMember) error
	// Member returns one membership.
	Member(
		ctx context.Context, projectKey string, typ model.PrincipalType, name string,
	) (model.ProjectMember, error)
	// Members returns a project's members of one principal type by name.
	Members(
		ctx context.Context, projectKey string, typ model.PrincipalType,
	) ([]model.ProjectMember, error)
	// DeleteMember removes a membership.
	DeleteMember(ctx context.Context, projectKey string, typ model.PrincipalType, name string) error

	// PutAssignment creates or replaces a repository's assignment and
	// shares. SharedWith comes back sorted and without duplicates.
	PutAssignment(ctx context.Context, a model.RepositoryAssignment) error
	// Assignment returns a repository's assignment.
	Assignment(ctx context.Context, repoKey string) (model.RepositoryAssignment, error)
	// Assignments returns the assignments of repositories assigned to or
	// shared with projectKey (including shared with all), by repository
	// key.
	Assignments(ctx context.Context, projectKey string) ([]model.RepositoryAssignment, error)
	// DeleteAssignment removes a repository's assignment and shares.
	DeleteAssignment(ctx context.Context, repoKey string) error
}

// ProjectRoleQuery selects a role-usage page. Limit must be positive; an empty
// AfterKey starts at the first row. The service validates global role identity
// and decodes its bound cursor before passing the last name/key tuple here.
type ProjectRoleQuery struct {
	RoleName   string
	Descending bool
	AfterName  string
	AfterKey   string
	Limit      int
}

// NewProjectAuthority allocates a new lifetime and initial policy revision.
func NewProjectAuthority() model.ProjectAuthority {
	return model.ProjectAuthority{
		Incarnation: model.ProjectIncarnation(rand.Text()),
		Revision:    model.AuthorizationRevision(rand.Text()),
	}
}

// NewSharingAuthority allocates a revision that cannot revive an older share.
func NewSharingAuthority() model.SharingAuthority {
	return model.SharingAuthority{Revision: model.AuthorizationRevision(rand.Text())}
}
