package store

import (
	"context"
	"time"

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

// WebhookFilter narrows subscription listings.
type WebhookFilter struct {
	Domain      types.WebhookDomain
	ProjectKey  string
	EnabledOnly bool
}

// DeliveryFilter narrows delivery records to those attempted at or after
// From and before To. Zero times are open bounds; an empty WebhookKey
// matches every subscription.
type DeliveryFilter struct {
	WebhookKey string
	From       time.Time
	To         time.Time
	Limit      int
}

// WebhookStore persists event subscriptions and their delivery records.
// Handler secrets are sealed.
type WebhookStore interface {
	// Create stores w; ErrConflict when the key exists.
	Create(ctx context.Context, w types.WebhookSubscription) error
	// Get returns the subscription, secrets included.
	Get(ctx context.Context, key string) (types.WebhookSubscription, error)
	// Update replaces the subscription.
	Update(ctx context.Context, w types.WebhookSubscription) error
	// Delete removes the subscription and its delivery records.
	Delete(ctx context.Context, key string) error
	// List returns matching subscriptions ordered by key.
	List(ctx context.Context, f WebhookFilter) ([]types.WebhookSubscription, error)
	// AddDelivery records a delivery attempt and returns it with its ID;
	// ErrNotFound when the subscription does not exist.
	AddDelivery(ctx context.Context, d types.WebhookDelivery) (types.WebhookDelivery, error)
	// Deliveries returns matching records newest first, at most f.Limit
	// (DefaultLimit when zero).
	Deliveries(ctx context.Context, f DeliveryFilter) ([]types.WebhookDelivery, error)
	// PruneDeliveries removes records attempted before t and returns how
	// many.
	PruneDeliveries(ctx context.Context, before time.Time) (int64, error)
}

// PolicyFilter narrows cleanup policy listings.
type PolicyFilter struct {
	ItemType   types.CleanupItemType
	ProjectKey string
}

// RunFilter narrows cleanup run listings.
type RunFilter struct {
	PolicyKey string
	ItemType  types.CleanupItemType
	Statuses  []types.RunStatus
}

// CleanupStore persists package and build cleanup policies and their runs.
type CleanupStore interface {
	// CreatePolicy stores p; ErrConflict when the key exists.
	CreatePolicy(ctx context.Context, p types.CleanupPolicy) error
	// Policy returns the policy.
	Policy(ctx context.Context, key string) (types.CleanupPolicy, error)
	// UpdatePolicy replaces the policy.
	UpdatePolicy(ctx context.Context, p types.CleanupPolicy) error
	// DeletePolicy removes the policy; its runs are kept for the run views.
	DeletePolicy(ctx context.Context, key string) error
	// Policies returns matching policies ordered by key.
	Policies(ctx context.Context, f PolicyFilter) ([]types.CleanupPolicy, error)

	// CreateRun stores r and returns it with its assigned ID.
	CreateRun(ctx context.Context, r types.CleanupRun) (types.CleanupRun, error)
	// Run returns the run with id.
	Run(ctx context.Context, id int64) (types.CleanupRun, error)
	// UpdateRun replaces a run.
	UpdateRun(ctx context.Context, r types.CleanupRun) error
	// Runs returns matching runs newest first.
	Runs(ctx context.Context, f RunFilter, p Page) ([]types.CleanupRun, string, error)
	// LastRun returns a policy's most recent run.
	LastRun(ctx context.Context, policyKey string) (types.CleanupRun, error)
}

// ReplicationStore persists replication targets and their run history. The
// replication key is assigned by the caller; the password is sealed.
type ReplicationStore interface {
	// Put creates or replaces the replication with r.Key; ErrInvalid when
	// the key is empty.
	Put(ctx context.Context, r types.Replication) error
	// Get returns the replication with key.
	Get(ctx context.Context, key string) (types.Replication, error)
	// ForRepo returns a repository's replications ordered by key.
	ForRepo(ctx context.Context, repoKey string) ([]types.Replication, error)
	// List returns every replication ordered by (repository, key).
	List(ctx context.Context) ([]types.Replication, error)
	// Delete removes a replication and its runs.
	Delete(ctx context.Context, key string) error
	// DeleteForRepo removes every replication of a repository.
	DeleteForRepo(ctx context.Context, repoKey string) error
	// PutRun creates or replaces the run (r.ReplicationKey, r.Started);
	// ErrNotFound when the replication does not exist.
	PutRun(ctx context.Context, r types.ReplicationRun) error
	// LastRun returns a replication's most recent run.
	LastRun(ctx context.Context, key string) (types.ReplicationRun, error)
}

// SettingsStore persists the singleton settings (types.Setting* keys) as
// JSON values.
type SettingsStore interface {
	// Get decodes the value stored under key into into; ErrNotFound when
	// nothing is stored, which callers read as "use the default".
	Get(ctx context.Context, key string, into any) error
	// Put stores v under key, replacing any value.
	Put(ctx context.Context, key string, v any) error
	// Delete removes the value, restoring the default; ErrNotFound when
	// nothing was stored.
	Delete(ctx context.Context, key string) error
	// Keys returns the keys with stored values, ordered.
	Keys(ctx context.Context) ([]string, error)
}

// LeaseStore persists named leases on background work.
type LeaseStore interface {
	// Acquire takes the lease l.Name for l.Holder until l.Expires when it is
	// free, expired at now, or already held by l.Holder (a renewal), and
	// reports whether it did. The stored lease is l as given, so a renewal
	// that wants to keep its acquisition time passes it back. ErrInvalid
	// when the name, holder or expiry is missing.
	Acquire(ctx context.Context, l types.Lease, now time.Time) (bool, error)
	// Release gives up a lease held by holder; releasing a lease someone
	// else holds, or nobody does, changes nothing.
	Release(ctx context.Context, name, holder string) error
	// List returns every lease ordered by name.
	List(ctx context.Context) ([]types.Lease, error)
}

// ConfigStore persists configuration documents. Their secrets are sealed;
// a secret with an empty value is not stored.
type ConfigStore interface {
	// Put creates or replaces the document (d.Kind, d.Key) and its secrets;
	// ErrInvalid when the kind or key is empty.
	Put(ctx context.Context, d types.ConfigDocument) error
	// Get returns one document, secrets included.
	Get(ctx context.Context, kind types.ConfigKind, key string) (types.ConfigDocument, error)
	// List returns the documents of a kind ordered by key.
	List(ctx context.Context, kind types.ConfigKind) ([]types.ConfigDocument, error)
	// Delete removes a document.
	Delete(ctx context.Context, kind types.ConfigKind, key string) error
}
