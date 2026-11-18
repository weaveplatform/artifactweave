package terraform_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccTerraformVirtualProviderBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var mutate []func(*config.Config)
			if backend == "postgres" {
				dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
				mutate = append(mutate, func(c *config.Config) { c.Database = config.Database{Driver: "postgres", DSN: dsn} })
			}
			w := harness.NewRemoteWorld(t, mutate...)
			up := fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "origin", Password: "private-provider"}})
			w.Repo = *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Terraform, URL: up.URL, TerraformRegistryURL: up.URL, TerraformProvidersURL: up.URL, Username: "origin", Password: "private-provider", ExcludesPattern: new("hashicorp/null/**")})
			w.Update()
			// A request-scoped test service copy keeps fault injection out of
			// the runtime's independent background source workers.
			sources := *w.Runtime.Artifacts.RemoteSources()
			w.H = harness.Router(
				harness.Config{
					Store:         w.Runtime.Store,
					Blobs:         w.Runtime.Blobs,
					Auth:          w.Runtime.Auth,
					Tokens:        w.Runtime.Tokens,
					Keys:          support.TerraformBackendKeys(t),
					RemoteSources: &sources,
					RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
					Logger:        slog.New(slog.DiscardHandler),
				},
			)
			key, err := terraformtest.NewKey()
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Keys().CreateKeypair(t.Context(), api.Keypair{Name: "hosted-key", Type: api.KeypairGPG, PublicKey: key.Public}); err != nil {
				t.Fatal(err)
			}
			for _, c := range []repo.Config{
				{Key: "hosted", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformProvider, PrimaryKeyPairRef: "hosted-key"},
				{Key: "federated", Class: repo.Federated, PackageType: repo.Terraform, TerraformType: repo.TerraformProvider, PrimaryKeyPairRef: "hosted-key"},
				{Key: "modules", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformModule},
				{Key: "inner", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"hosted"}},
				{Key: "virtual", Class: repo.Virtual, PackageType: repo.Terraform, TerraformType: repo.TerraformModule, Repositories: []string{"modules", "remote", "inner", "federated"}},
			} {
				if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
					t.Fatal(err)
				}
			}
			hosted := terraformtest.Release{Type: "null", Version: "2.0.0", Platforms: []string{"linux_amd64"}, Protocols: []string{"5.0"}}
			files := hosted.Files(key)
			for name, body := range files {
				harness.WantStatus(t, w.Request("PUT", "/artifactory/hosted/prefix/hashicorp/null/2.0.0/"+name, "admin", string(body)), 201)
			}
			for name, body := range (terraformtest.Release{Type: "null", Version: "3.0.0", Platforms: []string{"linux_amd64"}}).Files(key) {
				harness.WantStatus(t, w.Request("PUT", "/artifactory/federated/hashicorp/null/3.0.0/"+name, "admin", string(body)), 201)
			}
			// A namespace equal to its supplying member key keeps its full path.
			for name, body := range (terraformtest.Release{Type: "null", Version: "4.0.0", Platforms: []string{"linux_amd64"}}).Files(key) {
				harness.WantStatus(t, w.Request("PUT", "/artifactory/hosted/prefix/hosted/null/4.0.0/"+name, "admin", string(body)), 201)
			}
			remoteKey, err := terraformtest.NewKey()
			if err != nil {
				t.Fatal(err)
			}
			jsonAt := func(path string, doc any) {
				t.Helper()
				body, _ := json.Marshal(doc)
				up.Set(path, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/json"}}})
			}
			jsonAt("/.well-known/terraform.json", map[string]string{"providers.v1": "/registry/providers/"})
			remoteFiles := map[string][][]byte{}
			remoteRelease := func(version string) {
				t.Helper()
				archive := terraformtest.ModuleZip(map[string]string{"terraform-provider-null_v" + version: "private provider " + version})
				digest := sha256.Sum256(archive)
				filename := "terraform-provider-null_" + version + "_linux_amd64.zip"
				sums := []byte(hex.EncodeToString(digest[:]) + "  " + filename + "\n")
				signature := remoteKey.Sign(sums)
				remoteFiles[version] = [][]byte{archive, sums, signature}
				base := "/storage/" + version + "/"
				jsonAt(
					"/registry/providers/hashicorp/null/"+version+"/download/linux/amd64",
					map[string]any{
						"protocols":             []string{"6.0"},
						"os":                    "linux",
						"arch":                  "amd64",
						"filename":              filename,
						"download_url":          base + "provider.zip",
						"shasums_url":           base + "SHA256SUMS",
						"shasums_signature_url": base + "SHA256SUMS.sig",
						"shasum":                hex.EncodeToString(digest[:]),
						"signing_keys": map[string]any{
							"gpg_public_keys": []any{
								map[string]string{"key_id": remoteKey.KeyID(), "ascii_armor": remoteKey.Public, "source": "private origin", "trust_signature": "unchanged"},
							},
						},
					},
				)
				for index, path := range []string{"provider.zip", "SHA256SUMS", "SHA256SUMS.sig"} {
					up.Set(base+path, fakeupstream.Resource{Body: remoteFiles[version][index], Header: http.Header{"Content-Type": {"application/octet-stream"}}})
				}
			}
			remoteRelease("1.0.0")
			remoteRelease("2.0.0")
			versions := func(extra bool) {
				rows := []any{}
				for _, version := range []string{"1.0.0", "2.0.0"} {
					rows = append(rows, map[string]any{"version": version, "protocols": []string{"6.0"}, "platforms": []any{map[string]string{"os": "linux", "arch": "amd64"}}})
				}
				if extra {
					rows = append(rows, map[string]any{"version": "1.1.0", "protocols": []string{"6.0"}, "platforms": []any{map[string]string{"os": "linux", "arch": "amd64"}}})
				}
				jsonAt("/registry/providers/hashicorp/null/versions", map[string]any{"versions": rows})
			}
			versions(false)
			base := "/artifactory/api/terraform/v1/providers/virtual-rt-ns-hashicorp/null/"
			list := w.Request("GET", base+"versions", "admin", "")
			harness.WantStatus(t, list, 200)
			if strings.Count(list.Body.String(), `"version"`) != 3 {
				t.Fatal(list.Body.String())
			}
			var selectedRemote string
			for _, version := range []string{"2.0.0", "1.0.0"} {
				before := len(up.Requests())
				metadata := w.Request("GET", base+version+"/download/linux/amd64", "admin", "")
				harness.WantStatus(t, metadata, 200)
				var doc map[string]json.RawMessage
				if err := json.Unmarshal(metadata.Body.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if version == "2.0.0" && len(up.Requests()) != before {
					t.Fatal("hosted exact provider holder contacted upstream")
				}
				keyText := key.Public
				if version == "1.0.0" {
					keyText = remoteKey.Public
				}
				if !bytes.Contains(doc["signing_keys"], []byte(strings.ReplaceAll(keyText, "\n", `\n`))) {
					t.Fatal("mixed provider keys", string(doc["signing_keys"]))
				}
				for index, field := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
					var target string
					if err := json.Unmarshal(doc[field], &target); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(target, "/virtual/providers/") {
						t.Fatal(target)
					}
					actual := w.Request("GET", target, "", "")
					harness.WantStatus(t, actual, 200)
					expected := remoteFiles[version][index]
					if version == "2.0.0" {
						name := hosted.Name("linux_amd64.zip")
						if index == 1 {
							name = hosted.Name("SHA256SUMS")
						}
						if index == 2 {
							name = hosted.Name("SHA256SUMS.sig")
						}
						expected = files[name]
					}
					if !bytes.Equal(actual.Body.Bytes(), expected) {
						t.Fatal("provider bundle bytes changed", version, field)
					}
					if version == "1.0.0" && index == 0 {
						selectedRemote = target
					}
				}
			}
			mirrorHost, _ := url.Parse(up.URL)
			mirror := "/artifactory/api/terraform/virtual/providers/" + mirrorHost.Host + "/hashicorp/null/"
			harness.WantStatus(t, w.Request("GET", mirror+"index.json", "admin", ""), 200)
			for _, version := range []string{"1.0.0", "2.0.0", "3.0.0"} {
				r := w.Request("GET", mirror+version+".json", "admin", "")
				harness.WantStatus(t, r, 200)
				var doc struct {
					Archives map[string]struct {
						URL    string   `json:"url"`
						Hashes []string `json:"hashes"`
					} `json:"archives"`
				}
				if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				a := doc.Archives["linux_amd64"]
				if len(a.Hashes) == 0 || !strings.HasPrefix(a.Hashes[0], "h1:") || !strings.Contains(a.URL, "/virtual/providers/") {
					t.Fatal(doc)
				}
				harness.WantStatus(t, w.Request("GET", a.URL, "", ""), 200)
			}
			namespace := w.Request("GET", "/artifactory/api/terraform/v1/providers/virtual-rt-ns-hosted/null/4.0.0/download/linux/amd64", "admin", "")
			harness.WantStatus(t, namespace, 200)
			if !strings.Contains(namespace.Body.String(), "/virtual/providers/prefix/hosted/null/4.0.0/") {
				t.Fatal(namespace.Body.String())
			}
			// Refresh immediately before fault injection: a slow race run must
			// exercise source storage rather than the capability's 90-second expiry.
			refreshRemote := func() string {
				t.Helper()
				response := w.Request("GET", base+"1.0.0/download/linux/amd64", "admin", "")
				harness.WantStatus(t, response, 200)
				var doc struct {
					URL string `json:"download_url"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				return doc.URL
			}
			selectedRemote = refreshRemote()
			// Genuine source-store failures retain 500 on a valid capability.
			resolve := sources.Resolve
			sources.Resolve = func(context.Context, repo.Config) (model.SourceIdentity, error) {
				return model.SourceIdentity{}, fmt.Errorf("source lookup: %w", kernel.ErrStore)
			}
			harness.WantStatus(t, w.Request("GET", selectedRemote, "", ""), 500)
			sources.Resolve = resolve
			// Terraform ignores the remote-fetch flag (R7), even on a cold
			// version after a non-Terraform-originating server's request.
			remoteRelease("1.1.0")
			versions(true)
			c, err := w.Runtime.Store.Repos().Get(t.Context(), "remote")
			if err != nil {
				t.Fatal(err)
			}
			c.RetrievalCachePeriodSecs = new(0)
			if err := w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			cold := w.Request("GET", base+"1.1.0/download/linux/amd64", "reader", "", "X-Artifactory-Originated", "other-node")
			harness.WantStatus(t, cold, 200)
			if up.Count("GET", "/registry/providers/hashicorp/null/1.1.0/download/linux/amd64") == 0 {
				t.Fatal("R7 exemption did not reach cold upstream")
			}
			for _, request := range up.Requests() {
				if !request.Authorized {
					t.Fatal("private provider request was unauthenticated", request.Path)
				}
			}
			harness.WantStatus(t, w.Request("GET", base+"versions", "denied", ""), 404)
			selectedRemote = refreshRemote()
			c, err = w.Runtime.Store.Repos().Get(t.Context(), "virtual")
			if err != nil {
				t.Fatal(err)
			}
			c.Repositories = []string{"modules", "inner", "federated"}
			if err := w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			refused := w.Request("GET", selectedRemote, "", "")
			harness.WantStatus(t, refused, 403)
			if refused.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
				t.Fatal(refused.Body.String())
			}
		})
	}
}
