package terraform_test

import (
	"bytes"
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

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The native CLI discovers the registry, resolves a tagged module, then
// fetches the signed archive without credentials. The host override copies
// the server's discovery response, as decision 0040 prescribes for HTTP.
func TestAccTerraformModuleInit(t *testing.T) {
	binary := os.Getenv("ARTIFACTWEAVE_TERRAFORM_BIN")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("terraform")
		if err != nil {
			t.Skip("terraform CLI not installed")
		}
	}
	db := inmem.New()
	cheapSeed(t, db)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := repo.Config{
		Key:           "modules",
		Class:         repo.Local,
		PackageType:   repo.Terraform,
		TerraformType: repo.TerraformModule,
	}
	if err = db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	archive := zipBytes(
		t,
		map[string]string{"main.tf": `output "message" { value = "downloaded through registry" }`},
	)
	items := &item.Service{Store: db, Blobs: blobs}
	if _, err = items.Deploy(
		t.Context(),
		item.Deploy{
			Target: item.Target{Repo: c, Path: "acme/demo/aws/v1.0.0.zip"},
			Body:   bytes.NewReader(archive),
		},
	); err != nil {
		t.Fatal(err)
	}
	tokens, _ := seedTokens(t, db)
	issued, err := tokens.Issue(t.Context(), token.Request{Username: "admin", ByAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(
		db.Users(),
		db.Settings(),
		auth.Options{Hash: testHash, Tokens: tokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Store: db, Blobs: blobs, Auth: a, Tokens: tokens}
	tuneRegistry(&cfg)
	h := server.New(cfg)
	// A sig parameter on a generated JFrog operation cannot authorize it.
	wantStatus(
		t,
		do(t, h, "POST", "/artifactory/api/terraform/modules/reindex?sig=bad", nil, ""),
		401,
	)

	// Invalid credentials are ignored both for discovery and signed archive
	// downloads, but do not bypass authentication on a registry operation.
	expectDiscovery := do(t, h, "GET", "/.well-known/terraform.json", bearerAuth("invalid"), "")
	wantStatus(t, expectDiscovery, 200)
	s := httptest.NewServer(h)
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		s.URL+"/.well-known/terraform.json",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var discovery map[string]string
	err = json.NewDecoder(response.Body).Decode(&discovery)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cli := fmt.Sprintf(
		"host %q {\n services = {\n  \"modules.v1\" = %q\n  \"providers.v1\" = %q\n }\n}\ncredentials %q { token = %q }\n",
		u.Host,
		discovery["modules.v1"],
		discovery["providers.v1"],
		u.Host,
		issued.AccessToken,
	)
	configPath := filepath.Join(dir, "cli.tfrc")
	if err = os.WriteFile(configPath, []byte(cli), 0o600); err != nil {
		t.Fatal(err)
	}
	main := fmt.Sprintf(
		"module \"test\" {\n source = %q\n version = \"1.0.0\"\n}\n",
		u.Host+"/modules__acme/demo/aws",
	)
	if err = os.WriteFile(filepath.Join(dir, "main.tf"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "init", "-backend=false", "-input=false", "-no-color")
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"TF_CLI_CONFIG_FILE="+configPath,
		"CHECKPOINT_DISABLE=1",
		"TF_IN_AUTOMATION=1",
		"TF_DATA_DIR="+filepath.Join(dir, ".terraform"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("terraform init: %v\n%s", err, out)
	}
	t.Logf("terraform init: %s", out)
	installed, err := os.ReadFile(filepath.Join(dir, ".terraform", "modules", "test", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "downloaded through registry") {
		t.Fatal(string(installed))
	}
	// Exercise the real item-serving path, including HEAD, Range and the
	// credentials bypass, with the URL supplied to the CLI.
	download := do(
		t,
		h,
		"GET",
		"/artifactory/api/terraform/v1/modules/modules__acme/demo/aws/1.0.0/download",
		bearerAuth(issued.AccessToken),
		"",
	)
	wantStatus(t, download, 204)
	signed := download.Header().Get("X-Terraform-Get")
	got := do(t, h, "GET", signed, bearerAuth("expired"), "")
	wantStatus(t, got, 200)
	if !bytes.Equal(got.Body.Bytes(), archive) {
		t.Fatal("archive differs")
	}
	got = do(t, h, "HEAD", signed, bearerAuth("expired"), "")
	wantStatus(t, got, 200)
	headers := bearerAuth("expired")
	headers["Range"] = "bytes=0-3"
	got = do(t, h, "GET", signed, headers, "")
	wantStatus(t, got, 206)
	if !bytes.Equal(got.Body.Bytes(), archive[:4]) {
		t.Fatal("range differs")
	}
	if err := db.Users().
		Update(t.Context(), types.User{Name: "admin", Status: types.UserDisabled}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, do(t, h, "GET", signed, nil, ""), 403)
}
