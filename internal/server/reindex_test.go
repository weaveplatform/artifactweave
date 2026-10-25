package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
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

// claimsReindex is a format that claims a reindex kind as its own work,
// which the item service refuses to register.
type claimsReindex struct{ generic.Handler }

func (claimsReindex) WorkKinds() []string { return []string{format.KindReindex} }

func (claimsReindex) Work(context.Context, format.Env, types.WorkItem) error { return nil }

func (claimsReindex) Owed(repo.Config, bool, string) []format.WorkRef { return nil }

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
		derived := &item.Service{
			Store:            cfg.Store,
			Blobs:            cfg.Blobs,
			Work:             q,
			Formats:          pick,
			ReindexAuthorize: reindexauth.New(cfg.Store, cfg.Auth),
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
		return formats.For(t)
	}
}

// wantText checks a reindex's 200 and its plain-text answer.
func wantText(t *testing.T, rec *httptest.ResponseRecorder, text string) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Body.String() != text ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("%d %q %q, want %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"), text)
	}
}

// NuGet's, Helm's, Terraform's and an asynchronous PyPI reindex are
// queued under the repository's key for their caller, who needs Manage on
// it, or for Terraform's to be an administrator, and answer 7.x's text at
// once; the format rebuilds the index when the queue runs it.
func TestAccReindexIsQueued(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	w, q := reindexWorld(t, recording(f), true)
	for _, c := range []struct{ path, as, key, text string }{
		{
			"/artifactory/api/nuget/nuget-local/reindex", "deployer", "nuget-local",
			"NuGet reindex calculation for repository 'nuget-local' accepted.",
		},
		{
			"/artifactory/api/nuget/nuget-fed/reindex", "deployer", "nuget-fed",
			"NuGet reindex calculation for repository 'nuget-fed' accepted.",
		},
		{
			"/artifactory/api/nuget/nuget-remote/reindex", "deployer", "nuget-remote",
			"NuGet reindex calculation for repository 'nuget-remote' accepted.",
		},
		{
			"/artifactory/api/nuget/nuget-remote-cache/reindex", "deployer", "nuget-remote",
			"NuGet reindex calculation for repository 'nuget-remote-cache' accepted.",
		},
		{
			"/artifactory/api/helm/helm-local/reindex", "deployer", "helm-local",
			"Reindex of helm repo helm-local was scheduled to run.",
		},
		{
			"/artifactory/api/helm/helm-fed/reindex", "deployer", "helm-fed",
			"Reindex of helm repo helm-fed was scheduled to run.",
		},
		{
			"/artifactory/api/terraform/terraform-local/reindex", "admin", "terraform-local",
			"Reindexing of Terraform repository terraform-local was scheduled to run.",
		},
		{
			"/artifactory/api/pypi/pypi-local/reindex?async=true", "deployer", "pypi-local",
			"Recalculating index for pypi repository pypi-local scheduled to run",
		},
		{
			"/artifactory/api/pypi/pypi-local/reindex?async=1", "deployer", "pypi-local",
			"Recalculating index for pypi repository pypi-local scheduled to run",
		},
	} {
		wantText(t, w.do("POST", c.path, c.as, ""), c.text)
		if got := f.take(); len(got) != 0 {
			t.Errorf("%s: rebuilt before the queue ran: %v", c.path, got)
		}
		it, err := w.db.WorkQueue().Get(ctx, format.KindReindex, c.key)
		if err != nil || !strings.Contains(string(it.Payload), `"by":"`+c.as+`"`) {
			t.Errorf("%s: queued %+v: %v", c.path, it, err)
		}
		if err := q.EnsureFresh(ctx, format.KindReindex, c.key); err != nil {
			t.Fatal(err)
		}
		if got := f.take(); !slices.Equal(got, []string{c.key}) {
			t.Errorf("%s: rebuilt %v", c.path, got)
		}
	}
	// Manage is not enough for Terraform's.
	wantStatus(
		t,
		w.do("POST", "/artifactory/api/terraform/terraform-local/reindex", "deployer", ""),
		http.StatusForbidden,
	)
}

