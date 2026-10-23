// Package agentcommon provides a production-router fixture for Agent Packages
// and Agent Plugins server-route suites.
package agentcommon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type World struct {
	T       *testing.T
	Handler http.Handler
	DB      *inmem.Store
	Blobs   blob.Store
}

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
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: support.TestHash})
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: blobs, Auth: a,
	})
	return &World{T: t, Handler: h, DB: db, Blobs: blobs}
}

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

func WantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func SetTrashcan(t *testing.T, db store.Store, on bool) {
	t.Helper()
	if err := db.Settings().Put(t.Context(), types.SettingTrashcan,
		types.TrashcanSettings{Enabled: on, RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
}

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
