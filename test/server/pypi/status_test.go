package pypi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	seed        func(*testing.T, store.Store, blob.Store)
	rows        func() []statusCase
	tune        func(*server.Config)
}

func grantOverwriter(t *testing.T, db store.Store, key, under string) {
	t.Helper()
	if err := db.Permissions().Create(t.Context(), types.Permission{
		Name: "st-overwrite-" + key,
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"overwriter": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{key: {Include: []string{under + "**"}}},
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
	h, _, _ := pypiServer(t, func(cfg *server.Config) {
		if pypiStatusPart.tune != nil {
			pypiStatusPart.tune(cfg)
		}
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range append([]repo.Config{{Key: "libs-local", Class: repo.Local}}, pypiRepos[1:]...) {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		if err := cfg.Store.Users().Create(t.Context(), types.User{
			Name: "overwriter", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		pypiStatusPart.seed(t, cfg.Store, cfg.Blobs)
		libs, err := cfg.Store.Repos().Get(t.Context(), "libs-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&item.Service{Store: cfg.Store, Blobs: cfg.Blobs}).Deploy(
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
