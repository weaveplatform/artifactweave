package cran_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func cranFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	return repopath.Read(t, "internal/artifacts/ecosystem/cran/testdata/"+name)
}

func TestUnitCRANRemoteIndexPackageAndInvalidMetadata(t *testing.T) {
	index := cranFixtureBytes(t, "assertthat-PACKAGES")
	archive := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	var corrupt bool
	var badCachedArchive bool
	var failPackageRead bool
	var calls []string
	var usedCount int
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
				if permit.Intent != source.RetrievalIntentMayFetch {
					t.Fatalf("unexpected source permit: %+v", permit)
				}
				calls = append(calls, q.Resource.Path)
				var body []byte
				switch q.Resource.Path {
				case "src/contrib/PACKAGES":
					body = index
					if corrupt {
						body = []byte("Package: ../private\nVersion: 1.0\n")
					}
				case "src/contrib/assertthat_0.2.1.tar.gz":
					if failPackageRead {
						return source.Result{}, source.ErrUnavailable
					}
					if badCachedArchive {
						return source.Result{Item: &artmodel.Item{
							RepoKey: "cran-proxy", Cached: true, Path: "src/contrib",
							Name:   "assertthat_0.2.1.tar.gz",
							Actual: kernel.Checksums{MD5: strings.Repeat("0", 32)},
						}}, nil
					}
					body = archive
				default:
					return source.Result{}, kernel.ErrNotFound
				}
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{
						Size:      int64(len(body)),
						MediaType: "application/octet-stream",
					},
				}, Used: func(context.Context) error { usedCount++; return nil }}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	cranRepo(t, w, repo.Config{
		Key: "cran-proxy", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	cranRepo(t, w, repo.Config{
		Key: "cran-proxy-view", Class: repo.Virtual,
		PackageType: repo.Cran, Repositories: []string{"cran-proxy"},
	})
	for _, path := range []string{
		"/artifactory/cran-proxy/src/contrib/PACKAGES",
		"/artifactory/cran-proxy-view/src/contrib/PACKAGES",
	} {
		res := w.do(http.MethodGet, path, "admin", "")
		wantStatus(t, res, http.StatusOK)
		if !strings.Contains(res.Body.String(), "Package: assertthat") {
			t.Fatalf("%s did not expose validated index: %s", path, res.Body)
		}
	}
	res := w.do(http.MethodGet,
		"/artifactory/cran-proxy-view/src/contrib/assertthat_0.2.1.tar.gz", "admin", "")
	wantStatus(t, res, http.StatusOK)
	if !bytes.Equal(res.Body.Bytes(), archive) {
		t.Fatal("remote package content changed during virtual delivery")
	}
	if len(calls) < 3 {
		t.Fatalf("remote index or archive was not fetched: %v", calls)
	}
	if usedCount == 0 {
		t.Fatal("successful remote CRAN delivery did not mark cache content used")
	}
	corrupt = true
	res = w.do(http.MethodGet, "/artifactory/cran-proxy/src/contrib/PACKAGES", "admin", "")
	if res.Code != http.StatusBadGateway {
		t.Fatalf("invalid upstream index: %d %s", res.Code, res.Body)
	}
	res = w.do(http.MethodGet,
		"/artifactory/cran-proxy/src/contrib/assertthat_0.2.1.tar.gz", "admin", "")
	if res.Code != http.StatusBadGateway {
		t.Fatalf("package should not bypass invalid upstream index: %d %s", res.Code, res.Body)
	}
	corrupt = false
	badCachedArchive = true
	res = w.do(http.MethodGet,
		"/artifactory/cran-proxy/src/contrib/assertthat_0.2.1.tar.gz", "admin", "")
	if res.Code != http.StatusBadGateway {
		t.Fatalf("cached archive with wrong MD5: %d %s", res.Code, res.Body)
	}
	badCachedArchive = false
	failPackageRead = true
	res = w.do(http.MethodGet,
		"/artifactory/cran-proxy/src/contrib/assertthat_0.2.1.tar.gz", "admin", "")
	if res.Code != http.StatusBadGateway {
		t.Fatalf("unavailable upstream archive: %d %s", res.Code, res.Body)
	}
}

type cranFailedReader struct{}

func (cranFailedReader) Read([]byte) (int, error) { return 0, errors.New("transport read failed") }

func TestUnitCRANRemoteIndexFaults(t *testing.T) {
	index := cranFixtureBytes(t, "assertthat-PACKAGES")
	var mode string
	svc := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, _ source.Request, _ source.Permit) (source.Result, error) {
			switch mode {
			case "unavailable":
				return source.Result{}, source.ErrUnavailable
			case "empty result":
				return source.Result{}, nil
			case "read failure":
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(cranFailedReader{}), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(index))},
				}}, nil
			case "oversized index":
				body := bytes.Repeat([]byte("X"), 32<<20+1)
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				}}, nil
			default:
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(index)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(index))},
				}}, nil
			}
		},
	)}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	cranRepo(t, w, repo.Config{
		Key: "cran-fault-remote", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	for _, tc := range []struct {
		name     string
		expected int
	}{
		{"unavailable", http.StatusBadGateway},
		{"empty result", http.StatusBadGateway},
		{"read failure", http.StatusBadGateway},
		{"oversized index", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode = tc.name
			got := w.do(http.MethodGet,
				"/artifactory/cran-fault-remote/src/contrib/PACKAGES", "admin", "")
			wantStatus(t, got, tc.expected)
			if strings.Contains(got.Body.String(), "assertthat") {
				t.Fatalf("faulty upstream index advertised a package: %s", got.Body)
			}
		})
	}
	// A configured remote without a source service cannot serve its index.
	wNoSource := newItemWorld(t, false)
	cranRepo(t, wNoSource, repo.Config{
		Key: "cran-offline-remote", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	wantStatus(t, wNoSource.do(http.MethodGet,
		"/artifactory/cran-offline-remote/src/contrib/PACKAGES", "admin", ""),
		http.StatusBadGateway)
}

func TestUnitCRANRemoteIndexRechecksAuthorityBeforeDelivery(t *testing.T) {
	index := cranFixtureBytes(t, "assertthat-PACKAGES")
	checks := 0
	svc := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, _ source.Request, _ source.Permit) (source.Result, error) {
			return source.Result{
				Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(index)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(index))},
				},
				Revalidate: func(context.Context) error {
					checks++
					if checks > 1 {
						return source.ErrDenied
					}
					return nil
				},
			}, nil
		},
	)}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	cranRepo(t, w, repo.Config{
		Key: "cran-revoked", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	got := w.do(http.MethodGet, "/artifactory/cran-revoked/src/contrib/PACKAGES", "admin", "")
	wantStatus(t, got, http.StatusForbidden)
	if checks < 2 || strings.Contains(got.Body.String(), "assertthat") {
		t.Fatalf("revoked source disclosed index: checks=%d body=%s", checks, got.Body)
	}
}

func TestUnitCRANRemoteIndexRejectsRevokedPackageAuthority(t *testing.T) {
	index := cranFixtureBytes(t, "assertthat-PACKAGES")
	checks := 0
	svc := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, _ source.Request, _ source.Permit) (source.Result, error) {
			return source.Result{
				Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(index)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(index))},
				},
				Revalidate: func(context.Context) error {
					checks++
					if checks > 2 {
						return source.ErrUnavailable
					}
					return nil
				},
			}, nil
		},
	)}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	cranRepo(t, w, repo.Config{
		Key: "cran-revoked-file", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	cranRepo(t, w, repo.Config{
		Key: "cran-revoked-view", Class: repo.Virtual,
		PackageType: repo.Cran, Repositories: []string{"cran-revoked-file"},
	})
	for _, path := range []string{
		"/artifactory/cran-revoked-file/src/contrib/PACKAGES",
		"/artifactory/cran-revoked-view/src/contrib/PACKAGES",
	} {
		checks = 0
		got := w.do(http.MethodGet, path, "admin", "")
		wantStatus(t, got, http.StatusBadGateway)
		if checks < 3 || strings.Contains(got.Body.String(), "assertthat") {
			t.Fatalf(
				"revoked package authority disclosed index: checks=%d body=%s",
				checks,
				got.Body,
			)
		}
	}
}

func TestUnitCRANOfflineCachedIndexAndArchive(t *testing.T) {
	items := map[string]artmodel.Item{}
	sources := &source.Service{
		Resolve: sourceIdentity,
		Cache: sourceBackend(
			func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
				if permit.Intent != source.RetrievalIntentCacheOnly ||
					permit.UpstreamNetworkAllowed {
					t.Fatalf("cache read could fetch upstream: %+v", permit)
				}
				it, ok := items[q.Resource.Path]
				if !ok {
					return source.Result{}, kernel.ErrNotFound
				}
				return source.Result{Item: &it}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	cranRepo(t, w, repo.Config{
		Key: "cran-cache", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	for _, tc := range []struct {
		path, fixture string
	}{
		{"src/contrib/PACKAGES", "assertthat-PACKAGES"},
		{"src/contrib/assertthat_0.2.1.tar.gz", "assertthat_0.2.1.tar.gz"},
	} {
		body := cranFixtureBytes(t, tc.fixture)
		upload, err := w.blobs.BeginUpload(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := upload.Append(t.Context(), bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		info, err := upload.Commit(t.Context(), blob.Digests{})
		if err != nil {
			t.Fatal(err)
		}
		items[tc.path] = artmodel.Item{
			RepoKey: "cran-cache", Cached: true, Path: path.Dir(tc.path),
			Name: path.Base(tc.path), Size: int64(len(body)),
			Actual: kernel.Checksums{
				SHA256: info.Digests.SHA256, MD5: info.Digests.MD5,
			},
		}
	}
	// Cache aliases exercise the real source fence and byte delivery without
	// any network backend.
	for _, tc := range []struct {
		path, fixture string
	}{
		{"src/contrib/PACKAGES", "assertthat-PACKAGES"},
		{"src/contrib/assertthat_0.2.1.tar.gz", "assertthat_0.2.1.tar.gz"},
	} {
		got := w.do(http.MethodGet, "/artifactory/cran-cache-cache/"+tc.path, "admin", "")
		wantStatus(t, got, http.StatusOK)
		if tc.path != "src/contrib/PACKAGES" &&
			!bytes.Equal(got.Body.Bytes(), cranFixtureBytes(t, tc.fixture)) {
			t.Fatalf("cached archive changed during delivery: %s", tc.path)
		}
	}
}

func TestUnitCRANOrderedNestedVirtualMembers(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "cran-first", Class: repo.Local, PackageType: repo.Cran},
		{Key: "cran-second", Class: repo.Local, PackageType: repo.Cran},
		{
			Key: "cran-inner", Class: repo.Virtual, PackageType: repo.Cran,
			Repositories: []string{"cran-first"},
		},
		{
			Key: "cran-outer", Class: repo.Virtual, PackageType: repo.Cran,
			Repositories: []string{"missing", "cran-inner", "cran-second"},
		},
	} {
		cranRepo(t, w, c)
	}
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	for _, key := range []string{"cran-first", "cran-second"} {
		wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/"+key+"/sources",
			"admin", body), http.StatusCreated)
	}
	indexURL := "/artifactory/cran-outer/src/contrib/PACKAGES"
	archiveURL := "/artifactory/cran-outer/src/contrib/assertthat_0.2.1.tar.gz"
	index := w.do(http.MethodGet, indexURL, "admin", "")
	wantStatus(t, index, http.StatusOK)
	if strings.Count(index.Body.String(), "Package: assertthat") != 1 {
		t.Fatalf("duplicate virtual member advertised twice: %s", index.Body)
	}
	wantStatus(t, w.do(http.MethodGet, archiveURL, "admin", ""), http.StatusOK)
	wantStatus(t, w.do(http.MethodDelete,
		"/artifactory/cran-first/src/contrib/assertthat_0.2.1.tar.gz", "admin", ""),
		http.StatusNoContent)
	index = w.do(http.MethodGet, indexURL, "admin", "")
	wantStatus(t, index, http.StatusOK)
	if strings.Count(index.Body.String(), "Package: assertthat") != 1 {
		t.Fatalf("later readable member not selected after deletion: %s", index.Body)
	}
	wantStatus(t, w.do(http.MethodGet, archiveURL, "admin", ""), http.StatusOK)
}

func cranRepo(t *testing.T, w *itemWorld, c repo.Config) {
	t.Helper()
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
}

func TestUnitCRANPOSTUploadRefusalsAndVirtualTarget(t *testing.T) {
	w := newItemWorld(t, false)
	cranRepo(t, w, repo.Config{Key: "cran-post", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, repo.Config{Key: "cran-post-failed", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, repo.Config{
		Key: "cran-post-remote", Class: repo.Remote,
		PackageType: repo.Cran, URL: "https://up.example",
	})
	cranRepo(t, w, repo.Config{
		Key: "cran-post-virtual", Class: repo.Virtual,
		PackageType: repo.Cran, Repositories: []string{"cran-post"},
		DefaultDeploymentRepo: "cran-post",
	})
	cranRepo(t, w, repo.Config{
		Key: "cran-post-no-target", Class: repo.Virtual,
		PackageType: repo.Cran, Repositories: []string{"cran-post"},
	})
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	for _, tc := range []struct {
		name, path, as, body string
		expected             int
	}{
		{"missing repository", "/artifactory/api/cran/missing/sources", "admin", body, 404},
		{"wrong package type", "/artifactory/api/cran/libs-local/sources", "admin", body, 400},
		{"remote cannot deploy", "/artifactory/api/cran/cran-post-remote/sources", "admin", body, 405},
		{"virtual needs target", "/artifactory/api/cran/cran-post-no-target/sources", "admin", body, 405},
		{"no folder write", "/artifactory/api/cran/cran-post/sources", "user", body, 403},
		{"missing binary query", "/artifactory/api/cran/cran-post/binaries", "admin", body, 400},
		{"duplicate distribution", "/artifactory/api/cran/cran-post/binaries?distribution=windows&distribution=linux&rVersion=4.5", "admin", body, 400},
		{"duplicate r version", "/artifactory/api/cran/cran-post/binaries?distribution=windows&rVersion=4.5&rVersion=4.4", "admin", body, 400},
		{"invalid r version", "/artifactory/api/cran/cran-post/binaries?distribution=windows&rVersion=4.5/../4.4", "admin", body, 400},
		{"wrong archive", "/artifactory/api/cran/cran-post/binaries?distribution=windows&rVersion=4.5", "admin", body, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantStatus(t, w.do(http.MethodPost, tc.path, tc.as, tc.body), tc.expected)
		})
	}
	// A grant that reaches the feed folder does not authorize a different
	// package filename inferred from the archive's own DESCRIPTION.
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "other-cran-upload",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionWrite}},
			Targets: map[string]accesstest.Target{"cran-post": {
				Include: []string{"src/contrib/other_1.0.tar.gz"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-post/sources", "user", body),
		http.StatusForbidden)
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-post/sources", "admin", body,
		"X-Checksum-Sha256", strings.Repeat("0", 64)), http.StatusConflict)
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-post-virtual/sources",
		"admin", body), http.StatusCreated)
	if got := w.item(
		"cran-post",
		false,
		"src/contrib/assertthat_0.2.1.tar.gz",
	); got.Size != int64(
		len(body),
	) {
		t.Fatalf("virtual target stored unexpected content size: %d", got.Size)
	}
	for _, distribution := range []string{"mac", "macosx", "linux"} {
		got := w.do(http.MethodPost,
			"/artifactory/api/cran/cran-post/binaries?distribution="+distribution+"&rVersion=4.5",
			"admin", "invalid archive")
		wantStatus(t, got, http.StatusBadRequest)
	}
	// A transport failure while spooling must never commit a partial archive.
	req := httptest.NewRequest(http.MethodPost,
		"/artifactory/api/cran/cran-post-failed/sources", cranFailedReader{})
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusBadRequest)
	if _, err := w.db.Items().Get(t.Context(), "cran-post-failed", false,
		"src/contrib/assertthat_0.2.1.tar.gz"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("failed upload committed a package: %v", err)
	}
	// Even a valid archive must not be committed when a spool file cannot be created.
	missingTemp := filepath.Join(t.TempDir(), "missing-spool-directory")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, missingTemp)
	}
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-post/sources", "admin", body),
		http.StatusInternalServerError)
}

