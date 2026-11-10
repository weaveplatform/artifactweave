package pypi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access"
	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func newPypiReindexWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(t, false, func(cfg *harness.Config) {
		queue, err := work.New(cfg.Store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		module, err := access.New(access.Deps{Store: cfg.Store, Authenticator: cfg.Auth})
		if err != nil {
			t.Fatal(err)
		}
		api := module.API()
		derived := &item.Service{
			Store: cfg.Store, Blobs: cfg.Blobs, Work: queue, Formats: ecosystem.For,
			ReindexAuthorize: reindexauth.New(api.Authenticator, api.Authorizer),
		}
		if err := derived.RegisterWork(queue); err != nil {
			t.Fatal(err)
		}
		cfg.Work = queue
	})
	c := repo.Config{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), model.Permission{
		Name: "manage-pypi",
		Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
			Actions: model.Grants{Users: map[string]api.Actions{"deployer": api.ActionManage}},
			Targets: map[string]model.Target{"pypi-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

func wantText(t *testing.T, rec *httptest.ResponseRecorder, expected string) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Body.String() != expected ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("%d %q %q, want %q", rec.Code, rec.Body,
			rec.Header().Get("Content-Type"), expected)
	}
}
