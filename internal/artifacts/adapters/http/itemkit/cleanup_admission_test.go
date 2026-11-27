package itemkit_test

import (
	"context"
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
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type admissionWriter struct {
	*httptest.ResponseRecorder
	before   func()
	observed bool
}

func (w *admissionWriter) WriteHeader(status int) {
	if !w.observed {
		w.observed = true
		w.before()
	}
	w.ResponseRecorder.WriteHeader(status)
}

func (w *admissionWriter) Write(data []byte) (int, error) {
	if !w.observed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseRecorder.Write(data)
}

type admissionSigner struct{ blob.Store }

func (admissionSigner) Signs(blob.Via) bool { return true }
func (admissionSigner) SignURL(context.Context, string, blob.SignOptions) (string, error) {
	return "https://storage.example/private-object", nil
}

type admissionBoundary struct {
	api.CleanupExclusions
	before    func(context.Context)
	after     func(context.Context)
	failure   error
	admitted  api.CleanupAdmission
	completed []api.CleanupAdmission
}

func (a *admissionBoundary) Admit(ctx context.Context, key string, id int64) (api.CleanupAdmission, error) {
	if a.before != nil {
		a.before(ctx)
	}
	if a.failure != nil {
		return api.CleanupAdmission{}, a.failure
	}
	admitted, err := a.CleanupExclusions.Admit(ctx, key, id)
	if err != nil {
		return api.CleanupAdmission{}, err
	}
	a.admitted = admitted
	if a.after != nil {
		a.after(ctx)
	}
	return admitted, nil
}

func (a *admissionBoundary) Complete(ctx context.Context, admitted api.CleanupAdmission) error {
	a.completed = append(a.completed, admitted)
	return a.CleanupExclusions.Complete(ctx, admitted)
}

type admissionBoundaryStore struct {
	itemkit.Store
	exclusions api.CleanupExclusions
}

func (s admissionBoundaryStore) CleanupExclusions() api.CleanupExclusions { return s.exclusions }

// An ordinary replicated delete can land before admission or after its durable
// transaction, before Begin's item recheck. Neither case is a cleanup outage.
func TestUnitHostedAdmissionConcurrentDeletion(t *testing.T) {
	for _, test := range []struct {
		name                   string
		before, after, barrier bool
		failure                error
		status                 int
	}{
		{name: "deleted_before_admission", before: true, status: http.StatusNotFound},
		{name: "deleted_after_admission", after: true, status: http.StatusNotFound},
		{name: "held_barrier", barrier: true, status: http.StatusServiceUnavailable},
		{name: "metadata_failure", failure: kernel.ErrStore, status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			ctx := t.Context()
			configuration := w.Federated
			target := w.Put(t, configuration, false, "selected.txt", []byte("selected fixture payload"))
			guard := w.Put(t, configuration, false, "indexed.txt", []byte("unrelated indexed payload"))
			pkg := storekit.Get[model.Package](t)(w.Store.Packages().Ensure(ctx, model.Package{RepoKey: configuration.Key, Type: configuration.PackageType, Name: "guard"}))
			version := storekit.Get[model.PackageVersion](t)(w.Store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: "1"}))
			storekit.Must(t, w.Store.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: guard.ID}))
			repository := storekit.Get[api.CleanupRepository](t)(pkgindex.NewCleanupMetadata(w.Store).Repository(ctx, configuration.Key))
			ref := api.CleanupExclusion{
				ID: "concurrent-delete", Owner: "coordinator", Repository: configuration.Key,
				Incarnation: repository.Incarnation, Generation: repository.Generation,
				Versions: []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: version.Version}, ID: version.ID}},
			}
			exclusions := w.Store.CleanupExclusions()
			boundary := &admissionBoundary{CleanupExclusions: exclusions, failure: test.failure}
			remove := func(ctx context.Context) {
				_, err := w.Store.Items().Delete(ctx, configuration.Key, false, target.FullPath())
				storekit.Must(t, err)
			}
			if test.before {
				boundary.before = remove
			}
			if test.after {
				boundary.after = func(ctx context.Context) {
					if !errors.Is(exclusions.Prepare(ctx, ref), api.ErrCleanupExcluded) {
						t.Fatal("real admission did not hold cleanup before deletion")
					}
					remove(ctx)
					if !errors.Is(exclusions.Prepare(ctx, ref), api.ErrCleanupExcluded) {
						t.Fatal("deleting an item erased its live admission")
					}
				}
			}
			if test.barrier {
				storekit.Must(t, exclusions.Prepare(ctx, ref))
			}
			st := admissionBoundaryStore{Store: w.Store, exclusions: boundary}
			response := httptest.NewRecorder()
			w.Kit(itemkit.Deps{Store: st}).GetItem(response, kittest.Request(t, http.MethodGet, "/artifactory/"+configuration.Key+"/selected.txt"))
			if test.before || test.after {
				_, err := w.Store.Items().GetByID(ctx, target.ID)
				if !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("controlled deletion did not remove the selected identity")
				}
			}
			if test.after {
				if boundary.admitted.ID == "" || len(boundary.completed) != 1 || boundary.completed[0] != boundary.admitted {
					t.Fatal("failed recheck did not complete its exact durable admission")
				}
			} else if len(boundary.completed) != 0 || boundary.admitted.ID != "" {
				t.Fatal("refused admission unexpectedly acquired a durable token")
			}
			if test.barrier {
				storekit.Must(t, exclusions.Guard(ctx, ref))
			} else {
				// The unrelated indexed version proves completion removed the real
				// repository-wide pin, even though the selected item is gone.
				storekit.Must(t, exclusions.Prepare(ctx, ref))
			}
			if strings.Contains(response.Body.String(), "selected fixture payload") {
				t.Fatal("refused response served the selected bytes")
			}
			if response.Code != test.status {
				t.Fatalf("concurrent-deletion response status%d want%d (bytes withheld)", response.Code, test.status)
			}
		})
	}
}

