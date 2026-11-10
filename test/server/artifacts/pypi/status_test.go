package pypi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const overwriter = "overwriter:pw"

type statusCase struct {
	route, path, as, body string
	want                  int
}

func (c statusCase) method() string { method, _, _ := strings.Cut(c.route, " "); return method }

type statusPart struct {
	handwritten map[string]bool
	generated   map[string]bool
	seed        func(*testing.T, harness.Store, blob.Store)
	rows        func() []statusCase
	tune        harness.Tune
}

func grantOverwriter(t *testing.T, db harness.Store, key, under string) {
	t.Helper()
	if err := db.Permissions().Create(t.Context(), model.Permission{
		Name: "st-overwrite-" + key,
		Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
			Actions: model.Grants{Users: map[string]api.Actions{
				"overwriter": api.ActionRead | api.ActionWrite,
			}},
			Targets: map[string]model.Target{key: {Include: []string{under + "**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

type pypiStatusWorld struct {
	h http.Handler
}

func newPypiStatusWorld(t *testing.T, anonymous bool) pypiStatusWorld {
	t.Helper()
	h, _, _ := pypiServer(t, func(cfg *harness.Config) {
		if pypiStatusPart.tune != nil {
			pypiStatusPart.tune(cfg)
		}
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range append([]repo.Config{{Key: "libs-local", Class: repo.Local}}, pypiRepos[1:]...) {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		if err := cfg.Store.Users().Create(t.Context(), model.User{
			Name: "overwriter", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		pypiStatusPart.seed(t, cfg.Store, cfg.Blobs)
		libs, err := cfg.Store.Repos().Get(t.Context(), "libs-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&item.Service{
			Store: cfg.Store, Blobs: cfg.Blobs, Formats: ecosystem.For,
		}).Deploy(
			t.Context(),
			item.Deploy{
				Target: item.Target{
					Repo: libs,
					Path: "fixture/get.txt",
				},
				Body: strings.NewReader("fixture"),
			},
		); err != nil {
			t.Fatal(err)
		}
	})
	return pypiStatusWorld{h: h}
}

func TestAccPypiStatusTable(t *testing.T) {
	off := newPypiStatusWorld(t, false)
	on := newPypiStatusWorld(t, true)
	for _, c := range pypiStatusPart.rows() {
		h := off.h
		var headers map[string]string
		switch c.as {
		case "anonymous":
			h = on.h
		case "admin", "user":
			headers = basic(c.as, "pw")
		default:
			if user, password, ok := strings.Cut(c.as, ":"); ok {
				headers = basic(user, password)
			}
		}
		body := c.body
		if body != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			headers["Content-Type"], body = contentTypeOf(body)
		}
		rec := do(t, h, c.method(), c.path, headers, body)
		if rec.Code != c.want {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}
