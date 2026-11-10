package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	npmfmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// reindexRecorder is the format of the P8 package types in these tests:
// the generic format made a PathReindexer that records each rebuild, as
// "<key>" or "<key>/<path>", and fails it with err.
type reindexRecorder struct {
	generic.Handler
	mu   sync.Mutex
	runs []string
	err  error
}

func (f *reindexRecorder) Reindex(_ context.Context, _ format.Env, c repo.Config) error {
	return f.record(c.Key)
}

func (f *reindexRecorder) ReindexPath(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	p string,
) error {
	return f.record(c.Key + "/" + p)
}

func (f *reindexRecorder) record(run string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return f.err
}

// take returns the rebuilds run since it was last called.
func (f *reindexRecorder) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.runs
	f.runs = nil
	return out
}

// p8Repos are the P8 repositories of reindexWorld.
var p8Repos = []repo.Config{
	{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI},
	{Key: "pypi-remote", Class: repo.Remote, PackageType: repo.PyPI, URL: "https://up.example"},
	{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
	{Key: "nuget-fed", Class: repo.Federated, PackageType: repo.NuGet},
	{Key: "nuget-remote", Class: repo.Remote, PackageType: repo.NuGet, URL: "https://up.example"},
	{
		Key: "nuget-virtual", Class: repo.Virtual, PackageType: repo.NuGet,
		Repositories: []string{"nuget-local"},
	},
	{Key: "helm-local", Class: repo.Local, PackageType: repo.Helm},
	{Key: "helm-fed", Class: repo.Federated, PackageType: repo.Helm},
	{
		Key: "helm-virtual", Class: repo.Virtual, PackageType: repo.Helm,
		Repositories: []string{"helm-local"},
	},
	{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI},
	{
		Key: "terraform-local", Class: repo.Local, PackageType: repo.Terraform,
		TerraformType: repo.TerraformModule,
	},
}

// reindexWorld is an item world with the P8 repositories, deployer holding
// Manage on each and nothing else, and formats from pick. With a queue, the
// work it is given runs only when a test asks.
func reindexWorld(
	t *testing.T,
	pick func(repo.PackageType) format.Handler,
	queue bool,
) (*itemWorld, *work.Queue) {
	t.Helper()
	var q *work.Queue
	w := newItemWorld(t, false, func(cfg *Config) {
		cfg.Formats = pick
		if !queue {
			return
		}
		var err error
		if q, err = work.New(cfg.Store, work.Options{}); err != nil {
			t.Fatal(err)
		}
		api := withAccess(*cfg).Access
		derived := &item.Service{
			Store:            cfg.Store,
			Blobs:            cfg.Blobs,
			Work:             q,
			Formats:          pick,
			ReindexAuthorize: reindexauth.New(api.Authenticator, api.Authorizer),
		}
		if err := derived.RegisterWork(q); err != nil {
			t.Fatal(err)
		}
		cfg.Work = q
	})
	ctx := context.Background()
	targets := map[string]types.Target{}
	for _, c := range p8Repos {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		targets[c.Key] = types.Target{Include: []string{"**"}}
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "manage-p8",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionManage}},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return w, q
}

// recording picks f for the P8 package types and the real formats for the
// rest.
func recording(f *reindexRecorder) func(repo.PackageType) format.Handler {
	return func(t repo.PackageType) format.Handler {
		if slices.Contains(p8Types, t) {
			return f
		}
		return ecosystem.For(t)
	}
}

// Each operation refuses as 7.x does: a missing repository 404, a caller
// without the right 401 or 403, a repository of another type, or of a
// class 7.x does not reindex, 400. NuGet virtual reindex remains P10. Each
// answers in its path's envelope.
func TestAccReindexRefusals(t *testing.T) {
	w, _ := reindexWorld(t, recording(&reindexRecorder{}), true)
	for _, c := range []struct {
		path, as string
		expected int
	}{
		{"pypi/pypi-local/reindex", "user", http.StatusForbidden},
		{"pypi/pypi-local/reindex", "", http.StatusUnauthorized},
		{"pypi/missing/reindex", "admin", http.StatusNotFound},
		{"pypi/nuget-local/reindex", "admin", http.StatusBadRequest},
		{"pypi/pypi-remote/reindex", "admin", http.StatusBadRequest},
		{"pypi/pypi-remote-cache/reindex", "admin", http.StatusBadRequest},
		{"nuget/nuget-local/reindex", "user", http.StatusForbidden},
		{"nuget/nuget-remote/reindex", "user", http.StatusForbidden},
		{"nuget/nuget-remote-cache/reindex", "reader", http.StatusForbidden},
		{"nuget/nuget-virtual/reindex", "admin", http.StatusNotImplemented},
		{"nuget/nuget-virtual/reindex", "user", http.StatusForbidden},
		{"nuget/helm-local/reindex", "admin", http.StatusBadRequest},
		{"nuget/libs-local/reindex", "deployer", http.StatusForbidden},
		{"helm/helm-virtual/reindex", "admin", http.StatusBadRequest},
		{"helm/helmoci-local/reindex", "admin", http.StatusBadRequest},
		{"helm/missing/reindex", "admin", http.StatusNotFound},
		{"terraform/terraform-local/reindex", "deployer", http.StatusForbidden},
		{"terraform/terraform-local/reindex", "", http.StatusUnauthorized},
		{"terraform/helm-local/reindex", "admin", http.StatusBadRequest},
	} {
		path := "/artifactory/api/" + c.path
		rec := w.do("POST", path, c.as, "")
		if rec.Code != c.expected {
			t.Errorf("%s as %q: %d, want %d: %s", c.path, c.as, rec.Code, c.expected, rec.Body)
			continue
		}
		var body string
		switch s := errorProfileFor(path); s {
		case apierror.ProfileText:
			body = "\n"
		case apierror.ProfileTerraformRegistry:
			body = `{"errors":["`
		default:
			body = `{"errors":[{`
		}
		if !strings.Contains(rec.Body.String(), body) {
			t.Errorf("%s as %q: envelope %s", c.path, c.as, rec.Body)
		}
	}
	rec := w.do("POST", "/artifactory/api/nuget/nuget-virtual/reindex", "admin", "")
	if !strings.Contains(rec.Body.String(),
		"'nuget-virtual' is a virtual repository, which artifactweave does not reindex yet") {
		t.Errorf("later: %s", rec.Body)
	}
	rec = w.do("POST", "/artifactory/api/helm/helm-virtual/reindex", "admin", "")
	if !strings.Contains(
		rec.Body.String(),
		"Repository 'helm-virtual' is not a local helm repository",
	) {
		t.Errorf("refused: %s", rec.Body)
	}
}