// PyPI's reindex runs before it answers unless async is true: the run
// leaves nothing queued, and a run that fails answers as it failed and
// stays queued for the workers to retry.
func TestAccPypiReindexRunsBeforeItAnswers(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	w, _ := reindexWorld(t, recording(f), true)
	const text = "Recalculating index for pypi repository pypi-local scheduled to run"
	for _, query := range []string{"", "?async=false", "?async=0"} {
		wantText(t, w.do("POST", "/artifactory/api/pypi/pypi-local/reindex"+query, "deployer", ""),
			text)
		if got := f.take(); !slices.Equal(got, []string{"pypi-local"}) {
			t.Errorf("%q: rebuilt %v", query, got)
		}
		if _, err := w.db.WorkQueue().Get(ctx, format.KindReindex, "pypi-local"); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			t.Errorf("%q: still queued: %v", query, err)
		}
	}

	f.err = format.Reject(http.StatusConflict, "refused by the format")
	rec := w.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "deployer", "")
	wantStatus(t, rec, http.StatusConflict)
	// In Artifactory's envelope, as every answer under /api/pypi/ (#532).
	if rec.Body.String() != `{"errors":[{"message":"refused by the format","status":409}]}`+"\n" {
		t.Errorf("refusal %q", rec.Body)
	}
	f.err = errors.New("boom")
	wantStatus(t, w.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "deployer", ""),
		http.StatusInternalServerError)
	it, err := w.db.WorkQueue().Get(ctx, format.KindReindex, "pypi-local")
	if err != nil || it.State != types.WorkPending || it.LastError != "boom" {
		t.Errorf("after a failed run: %+v %v", it, err)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/pypi/pypi-local/reindex?async=maybe", "admin", ""),
		http.StatusBadRequest)
}

