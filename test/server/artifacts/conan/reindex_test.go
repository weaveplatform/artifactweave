package conan_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// reindexRecorder stands in for the Conan format in the reindex tests: the
// generic format made a Reindexer that records the key of each rebuild.
type reindexRecorder struct {
	generic.Handler
	mu   sync.Mutex
	runs []string
}

func (f *reindexRecorder) Reindex(_ context.Context, _ format.Env, c repo.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, c.Key)
	return nil
}

// take returns the rebuilds run since it was last called.
func (f *reindexRecorder) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	runs := f.runs
	f.runs = nil
	return runs
}

// The Conan reindex runs before it answers unless async is true: "The
// calculation can be synchronous (the default) or asynchronous"
// (calculateconanrepositorymetadata, #1384). It answers the archived 7.x
// reference's sample text either way, and only to an administrator ("Requires
// a valid admin user"), so a caller who holds Manage alone gets 403.
func TestAccConanReindexWaitsUnlessAsync(t *testing.T) {
	ctx := context.Background()
	f := &reindexRecorder{}
	var q *work.Queue
	w := newItemWorld(t, false, func(cfg *Config) {
		pick := func(t repo.PackageType) format.Handler {
			if t == repo.Conan {
				return f
			}
			return ecosystem.For(t)
		}
		var err error
		if q, err = work.New(cfg.Store, work.Options{}); err != nil {
			t.Fatal(err)
		}
		svc := &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Work: q, Formats: pick}
		if err := svc.RegisterWork(q); err != nil {
			t.Fatal(err)
		}
		cfg.Formats, cfg.Work = pick, q
	})
	targets := map[string]accesstest.Target{}
	for _, c := range []repo.Config{
		{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-fed", Class: repo.Federated, PackageType: repo.Conan},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		targets[c.Key] = accesstest.Target{Include: []string{"**"}}
	}
	if err := w.db.Permissions().Create(ctx, accesstest.Permission{
		Name: "manage-conan",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"deployer": api.ActionManage}},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"conan-local", "conan-fed"} {
		path := "/artifactory/api/conan/" + key + "/reindex"
		answer := "Recalculating index for Conan repository " + key + " scheduled to run."
		for _, query := range []string{"", "?async=false"} {
			wantReindexText(t, w.do(http.MethodPost, path+query, "admin", ""), answer)
			if got := f.take(); !slices.Equal(got, []string{key}) {
				t.Errorf("%s%s: rebuilt %v before the answer, want [%s]", key, query, got, key)
			}
		}
		wantReindexText(t, w.do(http.MethodPost, path+"?async=true", "admin", ""), answer)
		if got := f.take(); len(got) != 0 {
			t.Errorf("%s?async=true: rebuilt %v before the queue ran", key, got)
		}
		if err := q.EnsureFresh(ctx, format.KindReindex, key); err != nil {
			t.Fatal(err)
		}
		if got := f.take(); !slices.Equal(got, []string{key}) {
			t.Errorf("%s?async=true: the queue rebuilt %v, want [%s]", key, got, key)
		}
		for _, query := range []string{"", "?async=true"} {
			rec := w.do(http.MethodPost, path+query, "deployer", "")
			const forbidden = `{"errors":[{"message":"Forbidden","status":403}]}` + "\n"
			if rec.Code != http.StatusForbidden || rec.Body.String() != forbidden {
				t.Errorf("%s%s as a manager: %d %q, want 403 %q",
					key, query, rec.Code, rec.Body, forbidden)
			}
		}
		if got := f.take(); len(got) != 0 {
			t.Errorf("%s: a refused reindex rebuilt %v", key, got)
		}
	}
}

// wantReindexText checks a reindex's 200 and its plain-text answer.
func wantReindexText(t *testing.T, rec *httptest.ResponseRecorder, text string) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Body.String() != text ||
		rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("%d %q %q, want 200 %q text/plain",
			rec.Code, rec.Body, rec.Header().Get("Content-Type"), text)
	}
}
