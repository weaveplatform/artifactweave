package inmem

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
)

// Projects implements store.Store.
func (s *Store) Projects() store.ProjectStore { return projectStore{s} }

type projectStore struct{ s *Store }

type (
	roleKey   struct{ project, name string }
	memberKey struct {
		project string
		typ     types.PrincipalType
		name    string
	}
)

func (p projectStore) projects() *table[string, types.Project] {
	return tableOf[string, types.Project](p.s, "projects")
}

func (p projectStore) roles() *table[roleKey, types.Role] {
	return tableOf[roleKey, types.Role](p.s, "roles")
}

func (p projectStore) environments() *table[string, types.Environment] {
	return tableOf[string, types.Environment](p.s, "environments")
}

func (p projectStore) members() *table[memberKey, types.ProjectMember] {
	return tableOf[memberKey, types.ProjectMember](p.s, "project_members")
}

func (p projectStore) assignments() *table[string, types.RepositoryAssignment] {
	return tableOf[string, types.RepositoryAssignment](p.s, "project_repositories")
}

// requireProject returns ErrNotFound unless the project exists; global
// records (an empty key) need none. Call it under the lock.
func (p projectStore) requireProject(key string) error {
	if key != "" && !p.projects().has(key) {
		return notFound("project", key)
	}
	return nil
}

func (p projectStore) Create(ctx context.Context, v types.Project) error {
	return p.s.do(ctx, func() error {
		if p.projects().has(v.Key) {
			return conflict("project", v.Key)
		}
		p.projects().put(v.Key, v)
		return nil
	})
}