// Each operation refuses as 7.x does: a missing repository 404, a caller
// without the right 401 or 403, a repository of another type, or of a
// class 7.x does not reindex, 400. NuGet virtual reindex remains P10. Each
// answers in its path's envelope.
func TestAccReindexRefusals(t *testing.T) {
	w, _ := reindexWorld(t, recording(&reindexRecorder{}), true)
	for _, c := range []struct {
		path, as string
		want     int
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
		if rec.Code != c.want {
			t.Errorf("%s as %q: %d, want %d: %s", c.path, c.as, rec.Code, c.want, rec.Body)
			continue
		}
		var body string
		switch s := surfaceFor(path); s {
		case apierror.Text:
			body = "\n"
		case apierror.TerraformRegistry:
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

// Helm's partial reindex is queued for the path it names, of one segment
// or several, escaped or cleaned, with the Helm reindex's permission,
// classes and answer; the format rebuilds the index of that path alone.
// A path that cleans to the root reindexes the repository, a GET is no
// reindex, and a path too long to queue is refused.
func TestAccHelmPartialReindex(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	w, q := reindexWorld(t, recording(f), true)
	const base = "/artifactory/api/helm/"

	for _, c := range []struct{ key, path string }{
		{"helm-local", "charts/team/app-1.0.0.tgz"}, {"helm-local", "charts/b/notes.txt"}, {"helm-fed", "charts/notes.txt"},
	} {
		if rec := w.do(
			"PUT",
			"/artifactory/"+c.key+"/"+c.path,
			"admin",
			"fixture",
		); rec.Code != http.StatusCreated {
			t.Fatalf("seed partial reindex: %d %s", rec.Code, rec.Body)
		}
	}
	for _, c := range []struct{ path, as, key string }{
		{"helm-local/charts/reindex", "deployer", "helm-local/charts"},
		{"helm-local/charts/team/app-1.0.0.tgz/reindex", "admin", "helm-local/charts/team/app-1.0.0.tgz"},
		{"helm-local/charts%2Fteam/reindex", "deployer", "helm-local/charts/team"},
		{"helm-local/a/../charts//b/reindex", "deployer", "helm-local/charts/b"},
		{"helm-fed/charts/reindex", "deployer", "helm-fed/charts"},
	} {
		repoKey, _, _ := strings.Cut(c.key, "/")
		wantText(t, w.do("POST", base+c.path, c.as, ""),
			"Reindex of helm repo "+repoKey+" was scheduled to run.")
		if _, err := w.db.WorkQueue().Get(ctx, format.KindReindexPath, c.key); err != nil {
			t.Errorf("%s: not queued as %s: %v", c.path, c.key, err)
			continue
		}
		if err := q.EnsureFresh(ctx, format.KindReindexPath, c.key); err != nil {
			t.Fatal(err)
		}
		if got := f.take(); !slices.Equal(got, []string{c.key}) {
			t.Errorf("%s: rebuilt %v", c.path, got)
		}
	}

	wantText(t, w.do("POST", base+"helm-local/./reindex", "deployer", ""),
		"Reindex of helm repo helm-local was scheduled to run.")
	if _, err := w.db.WorkQueue().Get(ctx, format.KindReindex, "helm-local"); err != nil {
		t.Errorf("the root: %v", err)
	}
	if rec := w.do(
		"GET",
		base+"helm-local/charts/reindex",
		"admin",
		"",
	); rec.Code == http.StatusOK {
		t.Errorf("a GET reindexed: %s", rec.Body)
	}
	for _, c := range []struct {
		path, as string
		want     int
	}{
		{"helm-local/charts/reindex", "user", http.StatusForbidden},
		{"helm-local/charts/reindex", "", http.StatusUnauthorized},
		{"missing/charts/reindex", "admin", http.StatusNotFound},
		{"helm-local/missing/reindex", "admin", http.StatusNotFound},
		{"helm-virtual/charts/reindex", "admin", http.StatusBadRequest},
		{"pypi-local/charts/reindex", "admin", http.StatusBadRequest},
		{
			"helm-local/" + strings.Repeat("x", store.MaxWorkKey) + "/reindex", "admin",
			http.StatusBadRequest,
		},
	} {
		if rec := w.do("POST", base+c.path, c.as, ""); rec.Code != c.want {
			t.Errorf("%.60s as %q: %d, want %d: %s", c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}

// Every P8 format now rebuilds its index, so none of their reindex
// operations answers 501 any more. The reindex operations of the package
// types no format here serves keep the stub, even for a format that
// rebuilds an index.
func TestAccReindexWithoutAReindexer(t *testing.T) {
	w, _ := reindexWorld(t, formats.For, false)
	// Terraform's format has landed and rebuilds its index (#552).
	wantText(t, w.do("POST", "/artifactory/api/terraform/terraform-local/reindex", "admin", ""),
		"Reindexing of Terraform repository terraform-local was scheduled to run.")

	f := &reindexRecorder{}
	every, _ := reindexWorld(t, func(repo.PackageType) format.Handler { return f }, false)
	for _, p := range []string{
		"bower/x/reindex", "cocoapods/x/reindex",
		"conan/x/reindex", "conda/x/reindex", "cran/reindex/x",
		"pub/x/reindex",
	} {
		wantStatus(
			t,
			every.do("POST", "/artifactory/api/"+p, "admin", ""),
			http.StatusNotImplemented,
		)
	}
	// Cargo, RubyGems and Swift now have native reindexers, so unknown repositories are 404.
	for _, packageType := range []string{"cargo", "gems", "swift"} {
		wantStatus(
			t,
			every.do("POST", "/artifactory/api/"+packageType+"/x/reindex", "admin", ""),
			http.StatusNotFound,
		)
	}
}

func TestAccDebianReindex(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	w, q := reindexWorld(t, func(repo.PackageType) format.Handler { return f }, true)
	c := repo.Config{Key: "debian-local", Class: repo.Local, PackageType: repo.Debian}
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	const path = "/artifactory/api/deb/reindex/debian-local"
	const answer = "Debian repository debian-local metadata recalculation accepted"

	wantText(t, w.do("POST", path, "admin", ""), answer)
	if got := f.take(); !slices.Equal(got, []string{c.Key}) {
		t.Errorf("synchronous reindex: %v", got)
	}
	wantText(t, w.do("POST", path+"?async=true", "admin", ""), answer)
	if got := f.take(); len(got) != 0 {
		t.Errorf("asynchronous reindex ran before the queue: %v", got)
	}
	if err := q.EnsureFresh(ctx, format.KindReindex, c.Key); err != nil {
		t.Fatal(err)
	}
	if got := f.take(); !slices.Equal(got, []string{c.Key}) {
		t.Errorf("asynchronous reindex: %v", got)
	}
	for _, tc := range []struct {
		path string
		as   string
		want int
	}{
		{path, "user", http.StatusForbidden},
		{path, "", http.StatusUnauthorized},
		{"/artifactory/api/deb/reindex/missing", "admin", http.StatusNotFound},
		{"/artifactory/api/deb/reindex/helm-local", "admin", http.StatusBadRequest},
		{path + "?async=invalid", "admin", http.StatusBadRequest},
		{path + "?writeProps=2", "admin", http.StatusBadRequest},
	} {
		rec := w.do("POST", tc.path, tc.as, "")
		if rec.Code != tc.want {
			t.Errorf("%s as %q: %d, want %d: %s", tc.path, tc.as, rec.Code, tc.want, rec.Body)
		}
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
		return artifactoryService{cfg: cfg}
	}
	status := func(err error) int {
		var e *apierror.Error
		if !errors.As(err, &e) {
			return 0
		}
		return e.Status
	}
	if _, err := on(nil).reindex(ctx, repo.NuGet, "nuget-local", "", false); status(err) !=
		http.StatusNotImplemented {
		t.Errorf("no store: %v", err)
	}
	if _, err := on(db).reindex(ctx, repo.NuGet, "nuget-local", "", false); status(err) !=
		http.StatusUnauthorized {
		t.Errorf("no caller: %v", err)
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{Name: "admin", Admin: true})
	for _, c := range []struct {
		fail string
		call func(artifactoryService) error
	}{
		{"settings", func(s artifactoryService) error {
			_, err := s.reindex(auth.WithPrincipal(ctx, auth.Principal{Name: "deployer"}),
				repo.NuGet, "nuget-local", "", false)
			return err
		}},
		{"enqueue", func(s artifactoryService) error {
			_, err := s.reindex(admin, repo.NuGet, "nuget-local", "", false)
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

// Without the runtime's queue, a reindex is left in the store's queue for
// whichever node polls it, and a synchronous one runs through a queue of
// the server's own; one it cannot build answers 500.
func TestAccReindexWithoutTheRuntimeQueue(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	w, _ := reindexWorld(t, recording(f), false)
	wantText(t, w.do("POST", "/artifactory/api/nuget/nuget-local/reindex", "deployer", ""),
		"NuGet reindex calculation for repository 'nuget-local' accepted.")
	if _, err := w.db.WorkQueue().Get(ctx, format.KindReindex, "nuget-local"); err != nil {
		t.Errorf("not queued: %v", err)
	}
	wantText(t, w.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "deployer", ""),
		"Recalculating index for pypi repository pypi-local scheduled to run")
	if got := f.take(); !slices.Equal(got, []string{"pypi-local"}) {
		t.Errorf("rebuilt %v", got)
	}

	claims, _ := reindexWorld(t, func(t repo.PackageType) format.Handler {
		if t == repo.Maven {
			return claimsReindex{}
		}
		return recording(f)(t)
	}, false)
	wantStatus(t, claims.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "deployer", ""),
		http.StatusInternalServerError)
}
