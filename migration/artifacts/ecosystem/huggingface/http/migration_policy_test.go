package huggingfacehttp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	huggingfacehttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/huggingface/http"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// migrationPolicyWorld is a local repository holding one legacy model
// revision, with the access module bound over its store, and the faults its
// router kit has recorded.
type migrationPolicyWorld struct {
	f      *fixture
	access accessapi.API
	faults []error
}

func newMigrationPolicyWorld(t *testing.T) *migrationPolicyWorld {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For}
	if err = db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, items: items}
	f.legacyRevision("models", "1700000000", sha, false)
	return &migrationPolicyWorld{f: f, access: accesstest.Over(t, db).API()}
}

// migrate posts, as the administrator, the migration of the legacy revision
// with ref main, mounting the protocol with grants as its Grants. The router
// is served in the test's goroutine, so the faults it records need no lock.
func (w *migrationPolicyWorld) migrate(
	t *testing.T,
	grants accessapi.Grants,
	dry bool,
) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	pkgroute.Mount(router, pkgroute.Options{
		Store: w.f.db, Items: func() *item.Service { return w.f.items },
		ItemError: func(_ context.Context, _ string, err error) error { return err },
		Fault:     func(_ *http.Request, err error) { w.faults = append(w.faults, err) },
	}, huggingfacehttp.Protocol(huggingfacehttp.Options{Grants: grants}))
	p, d, err := w.access.Authorizer.Authorize(
		t.Context(),
		kernel.Principal{Name: "admin", Admin: true},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(
		http.MethodPost,
		"/artifactory/api/huggingfaceml/hf/"+migrationEndpoint,
		bytes.NewReader(migrationBody("models", "1700000000", sha, "main", dry)),
	)
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	out := httptest.NewRecorder()
	router.ServeHTTP(out, r)
	return out
}

// expectRefusal fails t unless out is status with the Artifactory envelope
// whose message is message.
func expectRefusal(t *testing.T, out *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	want := fmt.Sprintf("{\"errors\":[{\"message\":%q,\"status\":%d}]}\n", message, status)
	if out.Code != status || out.Body.String() != want {
		t.Fatalf("migration: %d %s, want %d %s", out.Code, out.Body, status, want)
	}
}

// The scan reads at most 1024 permissions, the bound the migration passes
// the access module: a store holding exactly that many is scanned, and one
// more refuses the migration, whichever permission would have changed access.
func TestAccLegacyMigrationRefusesPermissionScanPastTheLimit(t *testing.T) {
	w := newMigrationPolicyWorld(t)
	for n := range 1024 {
		if err := w.f.db.Permissions().
			Create(t.Context(), types.Permission{Name: fmt.Sprintf("%04d", n)}); err != nil {
			t.Fatal(err)
		}
	}
	if out := w.migrate(t, w.access.Grants, true); out.Code != http.StatusOK {
		t.Fatalf("migration within the scan limit: %d %s", out.Code, out.Body)
	}
	if err := w.f.db.Permissions().Create(t.Context(), types.Permission{Name: "1024"}); err != nil {
		t.Fatal(err)
	}
	expectRefusal(
		t,
		w.migrate(t, w.access.Grants, false),
		http.StatusRequestEntityTooLarge,
		"migration permission scan exceeds 1024 permissions",
	)
	if len(w.faults) != 0 {
		t.Fatalf("a refused scan was recorded as a fault: %v", w.faults)
	}
}

// The first permission whose targets the migration would change is named in
// the conflict, and the scan is asked about the repository, its class, every
// moved pair (the model's folder, the revision's folder, each file and the
// ref) and the 1024-permission bound.
func TestAccLegacyMigrationNamesTheFirstChangingPermission(t *testing.T) {
	w := newMigrationPolicyWorld(t)
	type question struct {
		repoKey, class string
		pairs          []accessapi.PathPair
		limit          int
	}
	var asked []question
	grants := &accessapitest.Grants{AccessChangeFunc: func(
		_ context.Context,
		repoKey, class string,
		pairs []accessapi.PathPair,
		limit int,
	) (string, error) {
		asked = append(asked, question{repoKey, class, pairs, limit})
		return "first-change", nil
	}}
	expectRefusal(
		t,
		w.migrate(t, grants, false),
		http.StatusConflict,
		"permission target changes access after migration: first-change",
	)
	source := "models/acme/demo/main/1700000000"
	snapshot := "models--acme--demo/snapshots/" + sha
	pairs := []accessapi.PathPair{
		{Source: "models/acme/demo", Destination: "models--acme--demo", Folder: true},
		{Source: source, Destination: snapshot, Folder: true},
		{Source: source + "/config.json", Destination: snapshot + "/config.json"},
		{Source: source + "/sub/tokenizer.json", Destination: snapshot + "/sub/tokenizer.json"},
		{Source: source, Destination: "models--acme--demo/refs/main"},
	}
	if len(asked) != 1 || asked[0].repoKey != "hf" || asked[0].class != "local" ||
		asked[0].limit != 1024 || !slices.Equal(asked[0].pairs, pairs) {
		t.Fatalf("scan asked %+v, want once for hf, local, %+v and 1024", asked, pairs)
	}
	published, _, err := w.f.db.Items().
		Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
	if err != nil || len(published) != 0 {
		t.Fatalf("refused migration published %v: %v", published, err)
	}
}

// A scan that fails for any other reason is a fault, wrapped with what the
// migration was reading, and answers 500 without a detail.
func TestAccLegacyMigrationWrapsPermissionScanFailures(t *testing.T) {
	w := newMigrationPolicyWorld(t)
	injected := errors.New("injected permission scan failure")
	grants := &accessapitest.Grants{AccessChangeFunc: func(
		context.Context, string, string, []accessapi.PathPair, int,
	) (string, error) {
		return "", injected
	}}
	expectRefusal(t, w.migrate(t, grants, false), http.StatusInternalServerError, "internal error")
	if len(w.faults) != 1 || !errors.Is(w.faults[0], injected) ||
		w.faults[0].Error() != "huggingface: migration permissions: "+injected.Error() {
		t.Fatalf("faults %v, want the scan failure wrapped once", w.faults)
	}
}
