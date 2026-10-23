// Package registryfixture assembles the real server for registry protocol
// suites. Docker and Helm OCI use the same repository, auth and blob-signing
// boundary without importing root-package test helpers.
package registryfixture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

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
func New(t *testing.T, anonymous bool, tune ...func(*server.Config)) *World {
	t.Helper()
	ctx := t.Context()
	db := inmem.New()
	support.Seed(t, db)
	for _, user := range []types.User{
		{Name: "reader", PasswordHash: support.CheapHash, Groups: []string{auth.ReadersGroup}},
		{Name: "deployer", PasswordHash: support.CheapHash},
		{Name: "user", PasswordHash: support.CheapHash},
	} {
		if err := db.Users().Create(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-libs",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: "anonymous-remotes",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					auth.AnonymousUser: types.ActionRead,
				}},
				Targets: map[string]types.Target{types.TargetAnyRemote: {Include: []string{"**"}}},
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
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: support.TestHash})
	if err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{
		BaseURL:       "https://reg.example",
		CompatVersion: "7.155.0",
		Store:         db,
		Blobs:         signingBlobs{Store: fsBlobs, routes: []blob.Via{blob.ViaStorage}},
		Auth:          a,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return &World{T: t, Handler: server.New(cfg), DB: db, Blobs: cfg.Blobs}
}

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

func (w *World) Item(key string, cached bool, path string) types.Item {
	w.T.Helper()
	it, err := w.DB.Items().Get(w.T.Context(), key, cached, path)
	if err != nil {
		w.T.Fatalf("item %s/%s: %v", key, path, err)
	}
	return it
}
