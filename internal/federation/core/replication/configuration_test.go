package replication_test

import (
	"context"
	"errors"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func ptr[T any](v T) *T { return &v }

type authority struct {
	any, platform bool
	project       string
}

func (p authority) Any() bool      { return p.any }
func (p authority) Platform() bool { return p.platform }
func (p authority) Allows(project string, _ repo.Class) bool {
	return p.platform || p.project == project
}

type repositories struct {
	values                     map[string]repo.Config
	metadataReads, secretReads int
	metadataError, secretError error
}

func (r *repositories) ProjectRepository(_ context.Context, key string) (artifactsapi.ProjectRepository, error) {
	r.metadataReads++
	if r.metadataError != nil {
		return artifactsapi.ProjectRepository{}, r.metadataError
	}
	c, ok := r.values[key]
	if !ok {
		return artifactsapi.ProjectRepository{}, kernel.ErrNotFound
	}
	return artifactsapi.ProjectRepository{Key: c.Key, Class: c.Class, ProjectKey: c.ProjectKey}, nil
}

func (r *repositories) Get(_ context.Context, key string) (repo.Config, error) {
	r.secretReads++
	if r.secretError != nil {
		return repo.Config{}, r.secretError
	}
	c, ok := r.values[key]
	if !ok {
		return repo.Config{}, kernel.ErrNotFound
	}
	return c, nil
}

type world struct {
	service replication.Service
	db      *fedinmem.Store
	repos   *repositories
	policy  authority
}

func newWorld() *world {
	shared := memtx.New()
	w := &world{db: fedinmem.New(shared), policy: authority{any: true, platform: true}}
	w.repos = &repositories{values: map[string]repo.Config{
		"local":   {Key: "local", Class: repo.Local, ProjectKey: "alpha"},
		"remote":  {Key: "remote", Class: repo.Remote, ProjectKey: "beta", URL: "https://upstream.example/artifactory/source"},
		"virtual": {Key: "virtual", Class: repo.Virtual},
	}}
	w.service = replication.Service{
		Store: w.db, Ownership: w.repos, Repositories: w.repos, Settings: platforminmem.New(shared).Settings(),
		Authorize: func(context.Context) (replication.Policy, error) { return w.policy, nil },
		Guard:     func(context.Context) error { return nil },
		ProxyExists: func(_ context.Context, key string) error {
			if key == "corp" {
				return nil
			}
			return kernel.ErrNotFound
		},
	}
	return w
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func stored(t *testing.T, w *world, repository string) model.Replication {
	t.Helper()
	rows, err := w.db.Replications().ForRepo(t.Context(), repository)
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("stored targets = %d, want one", len(rows))
	}
	return rows[0]
}

func TestUnitConfigurationCredentialLifecycle(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	in := replication.Input{
		URL:         ptr("https://target.example/artifactory/destination"),
		Username:    ptr("replicator"),
		Password:    ptr("fake-target-password"),
		Enabled:     ptr(false),
		SyncDeletes: ptr(true),
	}
	must(t, w.service.Replace(ctx, "local", in))
	first := stored(t, w, "local")
	if first.Key == "" || first.Revision == "" || first.Enabled || !first.SyncProperties || first.SocketTimeout != 15*time.Second {
		t.Fatal("create lost identity or native defaults")
	}
	public, err := w.service.Get(ctx, "local")
	must(t, err)
	if public.Password != crypt.Masked || public.Key != first.Key || public.RepoKey != "local" {
		t.Fatal("public view lost server identity or exposed an unmasked password")
	}
	must(t, w.service.Update(ctx, "local", replication.Input{Enabled: ptr(true)}))
	updated := stored(t, w, "local")
	if updated.Key != first.Key || updated.Password != first.Password || !updated.SyncDeletes || updated.Revision == first.Revision {
		t.Fatal("partial update lost omitted credentials/options or worker fence")
	}
	must(t, w.service.Update(ctx, "local", replication.Input{Enabled: ptr(true)}))
	if stored(t, w, "local") != updated {
		t.Fatal("identical partial retry invalidated current worker authority")
	}
	must(t, w.service.Replace(ctx, "local", replication.Input{URL: in.URL}))
	replaced := stored(t, w, "local")
	if replaced.Key != first.Key || replaced.Password != first.Password || replaced.SyncDeletes || replaced.Username != "" {
		t.Fatal("replacement lost stable identity/omitted password or acted as a patch")
	}
	must(t, w.service.Update(ctx, "local", replication.Input{Password: ptr(crypt.Masked)}))
	if stored(t, w, "local") != replaced {
		t.Fatal("masked round trip overwrote the credential or rotated its revision")
	}
	must(t, w.service.Update(ctx, "local", replication.Input{Password: ptr("rotated-fake-password")}))
	rotated := stored(t, w, "local")
	if rotated.Password == first.Password || rotated.Key != first.Key || rotated.Revision == replaced.Revision {
		t.Fatal("rotation did not retain identity and fence the previous credential")
	}
	must(t, w.service.Update(ctx, "local", replication.Input{Password: ptr("")}))
	public, err = w.service.Get(ctx, "local")
	must(t, err)
	if public.Password != "" {
		t.Fatal("explicitly cleared credential still reported configured")
	}
}

func TestUnitConfigurationDeleteRecreateFencesReusedIdentity(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	in := replication.Input{URL: ptr("https://target.example/artifactory/destination")}
	must(t, w.service.Replace(ctx, "local", in))
	first := stored(t, w, "local")
	called := false
	admit := func(context.Context, model.Replication) error { called = true; return nil }
	must(t, w.service.WithTarget(ctx, first.Key, first.Revision, admit))
	if !called {
		t.Fatal("current target was not admitted")
	}
	if err := w.service.Delete(ctx, "local", "https://other.example/repo"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("wrong URL filter: %v", err)
	}
	if stored(t, w, "local") != first {
		t.Fatal("a different URL deleted or changed the target")
	}
	must(t, w.service.Delete(ctx, "local", ""))
	if _, err := w.service.Get(ctx, "local"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("deleted target: %v", err)
	}
	must(t, w.service.Replace(ctx, "local", in))
	fresh := stored(t, w, "local")
	if fresh.Key == first.Key || fresh.Revision == first.Revision {
		t.Fatal("recreation reused old authority")
	}
	// Even an import that deliberately reuses repo, public key and URL cannot
	// revive the previous worker's private revision.
	must(t, w.db.Replications().Delete(ctx, fresh.Key))
	fresh.Key = first.Key
	must(t, w.db.Replications().Put(ctx, fresh))
	called = false
	if err := w.service.WithTarget(ctx, first.Key, first.Revision, admit); !errors.Is(err, kernel.ErrConflict) || called {
		t.Fatal("old worker authority revived after identity reuse")
	}
	must(t, w.service.WithTarget(ctx, fresh.Key, fresh.Revision, admit))
	if !called {
		t.Fatal("fresh imported authority refused")
	}
}

func TestUnitConfigurationValidatesBeforePublication(t *testing.T) {
	cases := []struct {
		name, repository string
		input            replication.Input
	}{
		{"missing repository", "missing", replication.Input{}},
		{"virtual", "virtual", replication.Input{}},
		{"missing push URL", "local", replication.Input{}},
		{"URL credentials", "local", replication.Input{URL: ptr("https://fake-user:fake-password@target.example/repo")}},
		{"URL parse failure", "local", replication.Input{URL: ptr("https://target.example/%invalid-fake-password")}},
		{"URL query", "local", replication.Input{URL: ptr("https://target.example/repo?password=fake-password")}},
		{"event replication", "local", replication.Input{URL: ptr("https://target.example/repo"), EventReplication: ptr(true)}},
		{"checksum replication", "local", replication.Input{URL: ptr("https://target.example/repo"), CheckBinaryExistence: ptr(true)}},
		{"non Quartz", "local", replication.Input{URL: ptr("https://target.example/repo"), CronExp: ptr("0 2 * * *")}},
		{"zero timeout", "local", replication.Input{URL: ptr("https://target.example/repo"), SocketTimeoutMillis: ptr(int64(0))}},
		{"overflow timeout", "local", replication.Input{URL: ptr("https://target.example/repo"), SocketTimeoutMillis: ptr(int64(1) << 40)}},
		{"missing proxy", "local", replication.Input{URL: ptr("https://target.example/repo"), Proxy: ptr("missing")}},
		{"foreign pull URL", "remote", replication.Input{URL: ptr("https://other.example/repo")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			err := w.service.Replace(t.Context(), tc.repository, tc.input)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			rows, readErr := w.db.Replications().List(t.Context())
			must(t, readErr)
			if len(rows) != 0 {
				t.Fatal("invalid input published a target")
			}
		})
	}
	w := newWorld()
	must(t, w.service.Replace(t.Context(), "remote", replication.Input{Proxy: ptr("corp"), CronExp: ptr("0 0 2 * * ?")}))
	pull := stored(t, w, "remote")
	if pull.URL != w.repos.values["remote"].URL || pull.Proxy != "corp" {
		t.Fatal("pull did not select the canonical source")
	}
}

func TestUnitConfigurationCurrentAuthorityBeforeDisclosure(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	must(t, w.service.Replace(ctx, "remote", replication.Input{Password: ptr("fake-secret")}))
	w.policy = authority{any: true, project: "alpha"}
	beforeMetadata, beforeSecrets := w.repos.metadataReads, w.repos.secretReads
	if _, err := w.service.Get(ctx, "remote"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("foreign read = %v", err)
	}
	if err := w.service.Update(ctx, "remote", replication.Input{Password: ptr("replacement")}); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("foreign update = %v", err)
	}
	if w.repos.secretReads != beforeSecrets || w.repos.metadataReads <= beforeMetadata {
		t.Fatal("authorization opened remote configuration before ownership checks")
	}
	w.policy = authority{}
	beforeMetadata = w.repos.metadataReads
	if _, err := w.service.Get(ctx, "missing"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("revoked authority = %v", err)
	}
	if w.repos.metadataReads != beforeMetadata {
		t.Fatal("revoked administrator looked up repository existence")
	}
	if stored(t, w, "remote").Password != "fake-secret" {
		t.Fatal("denied update changed credentials")
	}
}

var errInjected = errors.New("injected replication write failure")

type failedStore struct{ port.Store }

func (s failedStore) Replications() port.ReplicationStore {
	return failedTargets{s.Store.Replications()}
}

type failedTargets struct{ port.ReplicationStore }

func (s failedTargets) Put(ctx context.Context, r model.Replication) error {
	if err := s.ReplicationStore.Put(ctx, r); err != nil {
		return err
	}
	return errInjected
}

func TestUnitConfigurationFailedRotationRollsBackSecretAndRevision(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	must(t, w.service.Replace(ctx, "local", replication.Input{URL: ptr("https://target.example/repo"), Password: ptr("original-fake-password")}))
	before := stored(t, w, "local")
	w.service.Store = failedStore{w.db}
	err := w.service.Update(ctx, "local", replication.Input{Password: ptr("new-fake-password"), Enabled: ptr(false)})
	if !errors.Is(err, errInjected) {
		t.Fatalf("rotation error = %v", err)
	}
	if stored(t, w, "local") != before {
		t.Fatal("failed rotation partially published a credential, enablement or revision")
	}
}
