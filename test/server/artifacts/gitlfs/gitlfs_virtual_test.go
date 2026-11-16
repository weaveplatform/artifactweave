package gitlfs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const lfsMedia = "application/vnd.git-lfs+json"

// lfsWorld is the router over an application runtime on one SQL store with
// the Git LFS repositories of a virtual repository over [remote, nested
// virtual -> local]: lfs-virtual over lfs-remote and lfs-inner, which
// holds lfs-local and lfs-virtual again, a cycle; lfs-virtual deploys to
// lfs-local, and lfs-nodeploy, over lfs-inner, has no deployment target.
// lfs-remote reads the Git LFS endpoint /origin of a fake upstream. reader
// may read lfs-local and read and fill lfs-remote; deployer may deploy to
// lfs-local and do nothing else. Every password is "pw".
type lfsWorld struct {
	t  *testing.T
	rt *app.Runtime
	h  http.Handler
	up *fakeupstream.Server
}

func newLFSWorld(t *testing.T, backend string) *lfsWorld {
	t.Helper()
	rt := harness.App(t, func(cfg *config.Config) {
		cfg.Bootstrap.AdminPassword = "pw"
		if backend == "postgres" {
			cfg.Database.Driver = "postgres"
			cfg.Database.DSN = platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
		}
	})
	w := &lfsWorld{t: t, rt: rt, up: fakeupstream.New(t, fakeupstream.Options{})}
	for _, c := range []repo.Config{
		{Key: "lfs-remote", Class: repo.Remote, URL: w.up.URL + "/origin"},
		{Key: "lfs-local", Class: repo.Local},
		{
			Key: "lfs-virtual", Class: repo.Virtual,
			Repositories: []string{"lfs-remote", "lfs-inner"}, DefaultDeploymentRepo: "lfs-local",
		},
		{Key: "lfs-inner", Class: repo.Virtual, Repositories: []string{"lfs-local", "lfs-virtual"}},
		{Key: "lfs-nodeploy", Class: repo.Virtual, Repositories: []string{"lfs-inner"}},
	} {
		c.PackageType = repo.GitLFS
		if err := rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"reader", "deployer"} {
		if err := rt.Store.Users().
			Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	for _, grant := range []struct {
		name, user, target string
		actions            accessapi.Actions
	}{
		{"lfs-remote-fill", "reader", "lfs-remote", accessapi.ActionRead | accessapi.ActionWrite},
		{"lfs-local-read", "reader", "lfs-local", accessapi.ActionRead},
		{"lfs-local-deploy", "deployer", "lfs-local", accessapi.ActionWrite},
	} {
		if err := rt.Store.Permissions().Create(t.Context(), accesstest.Permission{
			Name: grant.name,
			Resources: map[accessapi.ResourceType]accesstest.Resource{
				accessapi.ResourceArtifact: {
					Actions: accesstest.Grants{Users: map[string]accessapi.Actions{grant.user: grant.actions}},
					Targets: map[string]accesstest.Target{grant.target: {}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.h = harness.Router(harness.Config{
		Store:         rt.Store,
		Blobs:         rt.Blobs,
		Auth:          rt.Auth,
		Tokens:        rt.Tokens,
		Keys:          rt.Keys,
		RemoteSources: rt.Artifacts.RemoteSources(),
		RemoteHTTP:    rt.Artifacts.RemoteHTTP(),
		Logger:        slog.New(slog.DiscardHandler),
	})
	return w
}

// do sends method under /artifactory/api/lfs/ with body as user, whose
// password is "pw"; a batch carries the Git LFS media type.
func (w *lfsWorld) do(method, path, user, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequest(method, "/artifactory/api/lfs/"+path, strings.NewReader(body))
	if strings.HasSuffix(path, "/objects/batch") {
		r.Header.Set("Content-Type", lfsMedia)
		r.Header.Set("Accept", lfsMedia)
	}
	r.SetBasicAuth(user, "pw")
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// batch asks key's batch API for one object as user and returns its
// action's href, failing unless the answer hands out that action.
func (w *lfsWorld) batch(key, user, operation, oid string, size int) string {
	w.t.Helper()
	rec := w.do(http.MethodPost, key+"/objects/batch", user,
		`{"operation":"`+operation+`","objects":[{"oid":"`+oid+`","size":`+strconv.Itoa(size)+`}]}`)
	var reply struct {
		Objects []struct {
			Actions map[string]struct {
				Href string `json:"href"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil ||
		len(reply.Objects) != 1 || reply.Objects[0].Actions[operation].Href == "" {
		w.t.Fatalf("%s %s batch: %d %s", key, operation, rec.Code, rec.Body)
	}
	return reply.Objects[0].Actions[operation].Href
}

func objectOf(body []byte) (oid, path string) {
	sum := sha256.Sum256(body)
	oid = hex.EncodeToString(sum[:])
	return oid, "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
}

// A virtual repository over [remote, nested virtual -> local] (#1540):
// deployer, who may deploy to lfs-local alone, pushes an object through
// it, where the virtual repository's key was never a deployment target
// and the nested member answered 400; reader fetches it from the nested
// local member past the cycle back to lfs-virtual, without asking the
// remote's upstream, as local members come first (decision 0008, R1 and
// R2); an object only the remote's upstream holds comes from it into its
// cache; and lfs-nodeploy refuses an upload with 7.x's 405 (R6).
func TestAccGitLFSVirtualOverRemoteAndNestedVirtual(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newLFSWorld(t, backend)
			local := []byte("pushed through a virtual Git LFS repository\n")
			oid, path := objectOf(local)
			transfer := "/objects/" + oid + "?size=" + strconv.Itoa(len(local))
			w.batch("lfs-virtual", "deployer", "upload", oid, len(local))
			rec := w.do(http.MethodPut, "lfs-virtual"+transfer, "deployer", string(local))
			harness.WantStatus(t, rec, http.StatusOK)
			if _, err := w.rt.Store.Items().Get(t.Context(), "lfs-local", false, path); err != nil {
				t.Fatalf("pushed object not in lfs-local: %v", err)
			}
			w.batch("lfs-virtual", "reader", "download", oid, len(local))
			rec = w.do(http.MethodGet, "lfs-virtual"+transfer, "reader", "")
			if rec.Code != http.StatusOK || rec.Body.String() != string(local) {
				t.Fatalf("local object through the nested member: %d %q", rec.Code, rec.Body)
			}
			if n := w.up.Count(http.MethodPost, "/origin/objects/batch"); n != 0 {
				t.Fatalf("remote's upstream asked %d times for a local member's object", n)
			}

			remote := []byte("held by the remote member's upstream alone\n")
			roid, rpath := objectOf(remote)
			w.up.Set("/origin/objects/batch", fakeupstream.Resource{
				Method: http.MethodPost, Status: http.StatusOK,
				Header: http.Header{"Content-Type": {lfsMedia}},
				Body: []byte(`{"transfer":"basic","objects":[{"oid":"` + roid + `","size":` +
					strconv.Itoa(len(remote)) + `,"actions":{"download":{"href":"` +
					w.up.URL + `/origin/objects/` + roid + `"}}}]}`),
			})
			w.up.Set("/origin/objects/"+roid, fakeupstream.Resource{Body: remote})
			rtransfer := "/objects/" + roid + "?size=" + strconv.Itoa(len(remote))
			w.batch("lfs-virtual", "reader", "download", roid, len(remote))
			rec = w.do(http.MethodGet, "lfs-virtual"+rtransfer, "reader", "")
			if rec.Code != http.StatusOK || rec.Body.String() != string(remote) {
				t.Fatalf("remote object through the virtual: %d %q", rec.Code, rec.Body)
			}
			if _, err := w.rt.Store.Items().Get(t.Context(), "lfs-remote", true, rpath); err != nil {
				t.Fatalf("remote object not cached: %v", err)
			}

			rec = w.do(http.MethodPut, "lfs-nodeploy"+transfer, "deployer", string(local))
			harness.WantStatus(t, rec, http.StatusMethodNotAllowed)
			if rec.Header().Get("Allow") != http.MethodGet || !strings.Contains(rec.Body.String(),
				"No local repository was configured as local deployment repository for the "+
					"(lfs-nodeploy) virtual repository.") {
				t.Fatalf("upload without a deployment target: %v %s", rec.Header(), rec.Body)
			}
		})
	}
}
