package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestLongPropertyValuesOnPostgres sets property values of
// types.PropertyValueMax characters through matrix parameters and the
// properties API, and one past it, on PostgreSQL, whose B-tree entries are
// far shorter: every request succeeds, and the longer value reads back cut.
func TestLongPropertyValuesOnPostgres(t *testing.T) {
	ctx := context.Background()
	db, err := postgres.Open(ctx, sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqltest.Reset(t, db.DB(), postgres.Dialect)
	cheapSeed(t, db)
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{BaseURL: "https://reg.example", Store: db, Blobs: blobs, Auth: a})
	admin := basic("admin", "pw")
	call := func(method, path string) *http.Response {
		t.Helper()
		return do(t, h, method, path, admin, "x").Result()
	}

	text := storetest.LongText(types.PropertyValueMax + 100)
	notes := string([]rune(text)[:types.PropertyValueMax])
	if r := call(
		http.MethodPut,
		"/artifactory/libs-local/a.txt;notes="+url.PathEscape(notes),
	); r.StatusCode != http.StatusCreated {
		t.Fatalf("deploy with a matrix parameter: %s", r.Status)
	}
	if r := call(
		http.MethodPut,
		"/artifactory/api/storage/libs-local/a.txt?properties="+url.QueryEscape(
			"long="+text,
		),
	); r.StatusCode != http.StatusNoContent {
		t.Fatalf("set properties: %s", r.Status)
	}
	var got struct {
		Properties map[string][]string `json:"properties"`
	}
	r := call(http.MethodGet, "/artifactory/api/storage/libs-local/a.txt?properties")
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Properties["notes"], []string{notes}) ||
		!slices.Equal(got.Properties["long"], []string{notes}) {
		t.Fatalf(
			"properties read back %d and %d values",
			len(got.Properties["notes"]),
			len(got.Properties["long"]),
		)
	}
	// The property search finds the item by the whole value.
	var found struct {
		Results []struct {
			URI string `json:"uri"`
		} `json:"results"`
	}
	r = call(http.MethodGet, "/artifactory/api/search/prop?notes="+url.QueryEscape(notes))
	if err := json.NewDecoder(r.Body).Decode(&found); err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 {
		t.Fatalf("property search found %d items", len(found.Results))
	}
}
