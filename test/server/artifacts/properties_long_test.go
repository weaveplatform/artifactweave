package artifacts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The long-property tests run the routes over the in-memory store and over
// PostgreSQL, whose B-tree entries are what a long key or value overflows;
// the PostgreSQL runs skip without Docker.

func TestAccLongPropertyValues(t *testing.T) { testLongPropertyValues(t, inmem.New()) }

func TestAccLongPropertyValuesOnPostgres(
	t *testing.T,
) {
	testLongPropertyValues(t, openPostgres(t))
}

func openPostgres(t *testing.T) store.Store {
	t.Helper()
	db, err := postgres.Open(context.Background(), sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqltest.Reset(t, db.DB(), postgres.Dialect)
	return db
}

// propertyServer is the router over db, with libs-local and the seeded
// admin, whose password is "pw".
func propertyServer(t *testing.T, db store.Store) http.Handler {
	t.Helper()
	ctx := context.Background()
	cheapSeed(t, db)
	libs := repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})
	if err := db.Repos().Create(ctx, *libs); err != nil {
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
	return New(Config{BaseURL: "https://reg.example", Store: db, Blobs: blobs, Auth: a})
}

// testLongPropertyValues sets property values of types.PropertyValueMax
// characters through matrix parameters and the properties API, and one past
// it: every request succeeds, and the longer value reads back cut.
func testLongPropertyValues(t *testing.T, db store.Store) {
	h := propertyServer(t, db)
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