func (p projectStore) Get(ctx context.Context, key string) (types.Project, error) {
	var out types.Project
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.projects().get(key); !ok {
			return notFound("project", key)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Update(ctx context.Context, v types.Project) error {
	return p.s.do(ctx, func() error {
		if !p.projects().has(v.Key) {
			return notFound("project", v.Key)
		}
		p.projects().put(v.Key, v)
		return nil
	})
}

func (p projectStore) Delete(ctx context.Context, key string) error {
	return p.s.do(ctx, func() error {
		if !p.projects().del(key) {
			return notFound("project", key)
		}
		p.roles().deleteWhere(func(k roleKey, _ types.Role) bool { return k.project == key })
		p.environments().
			deleteWhere(func(_ string, e types.Environment) bool { return e.ProjectKey == key })
		p.members().
			deleteWhere(func(k memberKey, _ types.ProjectMember) bool { return k.project == key })
		p.assignments().
			deleteWhere(func(_ string, a types.RepositoryAssignment) bool { return a.ProjectKey == key })
		for repo, a := range p.assignments().rows {
			if slices.Contains(a.SharedWith, key) {
				a = store.Clone(a)
				a.SharedWith = slices.DeleteFunc(
					a.SharedWith,
					func(k string) bool { return k == key },
				)
				p.assignments().put(repo, a)
			}
		}
		return nil
	})
}

func (p projectStore) List(ctx context.Context) ([]types.Project, error) {
	var out []types.Project
	err := p.s.do(ctx, func() error {
		out = p.projects().
			all(nil, func(a, b types.Project) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (p projectStore) PutRole(ctx context.Context, r types.Role) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(r.ProjectKey); err != nil {
			return err
		}
		p.roles().put(roleKey{r.ProjectKey, r.Name}, r)
		return nil
	})
}

func (p projectStore) Role(ctx context.Context, projectKey, name string) (types.Role, error) {
	var out types.Role
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.roles().get(roleKey{projectKey, name}); !ok {
			return notFound("role", projectKey+"/"+name)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Roles(ctx context.Context, projectKey string) ([]types.Role, error) {
	var out []types.Role
	err := p.s.do(ctx, func() error {
		out = p.roles().all(func(k roleKey, _ types.Role) bool { return k.project == projectKey },
			func(a, b types.Role) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteRole(ctx context.Context, projectKey, name string) error {
	return p.s.do(ctx, func() error {
		if !p.roles().del(roleKey{projectKey, name}) {
			return notFound("role", projectKey+"/"+name)
		}
		return nil
	})
}

func (p projectStore) CreateEnvironment(ctx context.Context, e types.Environment) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(e.ProjectKey); err != nil {
			return err
		}
		if p.environments().has(e.Name) {
			return conflict("environment", e.Name)
		}
		p.environments().put(e.Name, e)
		return nil
	})
}

func (p projectStore) Environments(
	ctx context.Context,
	projectKey string,
) ([]types.Environment, error) {
	var out []types.Environment
	err := p.s.do(ctx, func() error {
		out = p.environments().
			all(func(_ string, e types.Environment) bool { return e.ProjectKey == projectKey },
				func(a, b types.Environment) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) RenameEnvironment(ctx context.Context, projectKey, from, to string) error {
	return p.s.do(ctx, func() error {
		e, ok := p.environments().get(from)
		if !ok || e.ProjectKey != projectKey {
			return notFound("environment", projectKey+"/"+from)
		}
		if from == to {
			return nil
		}
		if p.environments().has(to) {
			return conflict("environment", to)
		}
		p.environments().del(from)
		e.Name = to
		p.environments().put(to, e)
		for k, r := range p.roles().rows {
			if slices.Contains(r.Environments, from) {
				r = store.Clone(r)
				renameEnv(r.Environments, from, to)
				p.roles().put(k, r)
			}
		}
		for k, a := range p.assignments().rows {
			if slices.Contains(a.Environments, from) {
				a = store.Clone(a)
				renameEnv(a.Environments, from, to)
				p.assignments().put(k, a)
			}
		}
		return nil
	})
}

// renameEnv replaces from with to in names.
func renameEnv(names []string, from, to string) {
	for i, n := range names {
		if n == from {
			names[i] = to
		}
	}
}

func (p projectStore) DeleteEnvironment(ctx context.Context, projectKey, name string) error {
	return p.s.do(ctx, func() error {
		e, ok := p.environments().get(name)
		if !ok || e.ProjectKey != projectKey {
			return notFound("environment", projectKey+"/"+name)
		}
		p.environments().del(name)
		return nil
	})
}

func (p projectStore) PutMember(ctx context.Context, m types.ProjectMember) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(m.ProjectKey); err != nil {
			return err
		}
		p.members().put(memberKey{m.ProjectKey, m.Type, m.Name}, m)
		return nil
	})
}

func (p projectStore) Member(
	ctx context.Context, projectKey string, typ types.PrincipalType, name string,
) (types.ProjectMember, error) {
	var out types.ProjectMember
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.members().get(memberKey{projectKey, typ, name}); !ok {
			return notFound("member", fmt.Sprintf("%s/%s/%s", projectKey, typ, name))
		}
		return nil
	})
	return out, err
}

func (p projectStore) Members(
	ctx context.Context, projectKey string, typ types.PrincipalType,
) ([]types.ProjectMember, error) {
	var out []types.ProjectMember
	err := p.s.do(ctx, func() error {
		out = p.members().all(
			func(k memberKey, _ types.ProjectMember) bool { return k.project == projectKey && k.typ == typ },
			func(a, b types.ProjectMember) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteMember(
	ctx context.Context,
	projectKey string,
	typ types.PrincipalType,
	name string,
) error {
	return p.s.do(ctx, func() error {
		if !p.members().del(memberKey{projectKey, typ, name}) {
			return notFound("member", fmt.Sprintf("%s/%s/%s", projectKey, typ, name))
		}
		return nil
	})
}

func (p projectStore) PutAssignment(ctx context.Context, a types.RepositoryAssignment) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(a.ProjectKey); err != nil {
			return err
		}
		a.SharedWith = slices.Compact(slices.Sorted(slices.Values(a.SharedWith)))
		p.assignments().put(a.RepoKey, a)
		return nil
	})
}

func (p projectStore) Assignment(
	ctx context.Context,
	repoKey string,
) (types.RepositoryAssignment, error) {
	var out types.RepositoryAssignment
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.assignments().get(repoKey); !ok {
			return notFound("assignment", repoKey)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Assignments(
	ctx context.Context,
	projectKey string,
) ([]types.RepositoryAssignment, error) {
	var out []types.RepositoryAssignment
	err := p.s.do(ctx, func() error {
		out = p.assignments().all(func(_ string, a types.RepositoryAssignment) bool {
			return a.ProjectKey == projectKey || a.SharedWithAll ||
				slices.Contains(a.SharedWith, projectKey)
		}, func(a, b types.RepositoryAssignment) int { return strings.Compare(a.RepoKey, b.RepoKey) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteAssignment(ctx context.Context, repoKey string) error {
	return p.s.do(ctx, func() error {
		if !p.assignments().del(repoKey) {
			return notFound("assignment", repoKey)
		}
		return nil
	})
}

// Webhooks implements store.Store.
func (s *Store) Webhooks() store.WebhookStore { return webhookStore{s} }

type webhookStore struct{ s *Store }

func (w webhookStore) hooks() *table[string, types.WebhookSubscription] {
	return tableOf[string, types.WebhookSubscription](w.s, "webhooks")
}

func (w webhookStore) deliveries() *table[int64, types.WebhookDelivery] {
	return tableOf[int64, types.WebhookDelivery](w.s, "webhook_deliveries")
}

func (w webhookStore) Create(ctx context.Context, v types.WebhookSubscription) error {
	return w.s.do(ctx, func() error {
		if w.hooks().has(v.Key) {
			return conflict("webhook", v.Key)
		}
		w.hooks().put(v.Key, v)
		return nil
	})
}

func (w webhookStore) Get(ctx context.Context, key string) (types.WebhookSubscription, error) {
	var out types.WebhookSubscription
	err := w.s.do(ctx, func() error {
		var ok bool
		if out, ok = w.hooks().get(key); !ok {
			return notFound("webhook", key)
		}
		return nil
	})
	return out, err
}

func (w webhookStore) Update(ctx context.Context, v types.WebhookSubscription) error {
	return w.s.do(ctx, func() error {
		if !w.hooks().has(v.Key) {
			return notFound("webhook", v.Key)
		}
		w.hooks().put(v.Key, v)
		return nil
	})
}

func (w webhookStore) Delete(ctx context.Context, key string) error {
	return w.s.do(ctx, func() error {
		if !w.hooks().del(key) {
			return notFound("webhook", key)
		}
		w.deliveries().
			deleteWhere(func(_ int64, d types.WebhookDelivery) bool { return d.WebhookKey == key })
		return nil
	})
}

func (w webhookStore) List(
	ctx context.Context,
	f store.WebhookFilter,
) ([]types.WebhookSubscription, error) {
	var out []types.WebhookSubscription
	err := w.s.do(ctx, func() error {
		out = w.hooks().all(func(_ string, v types.WebhookSubscription) bool {
			return (f.Domain == "" || v.Domain == f.Domain) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey) &&
				(!f.EnabledOnly || v.Enabled)
		}, func(a, b types.WebhookSubscription) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (w webhookStore) AddDelivery(
	ctx context.Context,
	d types.WebhookDelivery,
) (types.WebhookDelivery, error) {
	var out types.WebhookDelivery
	err := w.s.do(ctx, func() error {
		if !w.hooks().has(d.WebhookKey) {
			return notFound("webhook", d.WebhookKey)
		}
		d.ID = w.s.nextID("webhook_deliveries")
		w.deliveries().put(d.ID, d)
		out, _ = w.deliveries().get(d.ID)
		return nil
	})
	return out, err
}

// attemptKey sorts deliveries the way the SQL stores do, with an unset time
// as 0.
func attemptKey(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func (w webhookStore) Deliveries(
	ctx context.Context,
	f store.DeliveryFilter,
) ([]types.WebhookDelivery, error) {
	var out []types.WebhookDelivery
	err := w.s.do(ctx, func() error {
		out = w.deliveries().all(func(_ int64, d types.WebhookDelivery) bool {
			// Bounds compare at stored precision, as the SQL stores do.
			at := d.AttemptedAt
			return (f.WebhookKey == "" || d.WebhookKey == f.WebhookKey) &&
				(f.From.IsZero() || (!at.IsZero() && at.UnixMicro() >= f.From.UnixMicro())) &&
				(f.To.IsZero() || (!at.IsZero() && at.UnixMicro() < f.To.UnixMicro()))
		}, func(a, b types.WebhookDelivery) int {
			if c := cmp.Compare(attemptKey(b.AttemptedAt), attemptKey(a.AttemptedAt)); c != 0 {
				return c
			}
			return cmp.Compare(b.ID, a.ID)
		})
		out = out[:min(len(out), deliveryLimit(f.Limit))]
		return nil
	})
	return out, err
}

// deliveryLimit applies the default limit to a delivery listing.
func deliveryLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}

func (w webhookStore) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := w.s.do(ctx, func() error {
		n = w.deliveries().deleteWhere(func(_ int64, d types.WebhookDelivery) bool {
			return !d.AttemptedAt.IsZero() && d.AttemptedAt.UnixMicro() < before.UnixMicro()
		})
		return nil
	})
	return n, err
}

// Cleanup implements store.Store.
func (s *Store) Cleanup() store.CleanupStore { return cleanupStore{s} }

type cleanupStore struct{ s *Store }

func (c cleanupStore) policies() *table[string, types.CleanupPolicy] {
	return tableOf[string, types.CleanupPolicy](c.s, "cleanup_policies")
}

func (c cleanupStore) runs() *table[int64, types.CleanupRun] {
	return tableOf[int64, types.CleanupRun](c.s, "cleanup_runs")
}

func (c cleanupStore) CreatePolicy(ctx context.Context, p types.CleanupPolicy) error {
	return c.s.do(ctx, func() error {
		if c.policies().has(p.Key) {
			return conflict("cleanup policy", p.Key)
		}
		c.policies().put(p.Key, p)
		return nil
	})
}

func (c cleanupStore) Policy(ctx context.Context, key string) (types.CleanupPolicy, error) {
	var out types.CleanupPolicy
	err := c.s.do(ctx, func() error {
		var ok bool
		if out, ok = c.policies().get(key); !ok {
			return notFound("cleanup policy", key)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) UpdatePolicy(ctx context.Context, p types.CleanupPolicy) error {
	return c.s.do(ctx, func() error {
		if !c.policies().has(p.Key) {
			return notFound("cleanup policy", p.Key)
		}
		c.policies().put(p.Key, p)
		return nil
	})
}

func (c cleanupStore) DeletePolicy(ctx context.Context, key string) error {
	return c.s.do(ctx, func() error {
		if !c.policies().del(key) {
			return notFound("cleanup policy", key)
		}
		return nil
	})
}

func (c cleanupStore) Policies(
	ctx context.Context,
	f store.PolicyFilter,
) ([]types.CleanupPolicy, error) {
	var out []types.CleanupPolicy
	err := c.s.do(ctx, func() error {
		out = c.policies().all(func(_ string, p types.CleanupPolicy) bool {
			return (f.ItemType == "" || p.ItemType == f.ItemType) &&
				(f.ProjectKey == "" || p.ProjectKey == f.ProjectKey)
		}, func(a, b types.CleanupPolicy) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c cleanupStore) CreateRun(ctx context.Context, r types.CleanupRun) (types.CleanupRun, error) {
	var out types.CleanupRun
	err := c.s.do(ctx, func() error {
		r.ID = c.s.nextID("cleanup_runs")
		c.runs().put(r.ID, r)
		out, _ = c.runs().get(r.ID)
		return nil
	})
	return out, err
}

func (c cleanupStore) Run(ctx context.Context, id int64) (types.CleanupRun, error) {
	var out types.CleanupRun
	err := c.s.do(ctx, func() error {
		var ok bool
		if out, ok = c.runs().get(id); !ok {
			return notFound("cleanup run", id)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) UpdateRun(ctx context.Context, r types.CleanupRun) error {
	return c.s.do(ctx, func() error {
		if !c.runs().has(r.ID) {
			return notFound("cleanup run", r.ID)
		}
		c.runs().put(r.ID, r)
		return nil
	})
}

// newestFirst orders runs by descending ID.
func newestFirst(a, b types.CleanupRun) int { return cmp.Compare(b.ID, a.ID) }

func (c cleanupStore) Runs(
	ctx context.Context,
	f store.RunFilter,
	p store.Page,
) ([]types.CleanupRun, string, error) {
	var (
		out  []types.CleanupRun
		next string
	)
	err := c.s.do(ctx, func() error {
		rows := c.runs().all(func(_ int64, r types.CleanupRun) bool {
			return (f.PolicyKey == "" || r.PolicyKey == f.PolicyKey) &&
				(f.ItemType == "" || r.ItemType == f.ItemType) &&
				(len(f.Statuses) == 0 || slices.Contains(f.Statuses, r.Status))
		}, newestFirst)
		var err error
		out, next, err = paginate(
			rows,
			p,
			true,
			func(r types.CleanupRun) []any { return []any{r.ID} },
		)
		return err
	})
	return out, next, err
}

func (c cleanupStore) LastRun(ctx context.Context, policyKey string) (types.CleanupRun, error) {
	var out types.CleanupRun
	err := c.s.do(ctx, func() error {
		rows := c.runs().
			all(func(_ int64, r types.CleanupRun) bool { return r.PolicyKey == policyKey }, newestFirst)
		if len(rows) == 0 {
			return notFound("cleanup run of policy", policyKey)
		}
		out = rows[0]
		return nil
	})
	return out, err
}

// Replications implements store.Store.
func (s *Store) Replications() store.ReplicationStore { return replicationStore{s} }

type replicationStore struct{ s *Store }

type replicationRunKey struct {
	key     string
	started int64
}

func (rs replicationStore) replications() *table[string, types.Replication] {
	return tableOf[string, types.Replication](rs.s, "replications")
}

func (rs replicationStore) runs() *table[replicationRunKey, types.ReplicationRun] {
	return tableOf[replicationRunKey, types.ReplicationRun](rs.s, "replication_runs")
}

func (rs replicationStore) Put(ctx context.Context, r types.Replication) error {
	if r.Key == "" {
		return fmt.Errorf("replication of %s: key is required: %w", r.RepoKey, store.ErrInvalid)
	}
	return rs.s.do(ctx, func() error {
		rs.replications().put(r.Key, r)
		return nil
	})
}

func (rs replicationStore) Get(ctx context.Context, key string) (types.Replication, error) {
	var out types.Replication
	err := rs.s.do(ctx, func() error {
		var ok bool
		if out, ok = rs.replications().get(key); !ok {
			return notFound("replication", key)
		}
		return nil
	})
	return out, err
}

func compareReplications(a, b types.Replication) int {
	if c := strings.Compare(a.RepoKey, b.RepoKey); c != 0 {
		return c
	}
	return strings.Compare(a.Key, b.Key)
}

func (rs replicationStore) ForRepo(
	ctx context.Context,
	repoKey string,
) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.do(ctx, func() error {
		out = rs.replications().
			all(func(_ string, r types.Replication) bool { return r.RepoKey == repoKey },
				compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) List(ctx context.Context) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.do(ctx, func() error {
		out = rs.replications().all(nil, compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) Delete(ctx context.Context, key string) error {
	return rs.s.do(ctx, func() error {
		if !rs.replications().del(key) {
			return notFound("replication", key)
		}
		rs.runs().
			deleteWhere(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key })
		return nil
	})
}

func (rs replicationStore) DeleteForRepo(ctx context.Context, repoKey string) error {
	return rs.s.do(ctx, func() error {
		for key, r := range rs.replications().rows {
			if r.RepoKey == repoKey {
				rs.replications().del(key)
				rs.runs().
					deleteWhere(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key })
			}
		}
		return nil
	})
}

func (rs replicationStore) PutRun(ctx context.Context, r types.ReplicationRun) error {
	return rs.s.do(ctx, func() error {
		if !rs.replications().has(r.ReplicationKey) {
			return notFound("replication", r.ReplicationKey)
		}
		r = store.Normalise(r)
		rs.runs().put(replicationRunKey{r.ReplicationKey, r.Started.UnixMicro()}, r)
		return nil
	})
}

func (rs replicationStore) LastRun(ctx context.Context, key string) (types.ReplicationRun, error) {
	var out types.ReplicationRun
	err := rs.s.do(ctx, func() error {
		rows := rs.runs().
			all(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key },
				func(a, b types.ReplicationRun) int { return b.Started.Compare(a.Started) })
		if len(rows) == 0 {
			return notFound("replication run of", key)
		}
		out = rows[0]
		return nil
	})
	return out, err
}

// Settings implements store.Store.
func (s *Store) Settings() store.SettingsStore { return settingStore{s} }

// settingStore keeps values as JSON, so a read decodes exactly what the SQL
// stores would.
type settingStore struct{ s *Store }

func (st settingStore) values() *table[string, string] {
	return tableOf[string, string](st.s, "settings")
}

func (st settingStore) Get(ctx context.Context, key string, into any) error {
	return st.s.do(ctx, func() error {
		v, ok := st.values().get(key)
		if !ok {
			return notFound("setting", key)
		}
		if err := json.Unmarshal([]byte(v), into); err != nil {
			return fmt.Errorf("decode setting %s: %w", key, err)
		}
		return nil
	})
}

func (st settingStore) Put(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	return st.s.do(ctx, func() error {
		st.values().put(key, string(b))
		return nil
	})
}

func (st settingStore) Delete(ctx context.Context, key string) error {
	return st.s.do(ctx, func() error {
		if !st.values().del(key) {
			return notFound("setting", key)
		}
		return nil
	})
}

func (st settingStore) Keys(ctx context.Context) ([]string, error) {
	var out []string
	err := st.s.do(ctx, func() error {
		for k := range st.values().rows {
			out = append(out, k)
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

// Leases implements store.Store.
func (s *Store) Leases() store.LeaseStore { return leaseStore{s} }

type leaseStore struct{ s *Store }

func (l leaseStore) leases() *table[string, types.Lease] {
	return tableOf[string, types.Lease](l.s, "leases")
}

func (l leaseStore) Acquire(ctx context.Context, lease types.Lease, now time.Time) (bool, error) {
	if lease.Name == "" || lease.Holder == "" || lease.Expires.IsZero() {
		return false, fmt.Errorf(
			"lease %q: name, holder and expiry are required: %w",
			lease.Name,
			store.ErrInvalid,
		)
	}
	var took bool
	err := l.s.do(ctx, func() error {
		cur, ok := l.leases().get(lease.Name)
		// Compare at stored precision, as the SQL condition expires_at <= now does.
		if ok && cur.Holder != lease.Holder && cur.Expires.UnixMicro() > now.UnixMicro() {
			return nil
		}
		l.leases().put(lease.Name, lease)
		took = true
		return nil
	})
	return took, err
}

func (l leaseStore) Release(ctx context.Context, name, holder string) error {
	return l.s.do(ctx, func() error {
		if cur, ok := l.leases().get(name); ok && cur.Holder == holder {
			l.leases().del(name)
		}
		return nil
	})
}

func (l leaseStore) List(ctx context.Context) ([]types.Lease, error) {
	var out []types.Lease
	err := l.s.do(ctx, func() error {
		out = l.leases().
			all(nil, func(a, b types.Lease) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

// Configs implements store.Store.
func (s *Store) Configs() store.ConfigStore { return configStore{s} }

type configStore struct{ s *Store }

type configKey struct {
	kind types.ConfigKind
	key  string
}

func (c configStore) docs() *table[configKey, types.ConfigDocument] {
	return tableOf[configKey, types.ConfigDocument](c.s, "config_documents")
}

func (c configStore) Put(ctx context.Context, d types.ConfigDocument) error {
	if d.Kind == "" || d.Key == "" {
		return fmt.Errorf(
			"config document %q/%q: kind and key are required: %w",
			d.Kind,
			d.Key,
			store.ErrInvalid,
		)
	}
	return c.s.do(ctx, func() error {
		// The SQL stores keep no row for an empty secret, so neither does this.
		secrets := make(map[string]string, len(d.Secrets))
		for k, v := range d.Secrets {
			if v != "" {
				secrets[k] = v
			}
		}
		d.Secrets = secrets
		c.docs().put(configKey{d.Kind, d.Key}, d)
		return nil
	})
}

func (c configStore) Get(
	ctx context.Context,
	kind types.ConfigKind,
	key string,
) (types.ConfigDocument, error) {
	var out types.ConfigDocument
	err := c.s.do(ctx, func() error {
		var ok bool
		if out, ok = c.docs().get(configKey{kind, key}); !ok {
			return notFound("config "+string(kind), key)
		}
		return nil
	})
	return out, err
}

func (c configStore) List(
	ctx context.Context,
	kind types.ConfigKind,
) ([]types.ConfigDocument, error) {
	var out []types.ConfigDocument
	err := c.s.do(ctx, func() error {
		out = c.docs().all(func(k configKey, _ types.ConfigDocument) bool { return k.kind == kind },
			func(a, b types.ConfigDocument) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c configStore) Delete(ctx context.Context, kind types.ConfigKind, key string) error {
	return c.s.do(ctx, func() error {
		if !c.docs().del(configKey{kind, key}) {
			return notFound("config "+string(kind), key)
		}
		return nil
	})
}
