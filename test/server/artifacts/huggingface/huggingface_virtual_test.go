package huggingface_test

import (
	"crypto/sha1" //nolint:gosec // a Git blob ID, which the Hub answers.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

// hubVirtualWorld is an item world whose router runs over an application
// runtime on one SQL store, with Xet on, and the Hugging Face repositories
// of a virtual repository over [remote, nested virtual -> local]:
// hf-virtual over hf-remote and hf-inner, which holds hf-local and
// hf-virtual again, a cycle; hf-virtual deploys to hf-local, and
// hf-nodeploy, over hf-inner, has no deployment target. hf-remote reads a
// fake upstream that serves the model acme/remote. member may read hf-local
// and read and fill hf-remote; deployer may read and deploy to hf-local and
// do nothing else. Neither has a grant on a virtual repository. Every
// password is "pw".
type hubVirtualWorld struct {
	*itemWorld
	rt *app.Runtime
	up *fakeupstream.Server
}

func newHubVirtualWorld(t *testing.T, backend string) *hubVirtualWorld {
	t.Helper()
	rt := harness.App(t, func(cfg *config.Config) {
		cfg.Bootstrap.AdminPassword = "pw"
		if backend == "postgres" {
			cfg.Database.Driver = "postgres"
			cfg.Database.DSN = platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
		}
	})
	w := &hubVirtualWorld{rt: rt, up: fakeupstream.New(t, fakeupstream.Options{})}
	for _, c := range []repo.Config{
		{Key: "hf-remote", Class: repo.Remote, URL: w.up.URL},
		{Key: "hf-local", Class: repo.Local},
		{
			Key: "hf-virtual", Class: repo.Virtual,
			Repositories: []string{"hf-remote", "hf-inner"}, DefaultDeploymentRepo: "hf-local",
		},
		{Key: "hf-inner", Class: repo.Virtual, Repositories: []string{"hf-local", "hf-virtual"}},
		{Key: "hf-nodeploy", Class: repo.Virtual, Repositories: []string{"hf-inner"}},
	} {
		c.PackageType = repo.HuggingFaceML
		if err := rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"member", "deployer"} {
		if err := rt.Store.Users().
			Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	read, write := accessapi.ActionRead, accessapi.ActionRead|accessapi.ActionWrite
	for _, grant := range []struct {
		name, user, target string
		actions            accessapi.Actions
	}{
		{"hf-remote-fill", "member", "hf-remote", write},
		{"hf-local-read", "member", "hf-local", read},
		{"hf-local-deploy", "deployer", "hf-local", write},
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
	w.itemWorld = &itemWorld{t: t, h: harness.Router(harness.Config{
		Store:                 rt.Store,
		Blobs:                 rt.Blobs,
		Auth:                  rt.Auth,
		Tokens:                rt.Tokens,
		Keys:                  support.TerraformBackendKeys(t),
		RemoteSources:         rt.Artifacts.RemoteSources(),
		RemoteHTTP:            rt.Artifacts.RemoteHTTP(),
		HuggingFaceXetEnabled: true,
		Logger:                slog.New(slog.DiscardHandler),
	})}
	return w
}

// serveRemoteModel has the upstream serve revision rev of acme/remote with
// the one file config.json holding content, as the Hub does.
func (w *hubVirtualWorld) serveRemoteModel(rev string, content []byte) {
	git := sha1.New() //nolint:gosec // a Git blob ID, which the Hub answers.
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(content))
	_, _ = git.Write(content)
	info, err := json.Marshal(map[string]any{
		"id": "acme/remote", "sha": rev, "siblings": []map[string]any{{
			"rfilename": "config.json", "size": len(content),
			"blobId": hex.EncodeToString(git.Sum(nil)),
		}},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	typed := http.Header{"Content-Type": {"application/json"}}
	w.up.Set("/api/models/acme/remote/revision/main", fakeupstream.Resource{Body: info, Header: typed})
	w.up.Set("/acme/remote/resolve/main/config.json", fakeupstream.Resource{Body: content, Header: typed})
}

// A virtual repository over [remote, nested virtual -> local] (#1540):
// deployer, who may deploy to hf-local alone, commits a model through it and
// publishes a Xet file, where the virtual repository's key was asked for
// Write and the nested member answered 400; member, who may read the
// members alone, reads the model from the nested local member past the
// cycle back to hf-virtual, a Xet reconstruction of it through a read
// token, and a model only the remote member's upstream holds; hf-nodeploy
// refuses a write with 7.x's 405 (decision 0008, R1, R2 and R6).
func TestAccHuggingFaceVirtualOverRemoteAndNestedVirtual(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newHubVirtualWorld(t, backend)
			endpoint := "/artifactory/api/huggingfaceml/hf-virtual"

			write := xetTokenFor(t, w.itemWorld, "hf-virtual", "acme/demo", "write", "deployer")
			wantStatus(t, xetCall(w.itemWorld, write, "POST",
				"/v1/xorbs/default/"+xetFixtureHash, xetFixture(t, "large.xorb")), 200)
			wantStatus(t, xetCall(w.itemWorld, write, "POST", "/v2/shards",
				xetFixture(t, "large.shard")), 200)
			data := make([]byte, 1280000)
			for i := range data {
				data[i] = byte(i)
			}
			sum := sha256.Sum256(data)
			commit := fmt.Sprintf(
				"{\"key\":\"header\",\"value\":{\"summary\":\"through a virtual\"}}\n"+
					"{\"key\":\"file\",\"value\":{\"path\":\"config.json\",\"content\":\"e30=\",\"encoding\":\"base64\"}}\n"+
					"{\"key\":\"lfsFile\",\"value\":{\"path\":\"weights.bin\",\"algo\":\"sha256\",\"oid\":\"%x\",\"size\":%d}}\n",
				sum, len(data))
			wantStatus(t, w.do("POST", endpoint+"/api/models/acme/demo/commit/main", "deployer",
				commit, "Content-Type", "application/x-ndjson"), 200)

			listed := w.do("GET", endpoint+"/api/models/acme/demo/revision/main", "member", "")
			wantStatus(t, listed, 200)
			if !strings.Contains(listed.Body.String(), `"weights.bin"`) {
				t.Fatalf("model through the nested member: %s", listed.Body)
			}
			resolved := w.do("GET", endpoint+"/acme/demo/resolve/main/config.json", "member", "")
			wantStatus(t, resolved, 200)
			if resolved.Body.String() != "{}" {
				t.Fatalf("file through the nested member: %q", resolved.Body)
			}
			if n := w.up.Count(http.MethodGet, "/api/models/acme/demo/revision/main"); n != 0 {
				t.Fatalf("remote's upstream asked %d times for a local member's model", n)
			}
			read := xetTokenFor(t, w.itemWorld, "hf-virtual", "acme/demo", "read", "member")
			wantStatus(t, xetCall(w.itemWorld, read, "GET",
				"/v1/reconstructions/"+xetFixtureFile, nil), 200)

			w.serveRemoteModel(strings.Repeat("c", 40), []byte(`{"model_type":"remote"}`))
			remote := w.do("GET", endpoint+"/acme/remote/resolve/main/config.json", "member", "")
			wantStatus(t, remote, 200)
			if remote.Body.String() != `{"model_type":"remote"}` {
				t.Fatalf("remote member's model: %q", remote.Body)
			}

			refused := w.do("POST",
				"/artifactory/api/huggingfaceml/hf-nodeploy/api/models/acme/demo/preupload/main",
				"deployer", `{"files":[{"path":"config.json","sample":"e30=","size":2}]}`,
				"Content-Type", "application/json")
			wantStatus(t, refused, http.StatusMethodNotAllowed)
			if refused.Header().Get("Allow") != http.MethodGet || !strings.Contains(refused.Body.String(),
				"No local repository was configured as local deployment repository for the "+
					"(hf-nodeploy) virtual repository.") {
				t.Fatalf("write without a deployment target: %v %s", refused.Header(), refused.Body)
			}
		})
	}
}