func TestUnitCRANPOSTOverwriteRequiresCanonicalPathAuthority(t *testing.T) {
	w := newItemWorld(t, false)
	cranRepo(t, w, repo.Config{Key: "cran-overwrite", Class: repo.Local, PackageType: repo.Cran})
	first := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	second := bytes.Clone(first)
	// gzip's timestamp is outside the compressed payload and trailer. Changing
	// it gives a distinct, valid archive with the same CRAN package identity.
	second[4] ^= 1
	endpoint := "/artifactory/api/cran/cran-overwrite/sources"
	wantStatus(t, w.do(http.MethodPost, endpoint, "admin", string(first)), http.StatusCreated)
	wantStatus(t, w.do(http.MethodPost, endpoint, "admin", string(second)), http.StatusCreated)
	got := w.do(http.MethodGet,
		"/artifactory/cran-overwrite/src/contrib/assertthat_0.2.1.tar.gz", "admin", "")
	wantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), second) {
		t.Fatal("authorized CRAN overwrite did not replace stored bytes")
	}
}

func TestUnitCRANIndexVariantsAndVirtualPackageRoutes(t *testing.T) {
	w := newItemWorld(t, false)
	cranRepo(t, w, repo.Config{Key: "cran-index", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, repo.Config{
		Key: "cran-index-view", Class: repo.Virtual,
		PackageType: repo.Cran, Repositories: []string{"cran-index"},
	})
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-index/sources", "admin", body),
		http.StatusCreated)
	gz := w.do(http.MethodGet, "/artifactory/cran-index-view/src/contrib/PACKAGES.gz", "admin", "")
	wantStatus(t, gz, http.StatusOK)
	if gz.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("compressed index content type: %s", gz.Header().Get("Content-Type"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(gz.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("Package: assertthat")) {
		t.Fatalf("compressed index does not advertise installed package: %s", plain)
	}
	wantStatus(t, w.do(http.MethodHead, "/artifactory/cran-index-view/src/contrib/PACKAGES",
		"admin", ""), http.StatusOK)
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-index-view/src/contrib/assertthat_0.2.1.tar.gz", "admin", ""),
		http.StatusOK)
	// Native routing must leave stored local archives and unrelated paths to
	// the ordinary item handler.
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-index/src/contrib/assertthat_0.2.1.tar.gz", "admin", ""),
		http.StatusOK)
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-index/src/contrib/README", "admin", ""),
		http.StatusNotFound)
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/libs-local/src/contrib/PACKAGES", "admin", ""),
		http.StatusNotFound)
	for _, path := range []string{
		"/artifactory/cran-index-view/src/contrib/PACKAGES.rds",
		"/artifactory/cran-index-view/src/contrib/PACKAGES?unexpected=1",
		"/artifactory/cran-index-view/src/contrib/PACKAGES;tag=x",
		"/artifactory/cran-index-view/src/contrib/assertthat_0.2.1.tar.gz?unexpected=1",
		"/artifactory/cran-index-view/src/contrib/absent_1.0.tar.gz",
	} {
		got := w.do(http.MethodGet, path, "admin", "")
		if got.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, got.Code, got.Body)
		}
	}
	for _, path := range []string{
		"/artifactory/cran-index-view/src/contrib/PACKAGES",
		"/artifactory/cran-index-view/src/contrib/assertthat_0.2.1.tar.gz",
	} {
		got := w.do(http.MethodGet, path, "user", "")
		if got.Code != http.StatusForbidden {
			t.Errorf("denied %s: %d %s", path, got.Code, got.Body)
		}
	}
	// The virtual index and package response must not expose a member that
	// is disabled while the virtual repository itself remains readable.
	c, err := w.db.Repos().Get(t.Context(), "cran-index")
	if err != nil {
		t.Fatal(err)
	}
	c.BlackedOut = new(true)
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	index := w.do(http.MethodGet, "/artifactory/cran-index-view/src/contrib/PACKAGES", "admin", "")
	wantStatus(t, index, http.StatusOK)
	if strings.Contains(index.Body.String(), "assertthat") {
		t.Fatalf("disabled member leaked into virtual index: %s", index.Body)
	}
}

