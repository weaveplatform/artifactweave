package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

func cranFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	return repopath.Read(t, "migration/artifacts/ecosystem/cran/testdata/"+name)
}

func TestUnitCRANReadAuthorityCallbacks(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "cran-rights", Class: repo.Local, PackageType: repo.Cran}
	cranRepo(t, w, c)
	admin := kernel.Principal{Name: "admin", Admin: true}
	user := kernel.Principal{Name: "user"}
	for _, tc := range []struct {
		name        string
		who         kernel.Principal
		checkErr    error
		expectedOK  bool
		expectedErr bool
	}{
		{"readable", admin, nil, true, false},
		{"no grant", user, nil, false, false},
		{"upstream denied", admin, source.ErrDenied, false, false},
		{"upstream fault", admin, errors.New("upstream fault"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := accessFor(t.Context(), authorizerOver(w.db), tc.who, nil)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := cranPackageReadable(t.Context(), c,
				"src/contrib/assertthat_0.2.1.tar.gz", acc,
				func(context.Context, string) error { return tc.checkErr })
			if ok != tc.expectedOK || (err != nil) != tc.expectedErr {
				t.Fatalf("readable=%v err=%v", ok, err)
			}
		})
	}
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
		{"compressed index", "src/contrib/PACKAGES.gz", compressed.Bytes(), source.ResourceKindMetadata},
		{
			"windows binary", "bin/windows/contrib/4.5/assertthat_0.2.1.zip",
			cranFixtureBytes(t, "assertthat_0.2.1.zip"), source.ResourceKindArtifact,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := cranSourceRequest(source.Request{Resource: source.Resource{
				Kind: source.ResourceKindArtifact, Path: tc.path, Method: http.MethodGet,
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
