package itemkit_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccHostedAdmissionAcrossMigration(t *testing.T) {
	for _, test := range []struct {
		name                    string
		preparedFirst, redirect bool
	}{
		{name: "stale local streamed response", preparedFirst: true},
		{name: "stale local redirect", preparedFirst: true, redirect: true},
		{name: "local response already admitted"},
		{name: "local redirect already admitted", redirect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			ctx := t.Context()
			configuration := w.Local
			if test.redirect {
				configuration.DownloadRedirect = repo.Flex(true)
				storekit.Must(t, w.Store.Repos().Update(ctx, configuration))
			}
			const payload = "migration payload"
			it := w.Put(t, configuration, false, "package.tgz", []byte(payload))
			pkg := storekit.Get[model.Package](t)(w.Store.Packages().Ensure(ctx, model.Package{RepoKey: configuration.Key, Type: configuration.PackageType, Name: "package"}))
			version := storekit.Get[model.PackageVersion](t)(w.Store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: "1"}))
			storekit.Must(t, w.Store.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: it.ID}))
			exclusions := w.Store.CleanupExclusions()
			var ref api.CleanupExclusion
			migrate := func() {
				t.Helper()
				current := configuration
				current.Class = repo.Federated
				storekit.Must(t, w.Store.Repos().Update(ctx, current))
				metadata := pkgindex.NewCleanupMetadata(w.Store)
				repository := storekit.Get[api.CleanupRepository](t)(metadata.Repository(ctx, current.Key))
				ref = api.CleanupExclusion{
					ID:          "migration-response",
					Owner:       "coordinator",
					Repository:  current.Key,
					Incarnation: repository.Incarnation,
					Generation:  repository.Generation,
					Versions:    []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: version.Version}, ID: version.ID}},
				}
			}
			response := &admissionWriter{ResponseRecorder: httptest.NewRecorder(), before: func() {}}
			if test.preparedFirst {
				migrate()
				storekit.Must(t, exclusions.Prepare(ctx, ref))
			} else {
				response.before = func() {
					migrate()
					if err := exclusions.Prepare(ctx, ref); !errors.Is(err, api.ErrCleanupExcluded) {
						t.Fatalf("in-flight local response allowed federated cleanup: %v", err)
					}
				}
			}
			deps := itemkit.Deps{}
			if test.redirect {
				deps.Blobs = admissionSigner{w.Blobs}
			}
			request := kittest.Request(t, http.MethodGet, "/artifactory/"+configuration.Key+"/package.tgz?skipUpdateStats=true")
			// Found deliberately retains the configuration resolved before migration.
			w.Kit(deps).ServeItemFile(response, request, w.Store, itemkit.Found{Repo: configuration, Item: it}, kittest.Admin)
			if test.preparedFirst {
				if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), payload) || response.Header().Get("Location") != "" {
					t.Fatalf("stale local response bypassed exclusion: status=%d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
				}
			} else {
				want := http.StatusOK
				if test.redirect {
					want = http.StatusFound
				}
				if !response.observed || response.Code != want {
					t.Fatalf("admitted response status=%d, observed=%v", response.Code, response.observed)
				}
				if !test.redirect && response.Body.String() != payload {
					t.Fatalf("admitted content changed: %q", response.Body.String())
				}
				// Completion must remove the pre-migration pin without adopting its old generation.
				storekit.Must(t, exclusions.Prepare(ctx, ref))
			}
			stats := storekit.Get[model.ItemStats](t)(w.Store.Items().Stats(ctx, it.ID))
			if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
				t.Fatal("migration admission changed skipped public download statistics")
			}
		})
	}
}
