package pypi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func newPypiReindexWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(t, false, func(cfg *server.Config) {
		queue, err := work.New(cfg.Store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		derived := &item.Service{
			Store: cfg.Store, Blobs: cfg.Blobs, Work: queue,
			Formats: formats.For, ReindexAuthorize: reindexauth.New(cfg.Store, cfg.Auth),
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
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "manage-pypi",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionManage}},
			Targets: map[string]types.Target{"pypi-local": {Include: []string{"**"}}},
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
