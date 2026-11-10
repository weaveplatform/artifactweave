package agentcommon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// World is the production router over an in-memory store and a file-system
// blob store, built by NewWorld for one test. Do sends requests to Handler;
// DB and Blobs let a test seed state or check what a request left behind.
type World struct {
	T       *testing.T
	Handler http.Handler
	DB      *inmem.Store
	Blobs   blob.Store
}

// NewWorld builds a World for t: a seeded store with the users deployer and
// user, both with the password "pw", and the generic local repository
// libs-local; blobs under t's temporary directory; and the router with Basic
// authentication. It fails t when any of them cannot be built.
func NewWorld(t *testing.T) *World {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	for _, name := range []string{"deployer", "user"} {
		if err := db.Users().Create(t.Context(), types.User{
			Name: name, PasswordHash: support.CheapHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: support.TestHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := harness.Router(harness.Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: blobs, Auth: a,
	})
	return &World{T: t, Handler: h, DB: db, Blobs: blobs}
}

// Do sends a request to the world's router as the user as, with the password
// "pw" over Basic, or without credentials when as is empty. headers are name
// and value pairs; a trailing name without a value is ignored.
func (w *World) Do(method, path, as, body string, headers ...string) *httptest.ResponseRecorder {
	w.T.Helper()
	h := map[string]string{}
	if as != "" {
		h = support.Basic(as, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return support.Do(w.T, w.Handler, method, path, h, body)
}

// WantStatus fails t, printing the response body, unless rec has the status
// want.
func WantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

// SetTrashcan turns the trash can on or off in db, with a 14-day retention,
// and fails t when the setting cannot be stored.
func SetTrashcan(t *testing.T, db store.Store, on bool) {
	t.Helper()
	if err := db.Settings().Put(t.Context(), types.SettingTrashcan,
		types.TrashcanSettings{Enabled: on, RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
}

// HitsOf returns the results array of a search response. It fails t unless
// rec is 200 OK with a JSON body that has a results array.
func HitsOf(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	WantStatus(t, rec, http.StatusOK)
	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Results == nil {
		t.Fatalf("invalid search results: %s: %v", rec.Body, err)
	}
	return body.Results
}
