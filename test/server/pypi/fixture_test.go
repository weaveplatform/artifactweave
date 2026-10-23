package pypi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db store.Store)        { t.Helper(); support.Seed(t, db) }
func basic(user, password string) map[string]string { return support.Basic(user, password) }
func bearerAuth(value string) map[string]string     { return support.Bearer(value) }
func contentTypeOf(body string) (string, string) {
	if form, ok := strings.CutPrefix(body, "multipart:"); ok {
		boundary, payload, _ := strings.Cut(form, "\n")
		return "multipart/form-data; boundary=" + boundary, payload
	}
	return "application/json", body
}

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return result
}

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

func fixtureRepos(name string, kind repo.PackageType) []repo.Config {
	local := name + "-local"
	return []repo.Config{
		{Key: local, Class: repo.Local, PackageType: kind},
		{Key: name + "-remote", Class: repo.Remote, PackageType: kind, URL: "https://up.example"},
		{
			Key: name + "-virtual", Class: repo.Virtual, PackageType: kind,
			Repositories: []string{local}, DefaultDeploymentRepo: local,
		},
	}
}

// pypiServer assembles the real router with only the Pypi repository needed by
// this package. Each matrix/status fixture adds its own permission model.
func pypiServer(
	t *testing.T,
	tune ...func(*server.Config),
) (http.Handler, *inmem.Store, *token.Service) {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(t.Context(), types.User{Name: "user", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&pypiRepos[0])); err != nil {
		t.Fatal(err)
	}
	if err := db.Instance().Init(t.Context(), types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(t.Context(), db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a,
		Tokens: tokens, PasswordHash: testHash,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return server.New(cfg), db, tokens
}

func statusFixture(t *testing.T, anonymous bool, tune ...func(*server.Config)) http.Handler {
	t.Helper()
	h, _, _ := pypiServer(t, func(c *server.Config) {
		if pypiStatusPart.tune != nil {
			pypiStatusPart.tune(c)
		}
		pypiStatusPart.seed(t, c.Store, c.Blobs)
		if anonymous {
			if err := c.Store.Settings().
				Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, fn := range tune {
			fn(c)
		}
	})
	return h
}

func allowBasicTokens(c *server.Config) { c.AllowBasicAuthTokens = true }

func mintAdmin(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	headers := basic("admin", "pw")
	headers["Content-Type"] = "application/json"
	rec := do(t, h, http.MethodPost, "/access/api/v1/tokens", headers, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body)
	}
	return decodeObject(t, rec.Body.String())
}
