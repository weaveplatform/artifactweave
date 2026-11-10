package maven_test

import (
	"crypto/sha1" //nolint:gosec // Maven wire checksum
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type itemWorld struct {
	t  *testing.T
	h  http.Handler
	db *inmem.Store
}

// newItemWorld retains the reader and deployer principals used by the Maven
// server-assembly cases, while each case adds only its own Maven repositories.
func newItemWorld(t *testing.T, anonymous bool, tune ...harness.Tune) *itemWorld {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	for _, user := range []types.User{
		{Name: "reader", PasswordHash: cheapHash, Groups: []string{authn.ReadersGroup}},
		{Name: "deployer", PasswordHash: cheapHash},
		{Name: "user", PasswordHash: cheapHash},
	} {
		if err := db.Users().Create(t.Context(), user); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(t.Context(), types.Permission{
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
		if err := db.Settings().Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		BaseURL:       "https://reg.example",
		CompatVersion: "7.155.0",
		Store:         db,
		Blobs:         blobs,
		Auth:          a,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return &itemWorld{t: t, h: harness.Router(cfg), db: db}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	h := map[string]string{}
	if as != "" {
		h = basic(as, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return do(w.t, w.h, method, path, h, body)
}

func (w *itemWorld) put(path, body string) {
	w.t.Helper()
	wantStatus(w.t, w.do(http.MethodPut, path, "admin", body), http.StatusCreated)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func sha1Hex(body string) string {
	sum := sha1.Sum([]byte(body)) //nolint:gosec // Maven wire checksum
	return hex.EncodeToString(sum[:])
}