func TestUnitHostedAdmissionBeforeResponse(t *testing.T) {
	for _, test := range []struct {
		name, method, query, rangeHeader    string
		redirect, virtual, cancel, metadata bool
		status                              int
	}{
		{name: "GET", method: http.MethodGet, status: 200},
		{name: "statistics skipped", method: http.MethodGet, query: "?skipUpdateStats=true", status: 200},
		{name: "nonfirst range", method: http.MethodGet, rangeHeader: "bytes=2-4", status: 206},
		{name: "cancelled response", method: http.MethodGet, query: "?skipUpdateStats=true", cancel: true, status: 200},
		{name: "member redirect", method: http.MethodGet, redirect: true, status: 302},
		{name: "virtual physical member", method: http.MethodGet, virtual: true, status: 200},
		{name: "HEAD has no admission", method: http.MethodHead, metadata: true, status: 200},
		{name: "304 has no admission", method: http.MethodGet, query: "?skipUpdateStats=true", metadata: true, status: 304},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			ctx := t.Context()
			configuration := w.Federated
			if test.redirect {
				configuration.DownloadRedirect = repo.Flex(true)
				storekit.Must(t, w.Store.Repos().Update(ctx, configuration))
			}
			it := w.Put(t, configuration, false, "package.tgz", []byte("hosted response"))
			pkg := storekit.Get[model.Package](t)(w.Store.Packages().Ensure(ctx, model.Package{RepoKey: configuration.Key, Type: configuration.PackageType, Name: "package"}))
			version := storekit.Get[model.PackageVersion](t)(w.Store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: "1"}))
			storekit.Must(t, w.Store.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: it.ID}))
			metadata := pkgindex.NewCleanupMetadata(w.Store)
			repository := storekit.Get[api.CleanupRepository](t)(metadata.Repository(ctx, configuration.Key))
			ref := api.CleanupExclusion{
				ID:          "response",
				Owner:       "coordinator",
				Repository:  configuration.Key,
				Incarnation: repository.Incarnation,
				Generation:  repository.Generation,
				Versions:    []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: version.Version}, ID: version.ID}},
			}
			exclusions := w.Store.CleanupExclusions()
			request := kittest.Request(t, test.method, "/artifactory/"+configuration.Key+"/package.tgz"+test.query)
			if test.rangeHeader != "" {
				request.Header.Set("Range", test.rangeHeader)
			}
			if test.status == http.StatusNotModified {
				request.Header.Set("If-None-Match", it.Actual.SHA1)
			}
			responseCtx, cancel := context.WithCancel(request.Context())
			defer cancel()
			request = request.WithContext(responseCtx)
			response := &admissionWriter{ResponseRecorder: httptest.NewRecorder(), before: func() {
				err := exclusions.Prepare(ctx, ref)
				if test.metadata {
					storekit.Must(t, err)
				} else if !errors.Is(err, api.ErrCleanupExcluded) {
					t.Fatalf("response header preceded durable admission: %v", err)
				}
				if test.cancel {
					cancel()
				}
			}}
			deps := itemkit.Deps{}
			if test.redirect {
				deps.Blobs = admissionSigner{w.Blobs}
			}
			kit := w.Kit(deps)
			if test.virtual {
				virtual := w.Virtual
				virtual.Repositories = []string{configuration.Key}
				storekit.Must(t, w.Store.Repos().Update(ctx, virtual))
				request.URL.Path = "/artifactory/" + virtual.Key + "/package.tgz"
				kit.GetItem(response, request)
			} else {
				kit.ServeItemFile(response, request, w.Store, itemkit.Found{Repo: configuration, Item: it}, kittest.Admin)
			}
			if !response.observed || response.Code != test.status {
				t.Fatalf("response %d, observed=%v body=%s", response.Code, response.observed, response.Body.String())
			}
			// Completion must run after all bytes/redirect even when the HTTP request
			// was cancelled. It never needs the caller to retry a private token.
			if !test.metadata {
				storekit.Must(t, exclusions.Prepare(ctx, ref))
			}
			stats := storekit.Get[model.ItemStats](t)(w.Store.Items().Stats(ctx, it.ID))
			wantCount := int64(1)
			if test.metadata || test.query != "" || test.rangeHeader != "" {
				wantCount = 0
			}
			if stats.DownloadCount != wantCount {
				t.Fatalf("private admission changed public counts: %d, want %d", stats.DownloadCount, wantCount)
			}
		})
	}
}