func TestUnitCRANVirtualStaleMemberAndCorruptHostedIndex(t *testing.T) {
	w := newItemWorld(t, false)
	cranRepo(t, w, repo.Config{Key: "cran-stale", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, repo.Config{
		Key: "cran-stale-view", Class: repo.Virtual,
		PackageType: repo.Cran, Repositories: []string{"cran-stale"},
	})
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	wantStatus(t, w.do(http.MethodPost,
		"/artifactory/api/cran/cran-stale/sources", "admin", body), http.StatusCreated)
	const archive = "src/contrib/assertthat_0.2.1.tar.gz"
	const index = "src/contrib/PACKAGES"
	url := "/artifactory/cran-stale-view/" + archive
	wantStatus(t, w.do(http.MethodGet, url, "admin", ""), http.StatusOK)
	it := w.item("cran-stale", false, archive)
	if _, err := w.db.Items().Delete(t.Context(), "cran-stale", false, archive); err != nil {
		t.Fatal(err)
	}
	// The index can briefly lag an out-of-band deletion. A virtual member
	// must not invent a download merely because its stale index lists one.
	wantStatus(t, w.do(http.MethodGet, url, "admin", ""), http.StatusNotFound)
	it.Folder = true
	if _, err := w.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodGet, url, "admin", ""), http.StatusNotFound)

	// Damaged hosted index content is an internal consistency failure. It
	// must not expose the package as though metadata were still valid.
	idx := w.item("cran-stale", false, index)
	idx.Folder = true
	if _, err := w.db.Items().Put(t.Context(), idx); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-stale/src/contrib/PACKAGES", "admin", ""),
		http.StatusNotFound)
	idx.Folder = false
	idx.Actual.SHA256 = strings.Repeat("0", 64)
	if _, err := w.db.Items().Put(t.Context(), idx); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-stale/src/contrib/PACKAGES", "admin", ""),
		http.StatusInternalServerError)
	upload, err := w.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(
		t.Context(),
		strings.NewReader("Package: ../private\nVersion: 1.0\n"),
	); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	idx.Actual.SHA256 = info.Digests.SHA256
	if _, err := w.db.Items().Put(t.Context(), idx); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-stale/src/contrib/PACKAGES", "admin", ""),
		http.StatusInternalServerError)
}

