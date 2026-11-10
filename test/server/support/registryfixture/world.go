package registryfixture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// World is the production router over an in-memory store and a file-system
// blob store, built by New for one test. Do sends requests to Handler; DB and
// Blobs let a test seed state or check what a request left behind. Blobs
// signs download URLs for the storage route, as a cloud blob store would.
type World struct {
	T       *testing.T
	Handler http.Handler
	DB      *inmem.Store
	Blobs   blob.Store
}

type signingBlobs struct {
	blob.Store
	routes []blob.Via
	broken error
}

func (b signingBlobs) Signs(via blob.Via) bool { return slices.Contains(b.routes, via) }

func (b signingBlobs) SignURL(_ context.Context, sha string, o blob.SignOptions) (string, error) {
	if b.broken != nil {
		return "", b.broken
	}
	q := url.Values{"ttl": {o.TTL.String()}, "name": {o.Filename}, "type": {o.ContentType}}
	return "https://" + o.Via.String() + ".example/" + sha + "?" + q.Encode(), nil
}

// New builds the same full-router item world used by the root registry tests.
// The tune callbacks run after stores and authentication are ready.
func New(t *testing.T, anonymous bool, tune ...harness.Tune) *World {
	t.Helper()
	ctx := t.Context()
	db := inmem.New()
	support.Seed(t, db)
	for _, user := range []accesstest.User{
		{Name: "reader", PasswordHash: support.CheapHash, Groups: []string{authn.ReadersGroup}},
		{Name: "deployer", PasswordHash: support.CheapHash},
		{Name: "user", PasswordHash: support.CheapHash},
	} {
		if err := db.Users().Create(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "deploy-libs",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"deployer": api.ActionRead | api.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().Create(ctx, accesstest.Permission{
			Name: "anonymous-remotes",
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{
					kernel.AnonymousUser: api.ActionRead,
				}},
				Targets: map[string]accesstest.Target{
					api.TargetAnyRemote: {Include: []string{"**"}},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "other-local", Class: repo.Local},
		{Key: "server-local", Class: repo.Local, ChecksumPolicyType: repo.ServerGeneratedChecksums},
		{Key: "redirect-local", Class: repo.Local, DownloadRedirect: repo.Flex(true)},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
		{
			Key: "virt", Class: repo.Virtual,
			Repositories:          []string{"missing", "other-local", "libs-local", "up"},
			DefaultDeploymentRepo: "libs-local",
		},
		{Key: "loop", Class: repo.Virtual, Repositories: []string{"loop"}},
		{Key: "virt-nodeploy", Class: repo.Virtual, Repositories: []string{"libs-local"}},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fsBlobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: support.TestHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		BaseURL:       "https://reg.example",
		CompatVersion: "7.155.0",
		Store:         db,
		Blobs:         signingBlobs{Store: fsBlobs, routes: []blob.Via{blob.ViaStorage}},
		Auth:          a,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return &World{T: t, Handler: harness.Router(cfg), DB: db, Blobs: cfg.Blobs}
}

// Do sends a request to the world's router as the user as, with the password
// "pw" over Basic, or without credentials when as is empty. headers are name
// and value pairs; a trailing name without a value is ignored.
func (w *World) Do(method, path, as, body string, headers ...string) *httptest.ResponseRecorder {
	w.T.Helper()
	h := map[string]string{}
	if as != "" {
		h = support.Basic(as, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return support.Do(w.T, w.Handler, method, path, h, body)
}

// Item returns the item at path in the repository key, from its remote cache
// when cached is true, and fails the test when the store has no such item or
// cannot read it.
func (w *World) Item(key string, cached bool, path string) artmodel.Item {
	w.T.Helper()
	it, err := w.DB.Items().Get(w.T.Context(), key, cached, path)
	if err != nil {
		w.T.Fatalf("item %s/%s: %v", key, path, err)
	}
	return it
}
