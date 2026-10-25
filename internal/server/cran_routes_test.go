package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/source"
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
	var calls []string
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
				}}, nil
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
