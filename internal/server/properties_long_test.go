package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The long-property tests run the routes over the in-memory store and over
// PostgreSQL, whose B-tree entries are what a long key or value overflows;
// the PostgreSQL runs skip without Docker.

func TestLongPropertyValues(t *testing.T) { testLongPropertyValues(t, inmem.New()) }

func TestLongPropertyValuesOnPostgres(t *testing.T) { testLongPropertyValues(t, openPostgres(t)) }

func TestLongPropertyKeys(t *testing.T) { testLongPropertyKeys(t, inmem.New()) }

func TestLongPropertyKeysOnPostgres(t *testing.T) { testLongPropertyKeys(t, openPostgres(t)) }

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
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
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

// testLongPropertyKeys sets keys of types.PropertyKeyMax characters through
// every route that sets properties, and refuses one more character on each
// with a 400 that leaves everything as it was. "é" is two bytes of UTF-8,
// so the limit counts characters, not bytes.
func testLongPropertyKeys(t *testing.T, db store.Store) {
	ctx := context.Background()
	h := propertyServer(t, db)
	key := strings.Repeat("é", types.PropertyKeyMax)
	long := strings.Repeat("é", types.PropertyKeyMax+1)
	call := func(method, path, body string, status int) {
		t.Helper()
		headers := basic("admin", "pw")
		if strings.HasPrefix(path, buildsPath) {
			headers["Content-Type"] = vendorJSON
		}
		rec := do(t, h, method, path, headers, body)
		if rec.Code != status {
			t.Fatalf("%s %.80s: %d %.200s, want %d", method, path, rec.Code, rec.Body, status)
		}
	}
	props := func() types.Properties {
		t.Helper()
		it, err := db.Items().Get(ctx, "libs-local", false, "a.txt")
		if err != nil {
			t.Fatal(err)
		}
		p, err := db.Items().Properties(ctx, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Matrix parameters on deploy.
	call(
		http.MethodPut,
		"/artifactory/libs-local/b.txt;"+url.PathEscape(long)+"=v",
		"x",
		http.StatusBadRequest,
	)
	if _, err := db.Items().
		Get(ctx, "libs-local", false, "b.txt"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("a refused deploy stored its file: %v", err)
	}
	call(
		http.MethodPut,
		"/artifactory/libs-local/a.txt;"+url.PathEscape(key)+"=1",
		"x",
		http.StatusCreated,
	)

	// The properties API and the metadata API.
	call(http.MethodPut, "/artifactory/api/storage/libs-local/a.txt?properties="+
		url.QueryEscape("ok=2;"+long+"=2"), "", http.StatusBadRequest)
	call(http.MethodPatch, "/artifactory/api/metadata/libs-local/a.txt",
		fmt.Sprintf(`{"props":{"ok":"3",%q:"3"}}`, long), http.StatusBadRequest)
	want := types.Properties{key: {"1"}}
	if diff := cmp.Diff(want, props()); diff != "" {
		t.Fatalf("refused property writes changed the item (-want +got):\n%s", diff)
	}
	call(http.MethodPatch, "/artifactory/api/metadata/libs-local/a.txt",
		fmt.Sprintf(`{"props":{%q:"2"}}`, key), http.StatusNoContent)

	// AQL properties.update, which only changes keys an item has.
	call(http.MethodPost, aqlURL, fmt.Sprintf(
		`properties.update({"item.repo":"libs-local"}).keys(%q).newValue("4").dryRun("false")`,
		long,
	),
		http.StatusBadRequest)
	call(http.MethodPost, aqlURL, fmt.Sprintf(
		`properties.update({"item.repo":"libs-local"}).keys(%q).newValue("3").dryRun("false")`,
		key,
	),
		http.StatusOK)
	if diff := cmp.Diff(types.Properties{key: {"3"}}, props()); diff != "" {
		t.Fatalf("properties after the AQL update (-want +got):\n%s", diff)
	}

	// Build-info, at the build and module levels, and promotion.
	doc := func(number, buildKey, moduleKey string) string {
		return fmt.Sprintf(`{"name":"app","number":%q,"started":%q,"properties":{%q:"v"},`+
			`"modules":[{"id":"m","properties":{%q:"w"}}]}`, number, startedAt(0), buildKey, moduleKey)
	}
	call(http.MethodPut, buildsPath, doc("1", long, "m"), http.StatusBadRequest)
	call(http.MethodPut, buildsPath, doc("1", "b", long), http.StatusBadRequest)
	if _, err := db.Builds().Latest(ctx, "", "app"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused build-info document was stored: %v", err)
	}
	call(http.MethodPut, buildsPath, doc("1", key, key), http.StatusNoContent)
	run, err := db.Builds().Latest(ctx, "", "app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.Properties[key], []string{"v"}) ||
		!slices.Equal(run.Modules[0].Properties[key], []string{"w"}) {
		t.Fatalf(
			"build properties %v, module properties %v",
			run.Properties,
			run.Modules[0].Properties,
		)
	}
	call(http.MethodPost, buildsPath+"/promote/app/1",
		fmt.Sprintf(`{"status":"released","properties":{%q:["v"]}}`, long), http.StatusBadRequest)
	if diff := cmp.Diff(types.Properties{key: {"3"}}, props()); diff != "" {
		t.Fatalf("a refused promotion changed the item (-want +got):\n%s", diff)
	}

	// A long key that reached a store would still answer 400.
	err = artifactoryService{}.itemError(ctx, "set properties",
		fmt.Errorf("%w: %w", store.ErrInvalid, types.ErrPropertyKeyTooLong))
	var api *apierror.Error
	if !errors.As(err, &api) || api.Status != http.StatusBadRequest {
		t.Fatalf("itemError = %v", err)
	}
}