// The source feed's index, and the archive of the assertthat package the
// published fixtures hold.
const (
	cranIndex      = "src/contrib/PACKAGES"
	cranAssertthat = "src/contrib/assertthat_0.2.1.tar.gz"
)

// wantCRANError fails the test unless rec answered status with the
// Artifactory error envelope that carries message.
func wantCRANError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// cranPublish uploads each body to the source feed of the local repository
// key and then applies tune to its configuration, as an administrator who
// sets a download policy once the packages are deployed, which the patterns
// would otherwise refuse.
func cranPublish(t *testing.T, w *itemWorld, key string, tune func(*repo.Config), bodies ...string) {
	t.Helper()
	cranRepo(t, w, repo.Config{Key: key, Class: repo.Local, PackageType: repo.Cran})
	for _, body := range bodies {
		wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/"+key+"/sources", "admin", body),
			http.StatusCreated)
	}
	c, err := w.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	tune(&c)
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

// cranExcluding returns a tune that sets the exclude pattern to pattern.
func cranExcluding(pattern string) func(*repo.Config) {
	return func(c *repo.Config) { c.ExcludesPattern = &pattern }
}

// cranUpstream serves the published assertthat index and archive, and
// fails the test when the source is asked for the archive by a remote whose
// patterns reject it.
func cranUpstream(t *testing.T) *source.Service {
	index := cranFixtureBytes(t, "assertthat-PACKAGES")
	archive := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	return &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			body := index
			switch q.Resource.Path {
			case cranIndex:
			case cranAssertthat:
				if q.Repo.ExcludesPattern != nil &&
					strings.Contains(*q.Repo.ExcludesPattern, "assertthat") {
					t.Errorf("the source of %s was asked for an archive its patterns reject",
						q.Repo.Key)
				}
				body = archive
			default:
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)}
}

