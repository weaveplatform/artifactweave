package server

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitCRANSourceValidatesPublishedBytes(t *testing.T) {
	base := filepath.Join("..", "format", "cran", "testdata")
	index, err := os.ReadFile(filepath.Join(base, "assertthat-PACKAGES"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(base, "assertthat_0.2.1.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := cranSourceRequest(source.Request{Resource: source.Resource{
		Path: "src/contrib/PACKAGES", Method: http.MethodGet,
	}})
	if err != nil || q.Resource.ValidateMetadata == nil || !q.Resource.PermissionFolder ||
		q.Resource.PermissionPath != "src/contrib" {
		t.Fatalf("CRAN metadata contract = %+v, err = %v", q.Resource, err)
	}
	for _, tc := range []struct {
		name string
		body []byte
		size int64
		bad  bool
	}{
		{"published", index, int64(len(index)), false},
		{"mismatched length", index, int64(len(index) + 1), true},
		{"unsafe name", []byte("Package: ../private\nVersion: 1.0\n\n"), 35, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(tc.body), tc.size,
				source.MetadataResponse{})
			if tc.bad != (err != nil) {
				t.Fatalf("metadata error = %v", err)
			}
		})
	}
	q.Resource.Query = "malformed=1"
	if _, err := cranSourceRequest(q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatalf("index query admitted: %v", err)
	}
	pkg, err := cranSourceRequest(source.Request{Resource: source.Resource{
		Path: "src/contrib/assertthat_0.2.1.tar.gz", Method: http.MethodGet,
	}})
	if err != nil || pkg.Resource.Validate == nil || pkg.Resource.Variant != "cran-package-v1" {
		t.Fatalf("CRAN archive contract = %+v, err = %v", pkg.Resource, err)
	}
	if err := pkg.Resource.Validate(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
	); err != nil {
		t.Fatalf("published CRAN archive rejected: %v", err)
	}
	corrupt := bytes.Clone(archive)
	corrupt[len(corrupt)-1] ^= 0xff
	if err := pkg.Resource.Validate(
		t.Context(),
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
	); err == nil {
		t.Fatal("corrupt archive admitted")
	}
	for _, p := range []string{
		"src/contrib/PACKAGES.rds", "src/contrib/../secret.tar.gz",
		"bin/windows/contrib/latest/demo_1.0.zip",
	} {
		if _, err := cranSourceRequest(
			source.Request{Resource: source.Resource{Path: p}},
		); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			t.Errorf("unsafe or unsupported path %q admitted: %v", p, err)
		}
	}
}