// reindexFaults fails one store call a JFrog operation makes: "settings"
// the anonymous-access read behind a caller's rights, "enqueue" queueing
// work, and "get" reading it.
type reindexFaults struct {
	*inmem.Store
	fail string
}

func (s reindexFaults) Settings() store.SettingsStore {
	if s.fail == "settings" {
		return settingsDown{s.Store.Settings()}
	}
	return s.Store.Settings()
}

func (s reindexFaults) WorkQueue() store.WorkQueue {
	return queueDown{s.Store.WorkQueue(), s.fail}
}

type settingsDown struct{ store.SettingsStore }

func (settingsDown) Get(context.Context, string, any) error { return errBoom }

type queueDown struct {
	store.WorkQueue
	fail string
}

func (q queueDown) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	now time.Time,
) error {
	if q.fail == "enqueue" {
		return errBoom
	}
	return q.WorkQueue.Enqueue(ctx, kind, key, payload, now) //nolint:wrapcheck // test double
}

func (q queueDown) Get(ctx context.Context, kind, key string) (types.WorkItem, error) {
	if q.fail == "get" {
		return types.WorkItem{}, errBoom
	}
	return q.WorkQueue.Get(ctx, kind, key) //nolint:wrapcheck // test double
}

// A JFrog operation without a store answers 501, as the stub does; without
// a caller, 401; and a store that fails under it, 500.
func TestAccReindexFaults(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	on := func(st store.Store) artifactoryService {
		cfg := Config{Formats: recording(f)}
		if st != nil {
			cfg.Store = st
		}
		return newArtifactoryService(withAccess(cfg))
	}
	status := func(err error) int {
		var e *apierror.Error
		if !errors.As(err, &e) {
			return 0
		}
		return e.Status
	}
	if _, err := on(nil).Reindex(ctx, repo.NuGet, "nuget-local", "", false); status(err) !=
		http.StatusNotImplemented {
		t.Errorf("no store: %v", err)
	}
	if _, err := on(db).Reindex(ctx, repo.NuGet, "nuget-local", "", false); status(err) !=
		http.StatusUnauthorized {
		t.Errorf("no caller: %v", err)
	}
	admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
	for _, c := range []struct {
		fail string
		call func(artifactoryService) error
	}{
		{"settings", func(s artifactoryService) error {
			_, err := s.Reindex(kernel.WithPrincipal(ctx, kernel.Principal{Name: "deployer"}),
				repo.NuGet, "nuget-local", "", false)
			return err
		}},
		{"enqueue", func(s artifactoryService) error {
			_, err := s.Reindex(admin, repo.NuGet, "nuget-local", "", false)
			return err
		}},
		{"enqueue", func(s artifactoryService) error {
			_, err := s.MigrateNpmRepositoryToStrictLayout(admin,
				artgen.MigrateNpmRepositoryToStrictLayoutRequestObject{RepoKey: "npm-local"})
			return err
		}},
		{"get", func(s artifactoryService) error {
			_, err := s.ResumeNpmRepositoryMigration(admin,
				artgen.ResumeNpmRepositoryMigrationRequestObject{RepoKey: "npm-local"})
			return err
		}},
	} {
		if err := c.call(on(reindexFaults{db, c.fail})); status(err) !=
			http.StatusInternalServerError {
			t.Errorf("%s: %v", c.fail, err)
		}
	}
	// A parked conversion whose resume cannot be queued.
	park(t, db, npmfmt.KindConvert, "npm-local")
	if _, err := on(reindexFaults{db, "enqueue"}).ResumeNpmRepositoryMigration(admin,
		artgen.ResumeNpmRepositoryMigrationRequestObject{RepoKey: "npm-local"}); status(err) !=
		http.StatusInternalServerError {
		t.Errorf("resume: %v", err)
	}
}
