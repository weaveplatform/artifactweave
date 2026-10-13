package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
)

// runOperations covers projects, webhooks, cleanup, replication, settings,
// leases and configuration documents.
func runOperations(t *testing.T, newStore Factory) {
	t.Run("Projects", func(t *testing.T) { testProjects(t, newStore(t)) })
	t.Run("Roles", func(t *testing.T) { testRoles(t, newStore(t)) })
	t.Run("Environments", func(t *testing.T) { testEnvironments(t, newStore(t)) })
	t.Run("ProjectMembers", func(t *testing.T) { testProjectMembers(t, newStore(t)) })
	t.Run("Assignments", func(t *testing.T) { testAssignments(t, newStore(t)) })
	t.Run("Webhooks", func(t *testing.T) { testWebhooks(t, newStore(t)) })
	t.Run("WebhookDeliveries", func(t *testing.T) { testWebhookDeliveries(t, newStore(t)) })
	t.Run("CleanupPolicies", func(t *testing.T) { testCleanupPolicies(t, newStore(t)) })
	t.Run("CleanupRuns", func(t *testing.T) { testCleanupRuns(t, newStore(t)) })
	t.Run("Replications", func(t *testing.T) { testReplications(t, newStore(t)) })
	t.Run("Settings", func(t *testing.T) { testSettings(t, newStore(t)) })
	t.Run("Leases", func(t *testing.T) { testLeases(t, newStore(t)) })
	t.Run("Configs", func(t *testing.T) { testConfigs(t, newStore(t)) })
}

func project(key string) types.Project {
	return types.Project{
		Key: key, DisplayName: "Project " + key, Description: "the " + key + " project",
		AdminPrivileges:   types.AdminPrivileges{ManageMembers: true, CreateLocalRepository: true},
		StorageQuotaBytes: 1 << 30, SoftLimit: true,
	}
}

func createProjects(t *testing.T, s store.Store, keys ...string) {
	t.Helper()
	for _, k := range keys {
		must(t, s.Projects().Create(context.Background(), project(k)))
	}
}

func testProjects(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "charlie", "alpha", "bravo")
	wantErr(t, ps.Create(ctx, project("alpha")), store.ErrConflict)

	same(t, "Get", project("alpha"), get[types.Project](t)(ps.Get(ctx, "alpha")))
	_, err := ps.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	same(t, "List", []types.Project{project("alpha"), project("bravo"), project("charlie")},
		get[[]types.Project](t)(ps.List(ctx)))

	changed := project("alpha")
	changed.Description, changed.AdminPrivileges, changed.QuotaEmail = "renamed", types.AdminPrivileges{}, true
	must(t, ps.Update(ctx, changed))
	same(t, "Get after Update", changed, get[types.Project](t)(ps.Get(ctx, "alpha")))
	wantErr(t, ps.Update(ctx, project("nope")), store.ErrNotFound)

	// Deleting a project takes its roles, environments, members, owned
	// assignments and its shares of other projects' repositories with it.
	must(t, ps.PutRole(ctx, types.Role{ProjectKey: "alpha", Name: "dev", Type: types.RoleCustom}))
	must(t, ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "alpha", Name: "alpha-QA"}))
	must(
		t,
		ps.PutMember(
			ctx,
			types.ProjectMember{ProjectKey: "alpha", Type: types.PrincipalUser, Name: "ann"},
		),
	)
	must(
		t,
		ps.PutAssignment(
			ctx,
			types.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"},
		),
	)
	must(t, ps.PutAssignment(ctx, types.RepositoryAssignment{
		RepoKey: "bravo-libs", ProjectKey: "bravo", SharedWith: []string{"alpha", "charlie"},
	}))
	must(t, ps.Delete(ctx, "alpha"))
	wantErr(t, ps.Delete(ctx, "alpha"), store.ErrNotFound)
	_, err = ps.Get(ctx, "alpha")
	wantErr(t, err, store.ErrNotFound)
	if roles := get[[]types.Role](t)(ps.Roles(ctx, "alpha")); len(roles) != 0 {
		t.Fatalf("roles left: %v", roles)
	}
	if envs := get[[]types.Environment](t)(ps.Environments(ctx, "alpha")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
	_, err = ps.Member(ctx, "alpha", types.PrincipalUser, "ann")
	wantErr(t, err, store.ErrNotFound)
	_, err = ps.Assignment(ctx, "alpha-libs")
	wantErr(t, err, store.ErrNotFound)
	same(t, "shares after delete", []string{"charlie"},
		get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "bravo-libs")).SharedWith)
}

