package npm_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccNPMVirtualKeysRealBackends(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			if backend == "sqlite" {
				t.Setenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN", "")
			} else {
				t.Setenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN", dsn)
			}
			w := virtualNPMWorld(t, npmfmt.Settings{})
			w.Upstream = fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "origin", Password: "origin-secret"}})
			w.Repo.URL, w.Repo.Username, w.Repo.Password = w.Upstream.URL, "origin", "origin-secret"
			w.Update()
			body, localPublic := ecdsaKeyPairBody(t, "local-publisher", "ECDSA", "")
			harness.WantStatus(t, w.Request("POST", "/artifactory/api/security/keypair", "admin", body), 201)
			localKey, err := npmfmt.PublicKey(localPublic)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"first", "second"} {
				virtualNPMConfig(t, w, key, func(c *repo.Config) { c.PrimaryKeyPairRef = "local-publisher" })
			}
			virtualNPMLocal(t, w, "first", "shared", "1.0.0", "local winner")
			virtualNPMLocal(t, w, "second", "federated", "1.0.0", "federated signer")
			signing, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			remoteKey, err := npmfmt.PublicKey(&signing.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"shared", "remote-only"} {
				virtualNPMRemote(t, w, name, map[string]string{"1.0.0": "remote signer"}, map[string]string{"latest": "1.0.0"}, func(doc map[string]any) {
					dist := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
					sum := sha256.Sum256([]byte(name + "@1.0.0:" + dist["integrity"].(string)))
					sig, err := ecdsa.SignASN1(rand.Reader, signing, sum[:])
					if err != nil {
						t.Fatal(err)
					}
					dist["signatures"] = []npmfmt.Signature{{KeyID: remoteKey.KeyID, Sig: base64.StdEncoding.EncodeToString(sig)}}
				})
			}
			rawKeys, _ := json.Marshal(map[string]any{"keys": []npmfmt.Key{remoteKey}})
			w.Upstream.Set("/-/npm/v1/keys", fakeupstream.Resource{Body: rawKeys, Header: http.Header{"Content-Type": {"application/json"}}})
			root := npmBase + "virtual/"
			response := w.Request("GET", root+"-/npm/v1/keys", "admin", "")
			harness.WantStatus(t, response, 200)
			var document struct {
				Keys []npmfmt.Key `json:"keys"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Keys) != 2 || document.Keys[0].KeyID != localKey.KeyID || document.Keys[1].KeyID != remoteKey.KeyID {
				t.Fatal("whole first keys/order lost", response.Body.String())
			}
			// One local/federated key appears once; each version retains its own signer.
			for _, entry := range []struct {
				name string
				pub  *ecdsa.PublicKey
			}{{"shared", localPublic}, {"federated", localPublic}, {"remote-only", &signing.PublicKey}} {
				manifest := w.Request("GET", root+entry.name+"/1.0.0", "admin", "")
				harness.WantStatus(t, manifest, 200)
				var data struct {
					Dist npmVersionDist `json:"dist"`
				}
				if err := json.Unmarshal(manifest.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				if !verified(data.Dist, entry.pub, entry.name, "1.0.0") {
					t.Fatal("version signature changed supplying member", entry.name, manifest.Body.String())
				}
			}
			if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "keys-reader", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "keys-read-only", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"keys-reader": api.ActionRead}}, Targets: map[string]accesstest.Target{"first": {Include: []string{".npm/keys.json"}}, "remote": {Include: []string{".npm/keys.json"}}}}}}); err != nil {
				t.Fatal(err)
			}
			// The cached remote is warm. This caller has exact keys Read on real
			// members only, without package-folder Read or a virtual grant.
			harness.WantStatus(t, w.Request("GET", root+"-/npm/v1/keys", "keys-reader", ""), 200)
			virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new(".npm/keys.json") })
			before := len(w.Upstream.Requests())
			harness.WantStatus(t, w.Request("GET", root+"-/npm/v1/keys", "denied", ""), 403)
			harness.WantStatus(t, w.Request("GET", root+"-/npm/v1/keys", "admin", ""), 404)
			if len(w.Upstream.Requests()) != before {
				t.Fatal("root gates reached upstream")
			}
			virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = nil })
			// Credential revisions cannot reuse the old warmed key document.
			virtualNPMConfig(t, w, "remote", func(c *repo.Config) { c.Password = "wrong" })
			bad := w.Request("GET", root+"-/npm/v1/keys", "admin", "")
			harness.WantStatus(t, bad, 401)
			if bad.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("upstream refusal challenged caller")
			}
			virtualNPMConfig(t, w, "remote", func(c *repo.Config) { c.Password = "origin-secret" })
			harness.WantStatus(t, w.Request("GET", root+"-/npm/v1/keys", "admin", ""), 200)
			// Local configuration changes are live independently of the virtual's TTL.
			for _, key := range []string{"first", "second"} {
				virtualNPMConfig(t, w, key, func(c *repo.Config) { c.PrimaryKeyPairRef = "" })
			}
			onlyRemote := w.Request("GET", root+"-/npm/v1/keys", "admin", "")
			harness.WantStatus(t, onlyRemote, 200)
			if err := json.Unmarshal(onlyRemote.Body.Bytes(), &document); err != nil || len(document.Keys) != 1 || document.Keys[0].KeyID != remoteKey.KeyID {
				t.Fatal(onlyRemote.Body.String(), err)
			}
			virtualNPMConfig(t, w, "remote", func(c *repo.Config) { c.RetrievalCachePeriodSecs = new(0); c.MissedRetrievalCachePeriodSecs = new(0) })
			w.Upstream.Delete("/-/npm/v1/keys")
			none := w.Request("GET", root+"-/npm/v1/keys", "admin", "")
			harness.WantStatus(t, none, 404)
			// Repository-file provenance belongs to trusted file readers, not
			// API callers' query parameters or headers.
			harness.WantStatus(t, w.Request("GET", root+"-/npm/v1/keys?repositoryFile=true", "admin", "", "X-Repository-File", "true"), 404)
			if !strings.Contains(none.Body.String(), "not found") {
				t.Fatal(none.Body.String())
			}
		})
	}
}
