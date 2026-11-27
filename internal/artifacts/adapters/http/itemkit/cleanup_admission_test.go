package itemkit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
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
