package terraform_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type Config = server.Config

var (
	testHash              = support.TestHash
	cheapHash             = support.CheapHash
	terraformFixtureRepos = append(
		support.TerraformRegistryRepos(),
		support.TerraformProviderRepos()...)
)

func cheapSeed(t *testing.T, db store.Store)        { t.Helper(); support.Seed(t, db) }
func basic(user, password string) map[string]string { return support.Basic(user, password) }
func bearerAuth(value string) map[string]string     { return support.Bearer(value) }
func contentTypeOf(body string) (string, string)    { return "application/json", body }
func allowBasicTokens(c *Config)                    { c.AllowBasicAuthTokens = true }

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return value
}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mintAdmin(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs(basic("admin", "pw")), body)
	wantStatus(t, rec, http.StatusOK)
	return decodeObject(t, rec.Body.String())
}

func seedTokens(t *testing.T, db store.Store) (*token.Service, map[string]string) {
	t.Helper()
	if err := db.Instance().
		Init(t.Context(), types.InstanceIdentity{InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	svc, err := token.New(t.Context(), db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return svc, map[string]string{}
}

// terraformServer exercises the production router with Terraform-specific
// repository and signing configuration. Each suite supplies its own grants.
func terraformServer(
	t *testing.T,
	tune ...func(*Config),
) (http.Handler, *inmem.Store, *token.Service) {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(t.Context(), types.User{Name: "user", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Repos().
		Create(t.Context(), *repo.Defaults(&terraformFixtureRepos[0])); err != nil {
		t.Fatal(err)
	}
	tokens, _ := seedTokens(t, db)
	a, err := auth.NewAuthenticator(
		db.Users(),
		db.Settings(),
		auth.Options{Hash: testHash, Tokens: tokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0",
		Store:         db,
		Blobs:         blobs,
		Auth:          a,
		Tokens:        tokens,
		PasswordHash:  testHash,
	}
	tuneRegistry(&cfg)
	for _, fn := range tune {
		fn(&cfg)
	}
	return server.New(cfg), db, tokens
}