// TestAccCRANRefusalsAnswerTheDownloadPolicy: an index, and a remote or
// virtual package, is refused to a caller who may not read it before
// anything else (DV-057). Past that, a blacked-out local or remote
// repository, its -cache alias, or patterns that reject the requested path
// answer the download policy's 404 and message before the source is asked,
// and a virtual repository whose own patterns reject the path answers that
// no repository serves it.
func TestAccCRANRefusalsAnswerTheDownloadPolicy(t *testing.T) {
	w := newItemWorld(t, false)
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	cranPublish(t, w, "cran-local", func(*repo.Config) {}, body)
	cranPublish(t, w, "cran-dark", func(c *repo.Config) { c.BlackedOut = new(true) }, body)
	cranPublish(t, w, "cran-hidden", cranExcluding("**/PACKAGES*"), body)
	for _, c := range []repo.Config{
		{
			Key: "cran-dark-remote", Class: repo.Remote, PackageType: repo.Cran,
			URL: "https://up.example", BlackedOut: new(true),
		},
		{
			Key: "cran-picky-remote", Class: repo.Remote, PackageType: repo.Cran,
			URL: "https://up.example", ExcludesPattern: new("**/*.tar.gz"),
		},
		{
			Key: "cran-closed", Class: repo.Virtual, PackageType: repo.Cran,
			Repositories: []string{"cran-local"}, ExcludesPattern: new("src/**"),
		},
	} {
		cranRepo(t, w, c)
	}
	blackedOut := func(key, path string) string {
		return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
			key + ":" + path + "'."
	}
	rejected := func(key, path string) string {
		return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
			"' due to its include/exclude pattern settings."
	}
	for _, tc := range []struct {
		name, key, path, as string
		status              int
		message             string
	}{
		{"user reads an index", "cran-local", cranIndex, "user", http.StatusForbidden, "Forbidden"},
		{
			"user reads a blacked-out index", "cran-dark", cranIndex, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"user reads a package a remote's patterns reject", "cran-picky-remote",
			cranAssertthat, "user", http.StatusForbidden, "Forbidden",
		},
		{
			"blacked-out index", "cran-dark", cranIndex, "admin",
			http.StatusNotFound, blackedOut("cran-dark", cranIndex),
		},
		{
			"blacked-out compressed index", "cran-dark", cranIndex + ".gz", "admin",
			http.StatusNotFound, blackedOut("cran-dark", cranIndex+".gz"),
		},
		{
			"index the patterns reject", "cran-hidden", cranIndex, "admin",
			http.StatusNotFound, rejected("cran-hidden", cranIndex),
		},
		{
			"blacked-out remote index", "cran-dark-remote", cranIndex, "admin",
			http.StatusNotFound, blackedOut("cran-dark-remote", cranIndex),
		},
		{
			"blacked-out remote's cache", "cran-dark-remote-cache", cranIndex, "admin",
			http.StatusNotFound, blackedOut("cran-dark-remote", cranIndex),
		},
		{
			"blacked-out remote's package", "cran-dark-remote", cranAssertthat, "admin",
			http.StatusNotFound, blackedOut("cran-dark-remote", cranAssertthat),
		},
		{
			"remote package the patterns reject", "cran-picky-remote", cranAssertthat, "admin",
			http.StatusNotFound, rejected("cran-picky-remote", cranAssertthat),
		},
		{
			"virtual index its patterns reject", "cran-closed", cranIndex, "admin",
			http.StatusNotFound, "No repository found to serve the request for cran-closed:" + cranIndex,
		},
		{
			"virtual package its patterns reject", "cran-closed", cranAssertthat, "admin",
			http.StatusNotFound,
			"No repository found to serve the request for cran-closed:" + cranAssertthat,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.do(http.MethodGet, "/artifactory/"+tc.key+"/"+tc.path, tc.as, "")
			wantCRANError(t, rec, tc.status, tc.message)
		})
	}
}

