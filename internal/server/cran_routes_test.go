package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func cranFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "format", "cran", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
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
				if permit.Intent != source.MayFetch {
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
						return source.Result{Item: &types.Item{
							RepoKey: "cran-proxy", Cached: true, Path: "src/contrib",
							Name:   "assertthat_0.2.1.tar.gz",
							Actual: types.Checksums{MD5: strings.Repeat("0", 32)},
						}}, nil
					}
					body = archive
				default:
					return source.Result{}, store.ErrNotFound
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
		name string
		want int
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
			wantStatus(t, got, tc.want)
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
	items := map[string]types.Item{}
	sources := &source.Service{
		Resolve: sourceIdentity,
		Cache: sourceBackend(
			func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
				if permit.Intent != source.CacheOnly || permit.UpstreamNetworkAllowed {
					t.Fatalf("cache read could fetch upstream: %+v", permit)
				}
				it, ok := items[q.Resource.Path]
				if !ok {
					return source.Result{}, store.ErrNotFound
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
		items[tc.path] = types.Item{
			RepoKey: "cran-cache", Cached: true, Path: filepath.Dir(tc.path),
			Name: filepath.Base(tc.path), Size: int64(len(body)),
			Actual: types.Checksums{
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

func TestUnitCRANReadAuthorityCallbacks(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "cran-rights", Class: repo.Local, PackageType: repo.Cran}
	cranRepo(t, w, c)
	admin := auth.Principal{Name: "admin", Admin: true}
	user := auth.Principal{Name: "user"}
	for _, tc := range []struct {
		name     string
		who      auth.Principal
		checkErr error
		wantOK   bool
		wantErr  bool
	}{
		{"readable", admin, nil, true, false},
		{"no grant", user, nil, false, false},
		{"upstream denied", admin, source.ErrDenied, false, false},
		{"upstream fault", admin, errors.New("upstream fault"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := accessFor(t.Context(), w.db, tc.who)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := cranPackageReadable(t.Context(), c,
				"src/contrib/assertthat_0.2.1.tar.gz", acc,
				func(context.Context, string) error { return tc.checkErr })
			if ok != tc.wantOK || (err != nil) != tc.wantErr {
				t.Fatalf("readable=%v err=%v", ok, err)
			}
		})
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

func TestUnitCRANSourceClassifiesCompressedIndexAndWindowsBinary(t *testing.T) {
	plain := cranFixtureBytes(t, "assertthat-PACKAGES")
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		body       []byte
		kind       source.ResourceKind
	}{
		{"compressed index", "src/contrib/PACKAGES.gz", compressed.Bytes(), source.Metadata},
		{
			"Windows binary", "bin/windows/contrib/4.5/assertthat_0.2.1.zip",
			cranFixtureBytes(t, "assertthat_0.2.1.zip"), source.Artifact,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := cranSourceRequest(source.Request{Resource: source.Resource{
				Kind: source.Artifact, Path: tc.path, Method: http.MethodGet,
			}})
			if err != nil || q.Resource.Kind != tc.kind {
				t.Fatalf("classification: %+v err=%v", q.Resource, err)
			}
			if q.Resource.ValidateMetadata != nil {
				if err := q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(tc.body),
					int64(len(tc.body)), source.MetadataResponse{}); err != nil {
					t.Fatalf("valid compressed index refused: %v", err)
				}
				if err := q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(tc.body),
					32<<20+1, source.MetadataResponse{}); err == nil {
					t.Fatal("oversized CRAN index admitted")
				}
			}
			if q.Resource.Validate != nil {
				if err := q.Resource.Validate(t.Context(), bytes.NewReader(tc.body),
					int64(len(tc.body))); err != nil {
					t.Fatalf("valid binary refused: %v", err)
				}
				bad := bytes.Clone(tc.body)
				bad[len(bad)-1] ^= 0xff
				if err := q.Resource.Validate(t.Context(), bytes.NewReader(bad),
					int64(len(bad))); err == nil {
					t.Fatal("corrupt binary admitted")
				}
			}
		})
	}
	for _, path := range []string{
		"src/contrib/PACKAGES.gz", "bin/windows/contrib/4.5/assertthat_0.2.1.zip",
	} {
		if _, err := cranSourceRequest(source.Request{Resource: source.Resource{
			Path: path, Query: "bypass=1", Method: http.MethodGet,
		}}); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("query on CRAN path %q admitted: %v", path, err)
		}
	}
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
		want                 int
	}{
		{"missing repository", "/artifactory/api/cran/missing/sources", "admin", body, 404},
		{"wrong package type", "/artifactory/api/cran/libs-local/sources", "admin", body, 400},
		{"remote cannot deploy", "/artifactory/api/cran/cran-post-remote/sources", "admin", body, 405},
		{"virtual needs target", "/artifactory/api/cran/cran-post-no-target/sources", "admin", body, 405},
		{"no folder write", "/artifactory/api/cran/cran-post/sources", "user", body, 403},
		{"missing binary query", "/artifactory/api/cran/cran-post/binaries", "admin", body, 400},
		{"duplicate distribution", "/artifactory/api/cran/cran-post/binaries?distribution=windows&distribution=linux&rVersion=4.5", "admin", body, 400},
		{"duplicate R version", "/artifactory/api/cran/cran-post/binaries?distribution=windows&rVersion=4.5&rVersion=4.4", "admin", body, 400},
		{"invalid R version", "/artifactory/api/cran/cran-post/binaries?distribution=windows&rVersion=4.5/../4.4", "admin", body, 400},
		{"wrong archive", "/artifactory/api/cran/cran-post/binaries?distribution=windows&rVersion=4.5", "admin", body, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantStatus(t, w.do(http.MethodPost, tc.path, tc.as, tc.body), tc.want)
		})
	}
	// A grant that reaches the feed folder does not authorize a different
	// package filename inferred from the archive's own DESCRIPTION.
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "other-cran-upload",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionWrite}},
			Targets: map[string]types.Target{"cran-post": {
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
		"src/contrib/assertthat_0.2.1.tar.gz"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed upload committed a package: %v", err)
	}
	// Even a valid archive must not be committed when a spool file cannot be created.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-spool-directory"))
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-post/sources", "admin", body),
		http.StatusInternalServerError)
}

func TestUnitCRANUploadRequiresStoreAndAuthenticatedCaller(t *testing.T) {
	body := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	req := httptest.NewRequest(http.MethodPost, "/artifactory/api/cran/cran-local/sources",
		bytes.NewReader(body))
	missingStore := httptest.NewRecorder()
	(artifactoryService{}).uploadCranSource(missingStore, req)
	wantStatus(t, missingStore, http.StatusNotImplemented)
	w := newItemWorld(t, false)
	withoutCaller := httptest.NewRecorder()
	(artifactoryService{cfg: Config{Store: w.db}}).uploadCranSource(withoutCaller, req)
	wantStatus(t, withoutCaller, http.StatusUnauthorized)
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
