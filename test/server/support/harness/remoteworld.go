package harness

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/app"
	"github.com/weaveplatform/artifactweave/migration/app/config"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// RemoteWorld is the router over an application runtime that App opened,
// with a generic remote repository in front of a fake upstream, built by
// NewRemoteWorld for one test. Every user's password is "pw": admin; reader,
// which may read the remote; denied, which may not. A test that changes Repo
// saves it with Update, and one that needs other router options replaces H
// with a router over Runtime.
type RemoteWorld struct {
	T        *testing.T
	Runtime  *app.Runtime
	H        http.Handler
	Upstream *fakeupstream.Server
	Repo     repo.Config
}

// NewRemoteWorld builds a RemoteWorld for t: App with the admin password
// "pw"; the generic remote repository "remote" over a fake upstream, with
// repo.Defaults' settings; the users reader and denied, whose password hash
// is support.CheapHash; the permission remote-read, which lets reader read
// remote; and the router over the runtime's store, hot blob store,
// authenticator, token service and keyring and the artifacts module's
// remote sources and remote HTTP pool, with a logger that discards.
func NewRemoteWorld(t *testing.T) *RemoteWorld {
	t.Helper()
	rt := App(t, func(cfg *config.Config) { cfg.Bootstrap.AdminPassword = "pw" })
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: up.URL,
	})
	if err := rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reader", "denied"} {
		if err := rt.Store.Users().
			Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"remote": {}},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	h := Router(Config{
		Store:         rt.Store,
		Blobs:         rt.Blobs,
		Auth:          rt.Auth,
		Tokens:        rt.Tokens,
		Keys:          rt.Keys,
		RemoteSources: rt.Artifacts.RemoteSources(),
		RemoteHTTP:    rt.Artifacts.RemoteHTTP(),
		Logger:        slog.New(slog.DiscardHandler),
	})
	return &RemoteWorld{T: t, Runtime: rt, H: h, Upstream: up, Repo: c}
}

// Request sends method path with body to H, as user with the password "pw"
// when user is not empty and anonymously otherwise, with headers given as
// name, value pairs, and returns the response.
func (w *RemoteWorld) Request(
	method, path, user, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.T.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	w.H.ServeHTTP(rec, r)
	return rec
}

// Update saves Repo, which the test changed, through the runtime's store.
func (w *RemoteWorld) Update() {
	w.T.Helper()
	if err := w.Runtime.Store.Repos().Update(w.T.Context(), w.Repo); err != nil {
		w.T.Fatal(err)
	}
}
