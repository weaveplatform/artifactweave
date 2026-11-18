package terraform_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccTerraformVirtualModuleBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var mutate []func(*config.Config)
			if backend == "postgres" {
				dsn := platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
				mutate = append(mutate, func(c *config.Config) { c.Database.Driver = "postgres"; c.Database.DSN = dsn })
			}
			w := harness.NewRemoteWorld(t, mutate...)
			w.Repo = *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Terraform, URL: w.Upstream.URL, TerraformRegistryURL: w.Upstream.URL})
			w.Update()
			w.H = harness.Router(
				harness.Config{
					Store:         w.Runtime.Store,
					Blobs:         w.Runtime.Blobs,
					Auth:          w.Runtime.Auth,
					Tokens:        w.Runtime.Tokens,
					Keys:          support.TerraformBackendKeys(t),
					RemoteSources: w.Runtime.Artifacts.RemoteSources(),
					RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
					Logger:        slog.New(slog.DiscardHandler),
				},
			)
			for _, c := range []repo.Config{
				{Key: "acme", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformModule},
				{Key: "fed", Class: repo.Federated, PackageType: repo.Terraform, TerraformType: repo.TerraformModule},
				{Key: "providers", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformProvider},
				{Key: "nested", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"acme", "fed"}},
				{Key: "virtual", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"providers", "nested", "remote"}, DefaultDeploymentRepo: "acme"},
			} {
				if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
					t.Fatal(err)
				}
			}
			local := terraformtest.ModuleZip(map[string]string{"main.tf": `output "release" { value = "local" }`})
			harness.WantStatus(t, w.Request("PUT", "/artifactory/virtual/acme/demo/aws/1.0.0.zip", "admin", string(local)), 201)
			harness.WantStatus(t, w.Request("GET", "/artifactory/acme/acme/demo/aws/1.0.0.zip", "admin", ""), 200)
			putJSON := func(path string, doc any) {
				body, _ := json.Marshal(doc)
				w.Upstream.Set(path, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/json"}}})
			}
			putJSON("/.well-known/terraform.json", map[string]string{"modules.v1": "/registry/modules/"})
			putJSON(
				"/registry/modules/acme/demo/aws/versions",
				map[string]any{"modules": []any{map[string]any{"versions": []any{map[string]string{"version": "1.0.0"}, map[string]string{"version": "2.0.0"}}}}},
			)
			for _, version := range []string{"1.0.0", "2.0.0"} {
				w.Upstream.Set(
					"/registry/modules/acme/demo/aws/"+version+"/download",
					fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {"/archives/" + version + ".zip"}}},
				)
				w.Upstream.Set(
					"/archives/"+version+".zip",
					fakeupstream.Resource{
						Body: terraformtest.ModuleZip(map[string]string{"main.tf": `module "dependency" { source = "elsewhere.invalid/acme/dependency/aws//modules/child" }`}),
					},
				)
			}
			base := "/artifactory/api/terraform/virtual/v1/modules/acme/demo/aws/"
			list := w.Request("GET", base+"versions", "admin", "")
			harness.WantStatus(t, list, 200)
			if strings.Count(list.Body.String(), `"version"`) != 2 {
				t.Fatal(list.Body)
			}
			before := len(w.Upstream.Requests())
			metadata := w.Request("GET", base+"1.0.0/download", "admin", "")
			harness.WantStatus(t, metadata, 204)
			public := metadata.Header().Get("X-Terraform-Get")
			if !strings.Contains(public, "/virtual/v1/modules/acme/demo/aws/1.0.0.zip?sig=") {
				t.Fatal(public)
			}
			delivered := w.Request("GET", public, "", "")
			harness.WantStatus(t, delivered, 200)
			if !bytes.Equal(delivered.Body.Bytes(), local) || len(w.Upstream.Requests()) != before {
				t.Fatal("first hosted owner did not retain bytes and full namespace")
			}
			// Terraform is exempt from R7 even when its stored flag remains false.
			remote := w.Request("GET", base+"2.0.0/download", "reader", "", "X-Artifactory-Originated", "another-node")
			harness.WantStatus(t, remote, 204)
			signed := remote.Header().Get("X-Terraform-Get")
			delivered = w.Request("GET", signed, "", "")
			harness.WantStatus(t, delivered, 200)
			raw, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, "archives/2.0.0.zip")
			if err != nil {
				t.Fatal(err)
			}
			opened, _, err := w.Runtime.Blobs.Open(t.Context(), raw.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			cached, _ := io.ReadAll(opened)
			_ = opened.Close()
			if bytes.Equal(cached, delivered.Body.Bytes()) {
				t.Fatal("remote registry source was not rewritten")
			}
			harness.WantStatus(t, w.Request("GET", base+"versions", "denied", ""), 404)
			c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
			if err != nil {
				t.Fatal(err)
			}
			c.Repositories = []string{"nested", "providers"}
			c.DefaultDeploymentRepo = ""
			if err := w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			refusal := w.Request("GET", signed, "", "")
			harness.WantStatus(t, refusal, 403)
			if !strings.Contains(refusal.Body.String(), "Invalid or expired Terraform download URL") {
				t.Fatal(refusal.Body)
			}
			refused := w.Request("PUT", "/artifactory/virtual/acme/demo/aws/3.0.0.zip", "admin", string(local))
			harness.WantStatus(t, refused, 405)
			if refused.Header().Get("Allow") != "GET" {
				t.Fatal(refused.Header())
			}
			harness.WantStatus(t, w.Request("GET", "/artifactory/api/terraform/virtual/v1/providers/hashicorp/null/versions", "admin", ""), 501)
		})
	}
}
