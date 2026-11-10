package cran_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

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
	body := repopath.Read(t, "migration/artifacts/ecosystem/cran/testdata/assertthat_0.2.1.tar.gz")
	wantStatus(t, w.do("POST", "/artifactory/api/cran/cran-member/sources", "admin", string(body)),
		http.StatusCreated)
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "read-cran-index",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{
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
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "read-cran-package",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{
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

func TestUnitCRANNativePOSTAndReindex(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "cran-upload", Class: repo.Local, PackageType: repo.Cran}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	base := repopath.Path(t, "migration/artifacts/ecosystem/cran/testdata")
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
