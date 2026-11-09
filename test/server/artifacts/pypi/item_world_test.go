package pypi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type itemWorld struct {
	t  *testing.T
	h  http.Handler
	db *inmem.Store
}

// newItemWorld retains the reader and deployer principals used by the Pypi
// server-assembly cases, while each case adds only its own Pypi repositories.
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
	if err := db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "other-local", Class: repo.Local})); err != nil {
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

func (w *itemWorld) item(key string, cached bool, path string) types.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(w.t.Context(), key, cached, path)
	if err != nil {
		w.t.Fatalf("item %s/%s: %v", key, path, err)
	}
	return it
}

func grant(t *testing.T, w *itemWorld, name, user, repoKey string, actions types.Actions) {
	t.Helper()
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: name,
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{user: actions}},
			Targets: map[string]types.Target{repoKey: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(w, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