func TestUnitCRANVirtualIndexFiltersUnreadablePackage(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "cran-member", Class: repo.Local, PackageType: repo.Cran},
		{
			Key: "cran-view", Class: repo.Virtual, PackageType: repo.Cran,
			Repositories: []string{"cran-member"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(
		filepath.Join("..", "format", "cran", "testdata", "assertthat_0.2.1.tar.gz"),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/cran/cran-member/sources", "admin", string(body)),
		http.StatusCreated)
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "read-cran-index",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{
				"cran-member": {Include: []string{"src/contrib/PACKAGES"}},
				"cran-view":   {Include: []string{"src/contrib/PACKAGES"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	index := w.do("GET", "/artifactory/cran-view/src/contrib/PACKAGES", "user", "")
	wantStatus(t, index, http.StatusOK)
	if bytes.Contains(index.Body.Bytes(), []byte("assertthat")) {
		t.Fatalf("unreadable package leaked into virtual index: %s", index.Body)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "read-cran-package",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{
				"cran-member": {Include: []string{"src/contrib/assertthat_0.2.1.tar.gz"}},
				"cran-view":   {Include: []string{"src/contrib/assertthat_0.2.1.tar.gz"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	index = w.do("GET", "/artifactory/cran-view/src/contrib/PACKAGES", "user", "")
	wantStatus(t, index, http.StatusOK)
	if !bytes.Contains(index.Body.Bytes(), []byte("Package: assertthat")) {
		t.Fatalf("readable package absent from virtual index: %s", index.Body)
	}
}

func TestUnitCRANVirtualRejectsCyclesAndWrongMembers(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "cran-wrong", Class: repo.Local, PackageType: repo.Maven},
		{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran},
		{Key: "cran-empty", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"absent"}},
		{Key: "cran-cycle", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"cran-cycle"}},
		{Key: "cran-mixed", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"cran-wrong"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs}}
	for _, tc := range []struct {
		key      string
		expected error
	}{
		{"cran-empty", nil},
		{"cran-cycle", errCranVirtualCycle},
		{"cran-mixed", errCranMemberType},
	} {
		c, err := w.db.Repos().Get(t.Context(), tc.key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.cranCandidates(t.Context(), w.db, c, "src/contrib/PACKAGES", p, acc,
			map[string]bool{})
		if !errors.Is(err, tc.expected) || tc.expected == nil && len(got) != 0 {
			t.Errorf("%s: candidates=%v error=%v", tc.key, got, err)
		}
	}
	if _, err := svc.cranMemberIndex(t.Context(), w.db,
		repo.Config{Key: "cran-local", Class: repo.Local}, true,
		"src/contrib/PACKAGES", p, acc); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("local cache alias accepted: %v", err)
	}
}

func TestUnitCRANNativePOSTAndReindex(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "cran-upload", Class: repo.Local, PackageType: repo.Cran}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("..", "format", "cran", "testdata")
	for _, tc := range []struct {
		file, endpoint, path, index string
	}{
		{"assertthat_0.2.1.tar.gz", "sources", "src/contrib/assertthat_0.2.1.tar.gz", "src/contrib/PACKAGES"},
		{"assertthat_0.2.1.zip", "binaries?distribution=windows&rVersion=4.5", "bin/windows/contrib/4.5/assertthat_0.2.1.zip", "bin/windows/contrib/4.5/PACKAGES"},
	} {
		body, err := os.ReadFile(filepath.Join(base, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		rec := w.do("POST", "/artifactory/api/cran/cran-upload/"+tc.endpoint, "admin", string(body))
		wantStatus(t, rec, http.StatusCreated)
		if got := w.item("cran-upload", false, tc.path); got.Size != int64(len(body)) {
			t.Fatalf("stored %s size = %d", tc.path, got.Size)
		}
		index := w.do("GET", "/artifactory/cran-upload/"+tc.index, "admin", "")
		wantStatus(t, index, http.StatusOK)
		if !bytes.Contains(index.Body.Bytes(), []byte("Package: assertthat")) {
			t.Fatalf("%s index missing package: %s", tc.index, index.Body)
		}
	}
	for _, endpoint := range []string{
		"binaries?distribution=windows&rVersion=latest",
		"binaries?distribution=other&rVersion=4.5",
	} {
		wantStatus(t, w.do("POST", "/artifactory/api/cran/cran-upload/"+endpoint,
			"admin", "invalid"), http.StatusBadRequest)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/cran/cran-upload/sources", "admin", "invalid"),
		http.StatusBadRequest)
	wantStatus(t, w.do("POST", "/artifactory/api/cran/reindex/cran-upload", "admin", ""),
		http.StatusOK)
	wantStatus(t, w.do("DELETE", "/artifactory/cran-upload/src/contrib/assertthat_0.2.1.tar.gz",
		"admin", ""), http.StatusNoContent)
	wantStatus(t, w.do("GET", "/artifactory/cran-upload/src/contrib/PACKAGES", "admin", ""),
		http.StatusNotFound)
	wantStatus(
		t,
		w.do(
			"POST",
			"/artifactory/api/trash/restore/cran-upload/src/contrib/assertthat_0.2.1.tar.gz?to=cran-upload",
			"admin",
			"",
		),
		http.StatusAccepted,
	)
	wantStatus(t, w.do("GET", "/artifactory/cran-upload/src/contrib/PACKAGES", "admin", ""),
		http.StatusOK)
}