// A checked public-export snapshot is not reopened after a same-ID overwrite.
// Its durable admission still excludes cleanup of another indexed version,
// including when the original local repository migrates before admission.
func TestUnitHostedSelectedSnapshotExclusion(t *testing.T) {
	key, err := terraformtest.NewKey()
	storekit.Must(t, err)
	for _, test := range []struct {
		name                  string
		migrate, barrierFirst bool
	}{
		{name: "federated_admission_wins"},
		{name: "migrated_admission_wins", migrate: true},
		{name: "federated_barrier_wins", barrierFirst: true},
		{name: "migrated_barrier_wins", migrate: true, barrierFirst: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			ctx := t.Context()
			configuration := *repo.Defaults(&repo.Config{Key: "rpm-snapshot", Class: repo.Federated, PackageType: repo.RPM})
			if test.migrate {
				configuration.Class = repo.Local
			}
			storekit.Must(t, w.Store.Repos().Create(ctx, configuration))
			original := w.Put(t, configuration, false, "repodata/repomd.xml.key", []byte(key.Public))
			payload := w.Put(t, configuration, false, "package.rpm", []byte("indexed fixture"))
			pkg := storekit.Get[model.Package](t)(w.Store.Packages().Ensure(ctx, model.Package{RepoKey: configuration.Key, Type: repo.RPM, Name: "package"}))
			version := storekit.Get[model.PackageVersion](t)(w.Store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: "1"}))
			storekit.Must(t, w.Store.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: payload.ID}))
			exclusions := w.Store.CleanupExclusions()
			var ref api.CleanupExclusion
			blobs := &publicExportBlobs{Store: w.Blobs}
			blobs.afterOpen = func() {
				w.Put(t, configuration, false, "repodata/repomd.xml.key", []byte(key.Private))
				current := configuration
				if test.migrate {
					current.Class = repo.Federated
					storekit.Must(t, w.Store.Repos().Update(ctx, current))
				}
				repository := storekit.Get[api.CleanupRepository](t)(pkgindex.NewCleanupMetadata(w.Store).Repository(ctx, current.Key))
				ref = api.CleanupExclusion{
					ID: "selected-snapshot", Owner: "coordinator", Repository: current.Key,
					Incarnation: repository.Incarnation, Generation: repository.Generation,
					Versions: []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: version.Version}, ID: version.ID}},
				}
				if test.barrierFirst {
					storekit.Must(t, exclusions.Prepare(ctx, ref))
				}
			}
			var duringResponse error
			response := &admissionWriter{ResponseRecorder: httptest.NewRecorder(), before: func() {
				if !test.barrierFirst {
					duringResponse = exclusions.Prepare(ctx, ref)
				}
			}}
			w.Kit(itemkit.Deps{Blobs: blobs}).GetItem(response, kittest.Request(t, http.MethodGet, "/artifactory/"+configuration.Key+"/repodata/repomd.xml.key"))
			want := http.StatusOK
			if test.barrierFirst {
				want = http.StatusServiceUnavailable
			}
			if response.Code != want {
				t.Fatalf("selected snapshot status%d want%d (bytes withheld)", response.Code, want)
			}
			if blobs.opens != 1 || blobs.closes != 1 || strings.Contains(response.Body.String(), "PRIVATE KEY") {
				t.Fatal("selected snapshot was reopened, leaked private material or lost its reader")
			}
			if !test.barrierFirst {
				if response.Body.String() != key.Public || !errors.Is(duringResponse, api.ErrCleanupExcluded) {
					t.Fatal("selected public snapshot did not retain its repository-wide admission")
				}
				storekit.Must(t, exclusions.Prepare(ctx, ref))
			}
			current := storekit.Get[model.Item](t)(w.Store.Items().GetByID(ctx, original.ID))
			if current.Actual.SHA256 == original.Actual.SHA256 {
				t.Fatal("replacement control did not change the stored bytes")
			}
		})
	}
}
