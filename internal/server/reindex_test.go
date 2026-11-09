package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// claimsReindex is a format that claims a reindex kind as its own work,
// which the item service refuses to register.
type claimsReindex struct{ generic.Handler }

func (claimsReindex) WorkKinds() []string { return []string{format.KindReindex} }

func (claimsReindex) Work(context.Context, format.Env, types.WorkItem) error { return nil }

func (claimsReindex) Owed(repo.Config, bool, string) []format.WorkRef { return nil }

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

	f.err = format.Rejectf(http.StatusConflict, "refused by the format")
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
		expected int
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
		if rec := w.do("POST", base+c.path, c.as, ""); rec.Code != c.expected {
			t.Errorf("%.60s as %q: %d, want %d: %s", c.path, c.as, rec.Code, c.expected, rec.Body)
		}
	}
}

// Every P8 format now rebuilds its index, so none of their reindex
// operations answers 501 any more. The reindex operations of the package
// types no format here serves keep the stub, even for a format that
// rebuilds an index.
func TestAccReindexWithoutAReindexer(t *testing.T) {
	w, _ := reindexWorld(t, ecosystem.For, false)
	// Terraform's format has landed and rebuilds its index (#552).
	wantText(t, w.do("POST", "/artifactory/api/terraform/terraform-local/reindex", "admin", ""),
		"Reindexing of Terraform repository terraform-local was scheduled to run.")

	without, _ := reindexWorld(
		t,
		func(repo.PackageType) format.Handler { return generic.Handler{} },
		false,
	)
	wantStatus(t, without.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "admin", ""),
		http.StatusNotImplemented)

	f := &reindexRecorder{}
	every, _ := reindexWorld(t, func(repo.PackageType) format.Handler { return f }, false)
	// Native reindexers return 404 for unknown repositories.
	for _, packageType := range []string{"bower", "cargo", "cocoapods", "conan", "conda", "cran", "gems", "pub", "swift"} {
		wantStatus(
			t,
			every.do("POST", "/artifactory/api/"+packageType+"/x/reindex", "admin", ""),
			http.StatusNotFound,
		)
	}
}

func TestUnitCocoaPodsReindex(t *testing.T) {
	w, _ := reindexWorld(t, ecosystem.For, false)
	c := repo.Config{Key: "pods-local", Class: repo.Local, PackageType: repo.CocoaPods}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	wantText(t, w.do("POST", "/artifactory/api/cocoapods/pods-local/reindex", "admin", ""),
		"Reindex of CocoaPods repo pods-local was scheduled to run.")
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
		path     string
		as       string
		expected int
	}{
		{path, "user", http.StatusForbidden},
		{path, "", http.StatusUnauthorized},
		{"/artifactory/api/deb/reindex/missing", "admin", http.StatusNotFound},
		{"/artifactory/api/deb/reindex/helm-local", "admin", http.StatusBadRequest},
		{path + "?async=invalid", "admin", http.StatusBadRequest},
		{path + "?writeProps=2", "admin", http.StatusBadRequest},
	} {
		rec := w.do("POST", tc.path, tc.as, "")
		if rec.Code != tc.expected {
			t.Errorf("%s as %q: %d, want %d: %s", tc.path, tc.as, rec.Code, tc.expected, rec.Body)
		}
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