// TestAccCRANIndexKeepsAPackageThePatternsReject: a local or remote index
// lists a package whose archive the repository's patterns reject, as 7.x
// keeps such a package in its indexes (RTFACT-30830, RTFACT-30656); only
// that archive's GET answers the policy's 404. A remote whose patterns
// reject only the PACKAGES file still serves the archive, which a download
// checks at its own path: the index read that pins its MD5 checks the
// remote's patterns at the feed folder.
func TestAccCRANIndexKeepsAPackageThePatternsReject(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = cranUpstream(t) })
	archive := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	zeta := conanTestArchive(t, "zeta/DESCRIPTION", "Package: zeta\nVersion: 1.0\n")
	cranPublish(t, w, "cran-local", cranExcluding("**/assertthat_*"), string(archive), zeta)
	for _, c := range []repo.Config{
		{
			Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran,
			URL: "https://up.example", ExcludesPattern: new("**/assertthat_*"),
		},
		{
			Key: "cran-indexless", Class: repo.Remote, PackageType: repo.Cran,
			URL: "https://up.example", ExcludesPattern: new("**/PACKAGES*"),
		},
	} {
		cranRepo(t, w, c)
	}
	for _, tc := range []struct {
		key  string
		want []string
	}{
		{"cran-local", []string{"File: assertthat_0.2.1.tar.gz", "File: zeta_1.0.tar.gz"}},
		{"cran-remote", []string{"File: assertthat_0.2.1.tar.gz"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			index := w.do(http.MethodGet, "/artifactory/"+tc.key+"/"+cranIndex, "admin", "")
			wantStatus(t, index, http.StatusOK)
			for _, want := range tc.want {
				if !strings.Contains(index.Body.String(), want) {
					t.Errorf("index lacks %q: %s", want, index.Body)
				}
			}
			wantCRANError(t,
				w.do(http.MethodGet, "/artifactory/"+tc.key+"/"+cranAssertthat, "admin", ""),
				http.StatusNotFound, "The repository '"+tc.key+"' rejected the artifact '"+
					tc.key+":"+cranAssertthat+"' due to its include/exclude pattern settings.")
		})
	}
	got := w.do(http.MethodGet, "/artifactory/cran-indexless/"+cranAssertthat, "admin", "")
	wantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), archive) {
		t.Error("archive behind a rejected index changed during delivery")
	}
}

