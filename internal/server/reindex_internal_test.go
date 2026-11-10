package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
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
