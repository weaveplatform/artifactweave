package terraform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The pinned jf packages both modules through ordinary item deployment,
// publishes their checksums as build-info, and Terraform consumes those
// very archives using either its credentials file or TF_TOKEN_<host>.
func TestAccJFTerraformPublish(t *testing.T) {
	jf := os.Getenv("ARTIFACTWEAVE_JF_BIN")
	tf := os.Getenv("ARTIFACTWEAVE_TERRAFORM_BIN")
	if jf == "" || tf == "" {
		t.Skip("set ARTIFACTWEAVE_JF_BIN and ARTIFACTWEAVE_TERRAFORM_BIN for native clients")
	}
	h := statusFixture(t, false, allowBasicTokens, func(c *Config) {
		c.BaseURL = ""
		tuneRegistry(c)
		if err := c.Store.Permissions().Create(t.Context(), types.Permission{
			Name: "module-reader",
			Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {
					Actions: types.Grants{
						Users: map[string]types.Actions{"user": types.ActionRead},
					},
					Targets: map[string]types.Target{"tf-publish": {Include: []string{"acme/**"}}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	})
	wantStatus(t, do(
		t,
		h,
		"PUT",
		"/artifactory/api/repositories/tf-publish",
		jsonAs(
			basic("admin", "pw"),
		),
		`{"rclass":"local","packageType":"terraform","terraformType":"MODULE"}`,
	), 200)
	admin := mintAdmin(t, h, `{"expires_in":3600}`)["access_token"].(string)
	reader := mintAdmin(t, h,
		`{"username":"user","scope":"applied-permissions/user","expires_in":3600}`)["access_token"].(string)
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		write("publish/"+name+"/main.tf", fmt.Sprintf(`output "name" { value = %q }`, name))
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TF_") && !strings.HasPrefix(kv, "JFROG_") &&
			!strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "TERRAFORM_CONFIG=") {
			env = append(env, kv)
		}
	}
	env = append(env,
		"HOME="+dir, "JFROG_CLI_HOME_DIR="+filepath.Join(dir, "jf-home"),
		"JFROG_CLI_TEMP_DIR="+filepath.Join(dir, "jf-temp"),
		"JFROG_CLI_REPORT_USAGE=false", "JFROG_CLI_AVOID_NEW_VERSION_WARNING=true", "CI=true",
		"TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1",
	)
	run := func(binary, sub string, extra []string, args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = filepath.Join(dir, sub)
		cmd.Env = append(append([]string{}, env...), extra...)
		return cmd.CombinedOutput()
	}
	for _, args := range [][]string{
		{
			"config", "add", "aw", "--url", server.URL, "--artifactory-url", server.URL + "/artifactory",
			"--access-token", admin, "--interactive=false",
		},
		{"terraform-config", "--server-id-deploy=aw", "--repo-deploy=tf-publish"},
		{
			"terraform", "publish", "--namespace=acme", "--provider=aws", "--tag=v1.2.3",
			"--build-name=terraform-publish", "--build-number=1",
		},
		{"rt", "bp", "terraform-publish", "1"},
	} {
		if out, err := run(jf, "publish", nil, args...); err != nil {
			t.Fatalf("jf %s: %v\n%s", args[0], err, out)
		}
	}
	build := do(t, h, "GET", "/artifactory/api/build/terraform-publish/1", bearerAuth(admin), "")
	wantStatus(t, build, 200)
	var doc struct {
		BuildInfo struct {
			Modules []struct {
				ID, Type  string
				Artifacts []struct{ Name, Path, OriginalDeploymentRepo, SHA1, SHA256, MD5 string }
			}
		}
	}
	if err := json.Unmarshal(build.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.BuildInfo.Modules) != 1 {
		t.Fatalf("build modules: %s", build.Body)
	}
	sums := map[string]types.Checksums{}
	for _, module := range doc.BuildInfo.Modules {
		if module.ID != "terraform-publish" || module.Type != "terraform" ||
			len(module.Artifacts) != 2 {
			t.Fatalf("build module: %+v", module)
		}
		for _, a := range module.Artifacts {
			if a.Name != "v1.2.3.zip" || a.OriginalDeploymentRepo != "tf-publish" ||
				a.SHA1 == "" || a.SHA256 == "" || a.MD5 == "" {
				t.Fatalf("artifact: %+v", a)
			}
			if _, duplicate := sums[a.Path]; duplicate {
				t.Fatalf("duplicate artifact path: %q", a.Path)
			}
			sums[a.Path] = types.Checksums{SHA1: a.SHA1, SHA256: a.SHA256, MD5: a.MD5}
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		path := "acme/" + name + "/aws/v1.2.3.zip"
		rec := do(t, h, "GET", "/artifactory/api/storage/tf-publish/"+path, bearerAuth(admin), "")
		wantStatus(t, rec, 200)
		var stored struct{ Checksums types.Checksums }
		if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
			t.Fatalf("stored checksums: %s, %v", rec.Body, err)
		}
		want, ok := sums[path]
		if !ok || want.SHA1 != stored.Checksums.SHA1 ||
			want.MD5 != stored.Checksums.MD5 || want.SHA256 != stored.Checksums.SHA256 {
			t.Fatalf("%s build checksums %+v, stored %+v", path, want, stored.Checksums)
		}
	}
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	write(".terraformrc", fmt.Sprintf("host %q {\n services = {\n \"modules.v1\" = %q\n }\n}\n",
		u.Host, server.URL+"/artifactory/api/terraform/v1/modules/"))
	credentials, err := json.Marshal(map[string]any{"credentials": map[string]any{
		u.Host: map[string]string{"token": reader},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write(".terraform.d/credentials.tfrc.json", string(credentials))
	for _, mode := range []string{"file", "environment", "anonymous"} {
		t.Run(mode, func(t *testing.T) {
			write(
				mode+"/main.tf",
				fmt.Sprintf("module \"example\" {\n source = %q\n version = \"1.2.3\"\n}\n",
					u.Host+"/tf-publish__acme/alpha/aws"),
			)
			extra := []string{}
			if mode != "file" {
				// Environment credentials must override a bad file token.
				write(".terraform.d/credentials.tfrc.json",
					fmt.Sprintf(`{"credentials":{%q:{"token":"invalid"}}}`, u.Host))
			}
			if mode == "environment" {
				extra = append(extra, "TF_TOKEN_"+strings.ReplaceAll(u.Host, ".", "_")+"="+reader)
			}
			if mode == "anonymous" {
				write(".terraform.d/credentials.tfrc.json", `{}`)
			}
			out, err := run(tf, mode, extra, "init", "-backend=false", "-input=false", "-no-color")
			if mode == "anonymous" {
				if err == nil || !strings.Contains(string(out), "401") {
					t.Fatalf("anonymous init: %v\n%s", err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("terraform init with %s: %v\n%s", mode, err, out)
			}
			content, err := os.ReadFile(
				filepath.Join(dir, mode, ".terraform/modules/example/main.tf"),
			)
			if err != nil || !strings.Contains(string(content), `"alpha"`) {
				t.Fatalf("installed module: %s, %v", content, err)
			}
		})
	}
	wantStatus(
		t,
		do(t, h, http.MethodGet, "/.well-known/terraform.json", bearerAuth("invalid"), ""),
		200,
	)
}