func testRoles(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	reader := types.Role{
		Name: "Reader", Type: types.RoleCustomGlobal, Description: "reads",
		Environments: []string{"DEV", "PROD"}, Actions: []string{"READ_REPOSITORY"},
	}
	admin := types.Role{Name: "Admin", Type: types.RoleAdmin, Actions: []string{"ADMIN"}}
	dev := types.Role{
		ProjectKey:   "p1",
		Name:         "Developer",
		Type:         types.RoleCustom,
		Environments: []string{"DEV"},
	}
	for _, r := range []types.Role{reader, admin, dev} {
		must(t, ps.PutRole(ctx, r))
	}
	wantErr(t, ps.PutRole(ctx, types.Role{ProjectKey: "nope", Name: "x"}), store.ErrNotFound)

	same(t, "Role", reader, get[types.Role](t)(ps.Role(ctx, "", "Reader")))
	same(t, "global Roles", []types.Role{admin, reader}, get[[]types.Role](t)(ps.Roles(ctx, "")))
	same(t, "project Roles", []types.Role{dev}, get[[]types.Role](t)(ps.Roles(ctx, "p1")))
	_, err := ps.Role(ctx, "p1", "Reader")
	wantErr(t, err, store.ErrNotFound)

	got := get[types.Role](t)(ps.Role(ctx, "", "Reader"))
	got.Environments[0] = "mutated"
	same(t, "Role is a copy", reader, get[types.Role](t)(ps.Role(ctx, "", "Reader")))

	reader.Description, reader.Environments = "reads everything", nil
	must(t, ps.PutRole(ctx, reader))
	same(t, "replaced Role", reader, get[types.Role](t)(ps.Role(ctx, "", "Reader")))

	must(t, ps.DeleteRole(ctx, "", "Reader"))
	wantErr(t, ps.DeleteRole(ctx, "", "Reader"), store.ErrNotFound)
	_, err = ps.Role(ctx, "", "Reader")
	wantErr(t, err, store.ErrNotFound)
}

func testEnvironments(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1")
	for _, e := range []types.Environment{
		{Name: "PROD"}, {Name: "DEV"}, {ProjectKey: "p1", Name: "p1-STAGE"},
	} {
		must(t, ps.CreateEnvironment(ctx, e))
	}
	// Names are unique across scopes.
	wantErr(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "p1", Name: "DEV"}),
		store.ErrConflict,
	)
	wantErr(
		t,
		ps.CreateEnvironment(ctx, types.Environment{ProjectKey: "nope", Name: "X"}),
		store.ErrNotFound,
	)
	same(t, "global", []types.Environment{{Name: "DEV"}, {Name: "PROD"}},
		get[[]types.Environment](t)(ps.Environments(ctx, "")))
	same(t, "project", []types.Environment{{ProjectKey: "p1", Name: "p1-STAGE"}},
		get[[]types.Environment](t)(ps.Environments(ctx, "p1")))

	role := types.Role{
		ProjectKey:   "p1",
		Name:         "qa",
		Type:         types.RoleCustom,
		Environments: []string{"DEV", "p1-STAGE"},
	}
	untouched := types.Role{
		Name:         "ops",
		Type:         types.RoleCustomGlobal,
		Environments: []string{"PROD"},
	}
	must(t, ps.PutRole(ctx, role))
	must(t, ps.PutRole(ctx, untouched))
	assigned := types.RepositoryAssignment{
		RepoKey:      "p1-libs",
		ProjectKey:   "p1",
		Environments: []string{"p1-STAGE"},
	}
	must(t, ps.PutAssignment(ctx, assigned))

	// Renaming to the same name changes nothing.
	must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-STAGE"))
	same(t, "role after no-op rename", role, get[types.Role](t)(ps.Role(ctx, "p1", "qa")))
	must(t, ps.RenameEnvironment(ctx, "p1", "p1-STAGE", "p1-QA"))
	same(t, "renamed", []types.Environment{{ProjectKey: "p1", Name: "p1-QA"}},
		get[[]types.Environment](t)(ps.Environments(ctx, "p1")))
	role.Environments = []string{"DEV", "p1-QA"}
	same(t, "role after rename", role, get[types.Role](t)(ps.Role(ctx, "p1", "qa")))
	same(t, "untouched role", untouched, get[types.Role](t)(ps.Role(ctx, "", "ops")))
	assigned.Environments = []string{"p1-QA"}
	same(t, "assignment after rename", assigned,
		get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "p1-libs")))

	wantErr(t, ps.RenameEnvironment(ctx, "p1", "p1-QA", "DEV"), store.ErrConflict)
	wantErr(t, ps.RenameEnvironment(ctx, "p1", "nope", "X"), store.ErrNotFound)
	wantErr(t, ps.RenameEnvironment(ctx, "", "p1-QA", "X"), store.ErrNotFound)

	wantErr(t, ps.DeleteEnvironment(ctx, "", "p1-QA"), store.ErrNotFound)
	must(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"))
	wantErr(t, ps.DeleteEnvironment(ctx, "p1", "p1-QA"), store.ErrNotFound)
	if envs := get[[]types.Environment](t)(ps.Environments(ctx, "p1")); len(envs) != 0 {
		t.Fatalf("environments left: %v", envs)
	}
}

