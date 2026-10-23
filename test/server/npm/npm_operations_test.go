package npm_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// npmOpsWorld is an item world with npm repositories, a work queue that
// runs the npm format's work only when a test asks, and deployer holding
// Manage, and nothing else, on npm-local.
func npmOpsWorld(t *testing.T) (*itemWorld, *work.Queue) {
	t.Helper()
	var q *work.Queue
	w := newItemWorld(t, false, func(cfg *server.Config) {
		var err error
		if q, err = work.New(cfg.Store, work.Options{}); err != nil {
			t.Fatal(err)
		}
		derived := &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Work: q}
		if err := derived.RegisterWork(q); err != nil {
			t.Fatal(err)
		}
		cfg.Work = q
	})
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM, URL: "https://up.example"},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "manage-npm",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionManage}},
			Targets: map[string]types.Target{"npm-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return w, q
}

func (w *itemWorld) npmPackages() []string {
	w.t.Helper()
	pkgs, _, err := w.db.Packages().List(context.Background(), "npm-local",
		store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.Name)
	}
	return out
}

// A reindex is queued for a caller holding Manage, and answers 7.x's text
// at once; the npm format rebuilds the index when the queue runs it. A
// refusal answers in Artifactory's envelope, while a GET of a package
// called reindex is the registry's, in npm's.
func TestNpmReindexIsQueued(t *testing.T) {
	ctx := context.Background()
	w, q := npmOpsWorld(t)
	wantStatus(t, w.do("PUT", "/artifactory/npm-local/"+npmfmt.CanonicalPath("left-pad", "1.0.0"),
		"admin", string(npmTarballOf("left-pad", "1.0.0"))), http.StatusCreated)
	p, err := w.db.Packages().Find(ctx, "npm-local", "left-pad")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Packages().Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	rec := w.do("POST", "/artifactory/api/npm/npm-local/reindex", "deployer", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "Recalculating index for npm repository npm-local scheduled to run" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("%q %q", rec.Body, rec.Header().Get("Content-Type"))
	}
	if got := w.npmPackages(); len(got) != 0 {
		t.Fatalf("reindexed before the queue ran: %v", got)
	}
	if err := q.EnsureFresh(ctx, format.KindReindex, "npm-local"); err != nil {
		t.Fatal(err)
	}
	if got := w.npmPackages(); len(got) != 1 {
		t.Fatalf("after the run: %v", got)
	}
	for _, c := range []struct {
		path, as string
		want     int
	}{
		{"npm-local", "user", http.StatusForbidden},
		{"npm-local", "", http.StatusUnauthorized},
		{"missing", "admin", http.StatusNotFound},
		{"libs-local", "admin", http.StatusBadRequest},
		{"npm-remote", "admin", http.StatusBadRequest},
		{"npm-remote-cache", "admin", http.StatusBadRequest},
		// deployer manages npm-local alone.
		{"libs-local", "deployer", http.StatusForbidden},
	} {
		rec := w.do("POST", "/artifactory/api/npm/"+c.path+"/reindex", c.as, "")
		wantStatus(t, rec, c.want)
		if !strings.HasPrefix(rec.Body.String(), `{"errors":[`) {
			t.Errorf("%s as %q: %s", c.path, c.as, rec.Body)
		}
	}
	rec = w.do("GET", "/artifactory/api/npm/npm-local/reindex", "admin", "")
	wantStatus(t, rec, http.StatusNotFound)
	if !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Errorf("package reindex: %s", rec.Body)
	}
}

// The strict-layout conversion is queued for a caller holding Manage and
// answers 202 with no body; a conversion the queue parked is resumed, and
// with none parked, resume answers 400.
func TestNpmConversion(t *testing.T) {
	ctx := context.Background()
	w, q := npmOpsWorld(t)
	const base = "/artifactory/api/v1/migrations/npm/repositories/"
	wantStatus(t, w.do("PUT", "/artifactory/npm-local/stray/left-pad.tgz", "admin",
		string(npmTarballOf("left-pad", "1.0.0"))), http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/npm-local/"+npmfmt.CanonicalPath("blocked", "1.0.0"),
		"admin", "garbage"), http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/npm-local/blocked.tgz", "admin",
		string(npmTarballOf("blocked", "1.0.0"))), http.StatusCreated)
	wantStatus(t, w.do("POST", base+"npm-local/resume", "deployer", ""), http.StatusBadRequest)
	rec := w.do("POST", base+"npm-local/migrate", "deployer", "")
	wantStatus(t, rec, http.StatusAccepted)
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q", rec.Body)
	}
	it, err := w.db.WorkQueue().Get(ctx, npmfmt.KindConvert, "npm-local")
	if err != nil || !strings.Contains(string(it.Payload), `"by":"deployer"`) {
		t.Fatalf("queued %+v %v", it, err)
	}
	// Still pending, so there is nothing to resume.
	wantStatus(t, w.do("POST", base+"npm-local/resume", "deployer", ""), http.StatusBadRequest)
	if err := q.EnsureFresh(ctx, npmfmt.KindConvert, "npm-local"); err == nil ||
		!strings.Contains(err.Error(), "blocked.tgz") {
		t.Fatalf("run: %v", err)
	}
	if got := w.npmPackages(); len(got) != 1 || got[0] != "left-pad" {
		t.Fatalf("converted %v", got)
	}
	// Retries spent, the queue parks it; resume queues it again.
	park(t, w.db, npmfmt.KindConvert, "npm-local")
	wantStatus(t, w.do("POST", base+"npm-local/resume", "deployer", ""), http.StatusAccepted)
	it, err = w.db.WorkQueue().Get(ctx, npmfmt.KindConvert, "npm-local")
	if err != nil || it.State != types.WorkPending {
		t.Fatalf("resumed %+v %v", it, err)
	}
	for _, c := range []struct {
		op, key, as string
		want        int
	}{
		{"migrate", "npm-local", "user", http.StatusForbidden},
		{"resume", "npm-local", "", http.StatusUnauthorized},
		{"migrate", "missing", "admin", http.StatusNotFound},
		{"resume", "libs-local", "admin", http.StatusBadRequest},
		{"migrate", "npm-remote", "admin", http.StatusBadRequest},
	} {
		wantStatus(t, w.do("POST", base+c.key+"/"+c.op, c.as, ""), c.want)
	}
}

// park parks the work item of kind on key, as the queue does once an
// item's retries run out.
func park(t *testing.T, st store.Store, kind, key string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	q := st.WorkQueue()
	if err := q.Enqueue(ctx, kind, key, nil, now); err != nil {
		t.Fatal(err)
	}
	it, took, err := q.LeaseKey(ctx, kind, key, store.WorkLease{
		Holder: "test", Until: now.Add(time.Minute), Now: now,
	})
	if err != nil || !took {
		t.Fatalf("lease %v %v", took, err)
	}
	if err := q.Fail(ctx, kind, key, it.Generation, store.WorkFailure{
		Error: "spent", Park: true, RetryAt: now, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}