// TestAccCRANVirtualKeepsPackagesThePatternsReject: a virtual index keeps a
// package whose archive a local or remote member's patterns, or a virtual
// repository's own, reject, nested ones included, and that row claims the
// package's name. The archive's GET passes over the refusing member to the
// next that lists the package, without asking a refusing remote's source,
// answers not found when none serves that path, and answers that no
// repository serves it when the virtual repository's own patterns reject
// it.
func TestAccCRANVirtualKeepsPackagesThePatternsReject(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = cranUpstream(t) })
	archive := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	cranPublish(t, w, "cran-hiding", cranExcluding("**/assertthat_*"), string(archive))
	cranPublish(t, w, "cran-plain", func(*repo.Config) {}, string(archive))
	cranPublish(t, w, "cran-newer", cranExcluding("**/zeta_2.0*"),
		conanTestArchive(t, "zeta/DESCRIPTION", "Package: zeta\nVersion: 2.0\n"))
	cranPublish(t, w, "cran-older", func(*repo.Config) {},
		conanTestArchive(t, "zeta/DESCRIPTION", "Package: zeta\nVersion: 1.0\n"))
	for _, c := range []repo.Config{
		{
			Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran,
			URL: "https://up.example", ExcludesPattern: new("**/assertthat_*"),
		},
		{Key: "cran-virtual", Repositories: []string{"cran-hiding", "cran-plain"}},
		{Key: "cran-only-hiding", Repositories: []string{"cran-hiding"}},
		{
			Key: "cran-picky", Repositories: []string{"cran-plain"},
			ExcludesPattern: new("**/assertthat_*"),
		},
		{Key: "cran-nested", Repositories: []string{"cran-picky"}},
		{Key: "cran-over-remote", Repositories: []string{"cran-remote"}},
		{Key: "cran-versions", Repositories: []string{"cran-newer", "cran-older"}},
	} {
		if c.Class == "" {
			c.Class, c.PackageType = repo.Virtual, repo.Cran
		}
		cranRepo(t, w, c)
	}
	for _, key := range []string{
		"cran-virtual", "cran-only-hiding", "cran-picky", "cran-nested", "cran-over-remote",
	} {
		index := w.do(http.MethodGet, "/artifactory/"+key+"/"+cranIndex, "admin", "")
		wantStatus(t, index, http.StatusOK)
		if strings.Count(index.Body.String(), "File: assertthat_0.2.1.tar.gz") != 1 {
			t.Errorf("%s index does not list assertthat once: %s", key, index.Body)
		}
	}
	index := w.do(http.MethodGet, "/artifactory/cran-versions/"+cranIndex, "admin", "")
	wantStatus(t, index, http.StatusOK)
	if !strings.Contains(index.Body.String(), "File: zeta_2.0.tar.gz") ||
		strings.Contains(index.Body.String(), "zeta_1.0") {
		t.Errorf("cran-versions index does not list cran-newer's zeta alone: %s", index.Body)
	}
	got := w.do(http.MethodGet, "/artifactory/cran-virtual/"+cranAssertthat, "admin", "")
	wantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), archive) {
		t.Error("archive changed during virtual delivery")
	}
	for _, url := range []string{
		"/artifactory/cran-only-hiding/" + cranAssertthat,
		"/artifactory/cran-nested/" + cranAssertthat,
		"/artifactory/cran-over-remote/" + cranAssertthat,
		"/artifactory/cran-versions/src/contrib/zeta_2.0.tar.gz",
	} {
		wantCRANError(t, w.do(http.MethodGet, url, "admin", ""),
			http.StatusNotFound, "Could not find resource")
	}
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/cran-versions/src/contrib/zeta_1.0.tar.gz", "admin", ""), http.StatusOK)
	wantCRANError(t, w.do(http.MethodGet, "/artifactory/cran-picky/"+cranAssertthat, "admin", ""),
		http.StatusNotFound,
		"No repository found to serve the request for cran-picky:"+cranAssertthat)
}