func testProjectMembers(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2")
	bob := types.ProjectMember{
		ProjectKey: "p1",
		Type:       types.PrincipalUser,
		Name:       "bob",
		Roles:      []string{"Developer"},
	}
	ann := types.ProjectMember{
		ProjectKey: "p1",
		Type:       types.PrincipalUser,
		Name:       "ann",
		Roles:      []string{"Viewer", "QA"},
	}
	devs := types.ProjectMember{
		ProjectKey: "p1",
		Type:       types.PrincipalGroup,
		Name:       "devs",
		Roles:      []string{"Developer"},
	}
	other := types.ProjectMember{ProjectKey: "p2", Type: types.PrincipalUser, Name: "bob"}
	for _, m := range []types.ProjectMember{bob, ann, devs, other} {
		must(t, ps.PutMember(ctx, m))
	}
	wantErr(
		t,
		ps.PutMember(
			ctx,
			types.ProjectMember{ProjectKey: "nope", Type: types.PrincipalUser, Name: "x"},
		),
		store.ErrNotFound,
	)

	same(
		t,
		"Member",
		ann,
		get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)
	same(t, "users", []types.ProjectMember{ann, bob},
		get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalUser)))
	same(t, "groups", []types.ProjectMember{devs},
		get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalGroup)))
	_, err := ps.Member(ctx, "p1", types.PrincipalGroup, "bob")
	wantErr(t, err, store.ErrNotFound)

	got := get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann"))
	got.Roles[0] = "mutated"
	same(
		t,
		"Member is a copy",
		ann,
		get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)

	ann.Roles = []string{"Admin"}
	must(t, ps.PutMember(ctx, ann))
	same(
		t,
		"replaced",
		ann,
		get[types.ProjectMember](t)(ps.Member(ctx, "p1", types.PrincipalUser, "ann")),
	)

	must(t, ps.DeleteMember(ctx, "p1", types.PrincipalUser, "ann"))
	wantErr(t, ps.DeleteMember(ctx, "p1", types.PrincipalUser, "ann"), store.ErrNotFound)
	same(t, "after delete", []types.ProjectMember{bob},
		get[[]types.ProjectMember](t)(ps.Members(ctx, "p1", types.PrincipalUser)))
}

func testAssignments(t *testing.T, s store.Store) {
	ctx := context.Background()
	ps := s.Projects()
	createProjects(t, s, "p1", "p2", "p3")
	a := types.RepositoryAssignment{
		RepoKey: "repo-a", ProjectKey: "p1", Environments: []string{"DEV"},
		SharedWith: []string{"p3", "p2", "p3"}, Updated: at(1),
	}
	b := types.RepositoryAssignment{RepoKey: "repo-b", ProjectKey: "p2", Updated: at(2)}
	c := types.RepositoryAssignment{RepoKey: "repo-c", SharedWithAll: true, SharedReadOnly: true}
	d := types.RepositoryAssignment{
		RepoKey:        "repo-d",
		ProjectKey:     "p3",
		SharedWith:     []string{"p1"},
		SharedReadOnly: true,
	}
	for _, v := range []types.RepositoryAssignment{d, c, b, a} {
		must(t, ps.PutAssignment(ctx, v))
	}
	wantErr(
		t,
		ps.PutAssignment(ctx, types.RepositoryAssignment{RepoKey: "x", ProjectKey: "nope"}),
		store.ErrNotFound,
	)

	// Shares come back sorted and without duplicates.
	a.SharedWith = []string{"p2", "p3"}
	same(t, "Assignment", a, get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")))
	_, err := ps.Assignment(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	same(t, "Assignments p1", []types.RepositoryAssignment{a, c, d},
		get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p1")))
	same(t, "Assignments p2", []types.RepositoryAssignment{a, b, c},
		get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p2")))

	a.SharedWith, a.ProjectKey = nil, "p2"
	must(t, ps.PutAssignment(ctx, a))
	same(t, "replaced", a, get[types.RepositoryAssignment](t)(ps.Assignment(ctx, "repo-a")))
	same(t, "Assignments p3 after replace", []types.RepositoryAssignment{c, d},
		get[[]types.RepositoryAssignment](t)(ps.Assignments(ctx, "p3")))

	must(t, ps.DeleteAssignment(ctx, "repo-d"))
	wantErr(t, ps.DeleteAssignment(ctx, "repo-d"), store.ErrNotFound)
	_, err = ps.Assignment(ctx, "repo-d")
	wantErr(t, err, store.ErrNotFound)
}

func webhook(
	key string,
	domain types.WebhookDomain,
	project string,
	enabled bool,
) types.WebhookSubscription {
	return types.WebhookSubscription{
		Key: key, ProjectKey: project, Description: "hook " + key, Enabled: enabled, Domain: domain,
		EventTypes: []string{"deployed", "deleted"},
		Criteria: types.WebhookCriteria{
			AnyLocal: true, RepoKeys: []string{"libs"}, IncludePatterns: []string{"org/**"},
			SelectedReleaseBundles: map[string][]string{"app": {"1.0"}},
		},
		Handlers: []types.WebhookHandler{
			{
				Type:                types.HandlerWebhook,
				URL:                 "https://hooks.example/" + key,
				Secret:              "s3cret-" + key,
				UseSecretForSigning: true,
				Headers:             []types.Header{{Name: "X-Team", Value: "build"}},
			},
			{
				Type:    types.HandlerCustomWebhook,
				URL:     "https://custom.example/" + key,
				Method:  "POST",
				Payload: `{"name": "{{.data.name}}"}`,
				Secrets: []types.NamedSecret{
					{Name: "token", Value: "t-" + key},
					{Name: "user", Value: "u"},
				},
			},
		},
		Created: at(0), Updated: at(1),
	}
}

func testWebhooks(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Webhooks()
	w1 := webhook("w1", types.DomainArtifact, "", true)
	w2 := webhook("w2", types.DomainBuild, "p1", false)
	w3 := webhook("w3", types.DomainArtifact, "p1", true)
	for _, w := range []types.WebhookSubscription{w3, w1, w2} {
		must(t, ws.Create(ctx, w))
	}
	wantErr(t, ws.Create(ctx, w1), store.ErrConflict)

	same(t, "Get", w1, get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")))
	_, err := ws.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	list := func(f store.WebhookFilter) []types.WebhookSubscription {
		return get[[]types.WebhookSubscription](t)(ws.List(ctx, f))
	}
	same(t, "List", []types.WebhookSubscription{w1, w2, w3}, list(store.WebhookFilter{}))
	same(
		t,
		"by domain",
		[]types.WebhookSubscription{w1, w3},
		list(store.WebhookFilter{Domain: types.DomainArtifact}),
	)
	same(
		t,
		"by project",
		[]types.WebhookSubscription{w2, w3},
		list(store.WebhookFilter{ProjectKey: "p1"}),
	)
	same(
		t,
		"enabled",
		[]types.WebhookSubscription{w1, w3},
		list(store.WebhookFilter{EnabledOnly: true}),
	)

	got := get[types.WebhookSubscription](t)(ws.Get(ctx, "w1"))
	got.Handlers[0].Headers[0].Value = "mutated"
	got.Handlers[1].Secrets[0].Value = "mutated"
	same(t, "Get is a copy", w1, get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")))

	// Secrets are replaced with the subscription: a changed value, a
	// removed named secret and a cleared handler secret.
	w1.Handlers[0].Secret = ""
	w1.Handlers[1].Secrets = []types.NamedSecret{{Name: "token", Value: "rotated"}}
	w1.Enabled, w1.Updated = false, at(5)
	must(t, ws.Update(ctx, w1))
	same(t, "after Update", w1, get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")))
	wantErr(t, ws.Update(ctx, webhook("nope", types.DomainArtifact, "", true)), store.ErrNotFound)

	must(t, ws.Delete(ctx, "w1"))
	wantErr(t, ws.Delete(ctx, "w1"), store.ErrNotFound)
	_, err = ws.Get(ctx, "w1")
	wantErr(t, err, store.ErrNotFound)
	// Re-creating the key starts with no secrets from the deleted one.
	bare := types.WebhookSubscription{
		Key: "w1", Domain: types.DomainArtifact,
		Handlers: []types.WebhookHandler{{Type: types.HandlerWebhook, URL: "https://x"}},
	}
	must(t, ws.Create(ctx, bare))
	same(t, "re-created", bare, get[types.WebhookSubscription](t)(ws.Get(ctx, "w1")))
}

func testWebhookDeliveries(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Webhooks()
	must(t, ws.Create(ctx, types.WebhookSubscription{Key: "a", Domain: types.DomainArtifact}))
	must(t, ws.Create(ctx, types.WebhookSubscription{Key: "b", Domain: types.DomainArtifact}))
	delivery := func(key string, minute int, status int) types.WebhookDelivery {
		return types.WebhookDelivery{
			WebhookKey:  key,
			EventID:     key + "-event",
			EventType:   "deployed",
			AttemptedAt: at(minute),
			Status:      status,
			Elapsed:     1234567 * time.Microsecond,
			Request:     []byte(`{"url":"https://hooks.example"}`),
			Response:    []byte(`{"status":200}`),
		}
	}
	_, err := ws.AddDelivery(ctx, delivery("nope", 0, 200))
	wantErr(t, err, store.ErrNotFound)

	var added []types.WebhookDelivery
	var last int64
	for _, d := range []types.WebhookDelivery{
		delivery("a", 1, 200), delivery("a", 3, 500), delivery("b", 2, 200), delivery("a", 3, 200),
	} {
		got := get[types.WebhookDelivery](t)(ws.AddDelivery(ctx, d))
		if got.ID <= last {
			t.Fatalf("delivery ID %d after %d", got.ID, last)
		}
		last, d.ID = got.ID, got.ID
		same(t, "AddDelivery", d, got)
		added = append(added, d)
	}
	a1, a3, b2, a3b := added[0], added[1], added[2], added[3]
	if got := get[types.WebhookDelivery](
		t,
	)(
		ws.AddDelivery(ctx, delivery("b", 9, 0)),
	); got.Elapsed != 1234*time.Millisecond {
		t.Fatalf("elapsed = %v, want milliseconds", got.Elapsed)
	}
	deliveries := func(f store.DeliveryFilter) []types.WebhookDelivery {
		return get[[]types.WebhookDelivery](t)(ws.Deliveries(ctx, f))
	}
	b9 := delivery("b", 9, 0)
	b9.ID = last + 1
	all := deliveries(store.DeliveryFilter{})
	if len(all) != 5 {
		t.Fatalf("Deliveries = %d records", len(all))
	}
	b9.ID = all[0].ID
	same(t, "newest first", []types.WebhookDelivery{b9, a3b, a3, b2, a1}, all)
	same(
		t,
		"by webhook",
		[]types.WebhookDelivery{a3b, a3, a1},
		deliveries(store.DeliveryFilter{WebhookKey: "a"}),
	)
	same(t, "window", []types.WebhookDelivery{b2},
		deliveries(store.DeliveryFilter{From: at(2), To: at(3)}))
	same(
		t,
		"from",
		[]types.WebhookDelivery{b9, a3b, a3},
		deliveries(store.DeliveryFilter{From: at(3)}),
	)
	same(t, "limit", []types.WebhookDelivery{b9, a3b}, deliveries(store.DeliveryFilter{Limit: 2}))

	// A delivery without a time sorts after every timed one, falls outside
	// every window and is never pruned by age.
	untimed := delivery("b", 0, 0)
	untimed.AttemptedAt = time.Time{}
	untimed.ID = get[types.WebhookDelivery](t)(ws.AddDelivery(ctx, untimed)).ID
	same(t, "untimed last", []types.WebhookDelivery{b2, a1, untimed},
		deliveries(store.DeliveryFilter{})[3:])
	same(t, "untimed outside windows", []types.WebhookDelivery{b2},
		deliveries(store.DeliveryFilter{To: at(3), WebhookKey: "b"}))

	if n := get[int64](t)(ws.PruneDeliveries(ctx, at(3))); n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	same(
		t,
		"after prune",
		[]types.WebhookDelivery{b9, a3b, a3, untimed},
		deliveries(store.DeliveryFilter{}),
	)
	must(t, ws.Delete(ctx, "a"))
	same(
		t,
		"after webhook delete",
		[]types.WebhookDelivery{b9, untimed},
		deliveries(store.DeliveryFilter{}),
	)
}

func cleanupPolicy(key string, item types.CleanupItemType, project string) types.CleanupPolicy {
	return types.CleanupPolicy{
		Key:             key,
		Description:     "policy " + key,
		CronExp:         "0 0 2 * * ?",
		ProjectKey:      project,
		ItemType:        item,
		DurationMinutes: 60,
		Enabled:         true,
		SkipTrashcan:    true,
		Criteria: types.CleanupCriteria{
			PackageTypes: []string{
				"npm",
			},
			Repos:               []string{"npm-local"},
			IncludedPackages:    []string{"**"},
			CreatedBeforeInDays: 30,
			KeepLastNVersions:   3,
			IncludedProperties:  map[string][]string{"stage": {"dev", "test"}},
		},
		Created:   at(0),
		CreatedBy: "admin",
		Updated:   at(1),
		UpdatedBy: "ann",
	}
}

func testCleanupPolicies(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Cleanup()
	k1 := cleanupPolicy("k1", types.CleanupPackage, "p1")
	k2 := cleanupPolicy("k2", types.CleanupBuild, "")
	k3 := cleanupPolicy("k3", types.CleanupPackage, "p1")
	for _, p := range []types.CleanupPolicy{k3, k2, k1} {
		must(t, cs.CreatePolicy(ctx, p))
	}
	wantErr(t, cs.CreatePolicy(ctx, k1), store.ErrConflict)
	same(t, "Policy", k1, get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1")))
	_, err := cs.Policy(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	policies := func(f store.PolicyFilter) []types.CleanupPolicy {
		return get[[]types.CleanupPolicy](t)(cs.Policies(ctx, f))
	}
	same(t, "all", []types.CleanupPolicy{k1, k2, k3}, policies(store.PolicyFilter{}))
	same(
		t,
		"packages",
		[]types.CleanupPolicy{k1, k3},
		policies(store.PolicyFilter{ItemType: types.CleanupPackage}),
	)
	same(
		t,
		"builds",
		[]types.CleanupPolicy{k2},
		policies(store.PolicyFilter{ItemType: types.CleanupBuild}),
	)
	same(
		t,
		"project",
		[]types.CleanupPolicy{k1, k3},
		policies(store.PolicyFilter{ProjectKey: "p1"}),
	)

	got := get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1"))
	got.Criteria.IncludedProperties["stage"][0] = "mutated"
	same(t, "Policy is a copy", k1, get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1")))

	k1.Enabled, k1.Criteria, k1.Updated = false, types.CleanupCriteria{}, at(9)
	must(t, cs.UpdatePolicy(ctx, k1))
	same(t, "after Update", k1, get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1")))
	wantErr(
		t,
		cs.UpdatePolicy(ctx, cleanupPolicy("nope", types.CleanupPackage, "")),
		store.ErrNotFound,
	)

	must(t, cs.DeletePolicy(ctx, "k1"))
	wantErr(t, cs.DeletePolicy(ctx, "k1"), store.ErrNotFound)
	same(t, "after delete", []types.CleanupPolicy{k2, k3}, policies(store.PolicyFilter{}))
}

func testCleanupRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Cleanup()
	must(t, cs.CreatePolicy(ctx, cleanupPolicy("a", types.CleanupPackage, "")))
	statuses := []types.RunStatus{
		types.RunCompleted,
		types.RunFailed,
		types.RunInProgress,
		types.RunCompleted,
		types.RunStopped,
	}
	var runs []types.CleanupRun
	for i, st := range statuses {
		r := types.CleanupRun{
			PolicyKey:   "a",
			TriggerID:   int64(100 + i),
			ItemType:    types.CleanupPackage,
			Status:      st,
			DryRun:      i%2 == 0,
			Manual:      i == 1,
			TriggeredBy: "scheduler",
			Started: at(
				i,
			),
			TotalItems:   int64(10 * i),
			CleanedItems: int64(i),
			CleanedSize:  int64(1000 * i),
			Report:       []byte(`{"rows":[]}`),
		}
		if i == 4 {
			r.PolicyKey, r.ItemType = "b", types.CleanupBuild
		}
		if st != types.RunInProgress {
			r.Ended = at(i).Add(30 * time.Second)
		}
		got := get[types.CleanupRun](t)(cs.CreateRun(ctx, r))
		if i > 0 && got.ID <= runs[i-1].ID {
			t.Fatalf("run ID %d after %d", got.ID, runs[i-1].ID)
		}
		r.ID = got.ID
		same(t, "CreateRun", r, got)
		runs = append(runs, r)
	}
	same(t, "Run", runs[2], get[types.CleanupRun](t)(cs.Run(ctx, runs[2].ID)))
	_, err := cs.Run(ctx, runs[4].ID+100)
	wantErr(t, err, store.ErrNotFound)

	runs[2].Status, runs[2].Ended, runs[2].Report = types.RunCompletedWithErrors, at(20), nil
	must(t, cs.UpdateRun(ctx, runs[2]))
	same(t, "after UpdateRun", runs[2], get[types.CleanupRun](t)(cs.Run(ctx, runs[2].ID)))
	wantErr(t, cs.UpdateRun(ctx, types.CleanupRun{ID: runs[4].ID + 100}), store.ErrNotFound)

	// Deleting the policy keeps its runs for the run views.
	must(t, cs.DeletePolicy(ctx, "a"))
	list := func(f store.RunFilter) []types.CleanupRun {
		return pages(
			t,
			2,
			func(p store.Page) ([]types.CleanupRun, string, error) { return cs.Runs(ctx, f, p) },
		)
	}
	same(
		t,
		"newest first",
		[]types.CleanupRun{runs[4], runs[3], runs[2], runs[1], runs[0]},
		list(store.RunFilter{}),
	)
	same(
		t,
		"by policy",
		[]types.CleanupRun{runs[3], runs[2], runs[1], runs[0]},
		list(store.RunFilter{PolicyKey: "a"}),
	)
	same(
		t,
		"by item type",
		[]types.CleanupRun{runs[4]},
		list(store.RunFilter{ItemType: types.CleanupBuild}),
	)
	same(t, "by status", []types.CleanupRun{runs[3], runs[1], runs[0]},
		list(store.RunFilter{Statuses: []types.RunStatus{types.RunCompleted, types.RunFailed}}))

	_, _, err = cs.Runs(ctx, store.RunFilter{}, store.Page{After: store.Cursor("x")})
	wantErr(t, err, store.ErrInvalidCursor)

	same(t, "LastRun", runs[3], get[types.CleanupRun](t)(cs.LastRun(ctx, "a")))
	_, err = cs.LastRun(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
}

func replication(key, repoKey string) types.Replication {
	return types.Replication{
		RepoKey:              repoKey,
		Key:                  key,
		URL:                  "https://edge.example/artifactory/" + repoKey,
		Username:             "replicator",
		Password:             "pw-" + key,
		Enabled:              true,
		CronExp:              "0 0 * * * ?",
		EventReplication:     true,
		SyncDeletes:          true,
		SyncProperties:       true,
		IncludePathPrefix:    "org/",
		ExcludePathPrefix:    "org/tmp/",
		Proxy:                "corp",
		CheckBinaryExistence: true,
		SocketTimeout:        15*time.Second + 1234*time.Microsecond,
	}
}

func testReplications(t *testing.T, s store.Store) {
	ctx := context.Background()
	rs := s.Replications()
	wantErr(t, rs.Put(ctx, replication("", "repo-a")), store.ErrInvalid)
	k1, k2, k3 := replication(
		"k1",
		"repo-a",
	), replication(
		"k2",
		"repo-a",
	), replication(
		"k3",
		"repo-b",
	)
	for _, r := range []types.Replication{k3, k2, k1} {
		must(t, rs.Put(ctx, r))
	}
	got := get[types.Replication](t)(rs.Get(ctx, "k1"))
	same(t, "Get", k1, got)
	if got.SocketTimeout != 15001*time.Millisecond {
		t.Fatalf("socket timeout = %v, want whole milliseconds", got.SocketTimeout)
	}
	_, err := rs.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
	same(
		t,
		"ForRepo",
		[]types.Replication{k1, k2},
		get[[]types.Replication](t)(rs.ForRepo(ctx, "repo-a")),
	)
	same(t, "List", []types.Replication{k1, k2, k3}, get[[]types.Replication](t)(rs.List(ctx)))

	k1.Password, k1.Enabled = "rotated", false
	must(t, rs.Put(ctx, k1))
	same(t, "replaced", k1, get[types.Replication](t)(rs.Get(ctx, "k1")))
	k2.Password = ""
	must(t, rs.Put(ctx, k2))
	same(t, "no password", k2, get[types.Replication](t)(rs.Get(ctx, "k2")))

	wantErr(
		t,
		rs.PutRun(ctx, types.ReplicationRun{ReplicationKey: "nope", Started: at(1)}),
		store.ErrNotFound,
	)
	running := types.ReplicationRun{
		ReplicationKey: "k1",
		Started:        at(1),
		Status:         types.ReplicationIncomplete,
	}
	must(t, rs.PutRun(ctx, running))
	same(t, "LastRun running", running, get[types.ReplicationRun](t)(rs.LastRun(ctx, "k1")))
	done := types.ReplicationRun{
		ReplicationKey: "k1",
		Started:        at(1),
		Completed:      at(2),
		Status:         types.ReplicationOK,
	}
	must(t, rs.PutRun(ctx, done))
	same(t, "LastRun replaced", done, get[types.ReplicationRun](t)(rs.LastRun(ctx, "k1")))
	later := types.ReplicationRun{
		ReplicationKey: "k1",
		Started:        at(5),
		Status:         types.ReplicationError,
	}
	must(t, rs.PutRun(ctx, later))
	must(
		t,
		rs.PutRun(
			ctx,
			types.ReplicationRun{ReplicationKey: "k2", Started: at(3), Status: types.ReplicationOK},
		),
	)
	same(t, "LastRun newest", later, get[types.ReplicationRun](t)(rs.LastRun(ctx, "k1")))
	_, err = rs.LastRun(ctx, "k3")
	wantErr(t, err, store.ErrNotFound)

	must(t, rs.Delete(ctx, "k1"))
	wantErr(t, rs.Delete(ctx, "k1"), store.ErrNotFound)
	_, err = rs.LastRun(ctx, "k1")
	wantErr(t, err, store.ErrNotFound)
	must(t, rs.DeleteForRepo(ctx, "repo-a"))
	must(t, rs.DeleteForRepo(ctx, "repo-none"))
	_, err = rs.LastRun(ctx, "k2")
	wantErr(t, err, store.ErrNotFound)
	same(t, "after deletes", []types.Replication{k3}, get[[]types.Replication](t)(rs.List(ctx)))
}

func testSettings(t *testing.T, s store.Store) {
	ctx := context.Background()
	ss := s.Settings()
	var lock types.UserLockPolicy
	wantErr(t, ss.Get(ctx, types.SettingUserLockPolicy, &lock), store.ErrNotFound)

	must(
		t,
		ss.Put(
			ctx,
			types.SettingUserLockPolicy,
			types.UserLockPolicy{Enabled: true, LoginAttempts: 5},
		),
	)
	must(
		t,
		ss.Put(
			ctx,
			types.SettingTrashcan,
			types.TrashcanSettings{Enabled: true, RetentionDays: 14},
		),
	)
	must(t, ss.Put(ctx, types.SettingBasicAuthEnabled, true))
	must(t, ss.Get(ctx, types.SettingUserLockPolicy, &lock))
	same(t, "Get", types.UserLockPolicy{Enabled: true, LoginAttempts: 5}, lock)

	must(t, ss.Put(ctx, types.SettingTrashcan, types.TrashcanSettings{RetentionDays: 7}))
	var trash types.TrashcanSettings
	must(t, ss.Get(ctx, types.SettingTrashcan, &trash))
	same(t, "replaced", types.TrashcanSettings{RetentionDays: 7}, trash)

	if err := ss.Put(ctx, "bad", make(chan int)); err == nil {
		t.Fatal("Put of a value JSON cannot encode succeeded")
	}
	var n int
	if err := ss.Get(ctx, types.SettingTrashcan, &n); err == nil {
		t.Fatal("Get into a mismatched type succeeded")
	}
	same(
		t,
		"Keys",
		[]string{types.SettingBasicAuthEnabled, types.SettingUserLockPolicy, types.SettingTrashcan},
		get[[]string](t)(ss.Keys(ctx)),
	)

	must(t, ss.Delete(ctx, types.SettingTrashcan))
	wantErr(t, ss.Delete(ctx, types.SettingTrashcan), store.ErrNotFound)
	wantErr(t, ss.Get(ctx, types.SettingTrashcan, &trash), store.ErrNotFound)
	same(
		t,
		"Keys after delete",
		[]string{types.SettingBasicAuthEnabled, types.SettingUserLockPolicy},
		get[[]string](t)(ss.Keys(ctx)),
	)
}

func testLeases(t *testing.T, s store.Store) {
	ctx := context.Background()
	ls := s.Leases()
	acquire := func(l types.Lease, now time.Time) bool {
		t.Helper()
		return get[bool](t)(ls.Acquire(ctx, l, now))
	}
	_, err := ls.Acquire(ctx, types.Lease{Name: "gc", Expires: at(10)}, at(0))
	wantErr(t, err, store.ErrInvalid)
	_, err = ls.Acquire(ctx, types.Lease{Name: "gc", Holder: "n1"}, at(0))
	wantErr(t, err, store.ErrInvalid)

	n1 := types.Lease{
		Name:     "gc",
		Holder:   "n1",
		Task:     "garbage collection",
		Acquired: at(0),
		Expires:  at(10),
	}
	if !acquire(n1, at(0)) {
		t.Fatal("a free lease was not taken")
	}
	if acquire(types.Lease{Name: "gc", Holder: "n2", Acquired: at(5), Expires: at(15)}, at(5)) {
		t.Fatal("a held lease was taken by another holder")
	}
	renewed := n1
	renewed.Expires = at(20)
	if !acquire(renewed, at(5)) {
		t.Fatal("the holder could not renew")
	}
	other := types.Lease{Name: "cleanup", Holder: "n3", Acquired: at(1), Expires: at(30)}
	if !acquire(other, at(1)) {
		t.Fatal("a second lease was not taken")
	}
	same(t, "List", []types.Lease{other, renewed}, get[[]types.Lease](t)(ls.List(ctx)))

	// A lease is free once now reaches its expiry.
	n2 := types.Lease{Name: "gc", Holder: "n2", Acquired: at(20), Expires: at(40)}
	if !acquire(n2, at(20)) {
		t.Fatal("an expired lease was not taken over")
	}
	must(t, ls.Release(ctx, "gc", "n1"))
	same(
		t,
		"release by a non-holder",
		[]types.Lease{other, n2},
		get[[]types.Lease](t)(ls.List(ctx)),
	)
	must(t, ls.Release(ctx, "gc", "n2"))
	must(t, ls.Release(ctx, "gc", "n2"))
	same(t, "after release", []types.Lease{other}, get[[]types.Lease](t)(ls.List(ctx)))
}

func testConfigs(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Configs()
	wantErr(t, cs.Put(ctx, types.ConfigDocument{Key: "x"}), store.ErrInvalid)
	wantErr(t, cs.Put(ctx, types.ConfigDocument{Kind: types.ConfigOIDC}), store.ErrInvalid)

	github := types.ConfigDocument{
		Kind:    types.ConfigOIDC,
		Key:     "github",
		Enabled: true,
		Document: []byte(
			`{"issuer_url":"https://token.actions.githubusercontent.com","provider_type":"GitHub"}`,
		),
		Secrets: map[string]string{"client_secret": "cs", "unset": ""},
		Updated: at(1),
	}
	azure := types.ConfigDocument{
		Kind:     types.ConfigOIDC,
		Key:      "azure",
		Document: []byte(`{}`),
		Updated:  at(2),
	}
	corp := types.ConfigDocument{
		Kind:     types.ConfigLDAP,
		Key:      "corp",
		Enabled:  true,
		Document: []byte(`{"ldap_url":"ldaps://corp"}`),
		Secrets:  map[string]string{"manager_password": "mp"},
	}
	for _, d := range []types.ConfigDocument{github, azure, corp} {
		must(t, cs.Put(ctx, d))
	}
	// An empty secret is not stored.
	github.Secrets = map[string]string{"client_secret": "cs"}
	same(t, "Get", github, get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")))
	_, err := cs.Get(ctx, types.ConfigLDAP, "github")
	wantErr(t, err, store.ErrNotFound)

	same(t, "List oidc", []types.ConfigDocument{azure, github},
		get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigOIDC)))
	same(
		t,
		"List ldap",
		[]types.ConfigDocument{corp},
		get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigLDAP)),
	)
	if got := get[[]types.ConfigDocument](t)(cs.List(ctx, types.ConfigSAML)); len(got) != 0 {
		t.Fatalf("List saml = %v", got)
	}

	got := get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github"))
	got.Secrets["client_secret"] = "mutated"
	got.Document[0] = '['
	same(
		t,
		"Get is a copy",
		github,
		get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)

	github.Secrets, github.Enabled = nil, false
	must(t, cs.Put(ctx, github))
	same(
		t,
		"replaced",
		github,
		get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigOIDC, "github")),
	)

	must(t, cs.Delete(ctx, types.ConfigLDAP, "corp"))
	wantErr(t, cs.Delete(ctx, types.ConfigLDAP, "corp"), store.ErrNotFound)
	_, err = cs.Get(ctx, types.ConfigLDAP, "corp")
	wantErr(t, err, store.ErrNotFound)
	// Re-creating the key starts without the deleted document's secrets.
	must(t, cs.Put(ctx, types.ConfigDocument{Kind: types.ConfigLDAP, Key: "corp"}))
	same(t, "re-created", types.ConfigDocument{Kind: types.ConfigLDAP, Key: "corp"},
		get[types.ConfigDocument](t)(cs.Get(ctx, types.ConfigLDAP, "corp")))
}
