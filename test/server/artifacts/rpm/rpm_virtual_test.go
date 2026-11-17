package rpm_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccRPMVirtualRepodataWalksRemoteAndNestedMembers (#1551): through
// the assembled server a virtual repository over [remote, nested virtual
// -> local], whose nested member lists the outer one again, merges the
// remote's and the local's repodata and skips the cycle (decision 0008,
// R2), where it answered 500. The remote's reader, who may read no other
// member and no virtual repository, reads the merge through it (R3), where
// it was refused 403; a user who may read no member is still refused. A
// second read within the virtual repository's period serves the same set.
func TestAccRPMVirtualRepodataWalksRemoteAndNestedMembers(t *testing.T) {
	dir := repopath.Path(t, "test/acceptance/testdata/rpm-upstream/repodata")
	files := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files["repodata/"+e.Name()] = body
	}
	r := chi.NewRouter()
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[chi.URLParam(r, "*")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	up := httptest.NewServer(r)
	t.Cleanup(up.Close)
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.RPM
	w.Repo.URL = up.URL
	w.Update()
	for _, c := range []repo.Config{
		{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM},
		{
			Key: "rpm-inner", Class: repo.Virtual, PackageType: repo.RPM,
			Repositories: []string{"rpm-local", "rpm-virtual"},
		},
		{
			Key: "rpm-virtual", Class: repo.Virtual, PackageType: repo.RPM,
			Repositories: []string{"remote", "rpm-inner"},
		},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range files {
		seed(t, w, "rpm-local", p, body)
	}

	first := w.Request(http.MethodGet, "/artifactory/rpm-virtual/repodata/repomd.xml", "admin", "")
	if got := listedBy(t, w, "admin", first); len(got) != 1 {
		t.Fatalf("the merged repodata lists %v, want the one package", got)
	}
	again := w.Request(http.MethodGet, "/artifactory/rpm-virtual/repodata/repomd.xml", "admin", "")
	if again.Code != http.StatusOK || !bytes.Equal(again.Body.Bytes(), first.Body.Bytes()) {
		t.Errorf("a second read within the period answered %d with other bytes", again.Code)
	}
	reader := w.Request(http.MethodGet, "/artifactory/rpm-virtual/repodata/repomd.xml", "reader", "")
	if got := listedBy(t, w, "reader", reader); len(got) != 1 {
		t.Errorf("the remote's reader got %v through the virtual repository", got)
	}
	wantRefusal(t, w.Request(http.MethodGet, "/artifactory/rpm-virtual/repodata/repomd.xml", "denied", ""),
		http.StatusForbidden, "Forbidden")
}

// listedBy decodes the virtual repository's repomd.xml, answered in rec,
// and the primary it names, read by user, into the packages' paths.
func listedBy(t *testing.T, w *harness.RemoteWorld, user string, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("repomd.xml answered %d %s, want 200", rec.Code, rec.Body)
	}
	primaryPath, sum, err := rpm.DecodeRepomd(rec.Body.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	res := w.Request(http.MethodGet, "/artifactory/rpm-virtual/"+primaryPath, user, "")
	packages, err := rpm.DecodePrimary(res.Body.Bytes(), "", sum)
	if res.Code != http.StatusOK || err != nil {
		t.Fatalf("primary answered %d (%v), want 200", res.Code, err)
	}
	var paths []string
	for _, p := range packages {
		paths = append(paths, p.Path)
	}
	return paths
}

// seed stores body as the file at path in the repository key of the
// world's runtime, blob first, as a deploy leaves it.
func seed(t *testing.T, w *harness.RemoteWorld, key, path string, body []byte) {
	t.Helper()
	upload, err := w.Runtime.Blobs.BeginUpload(t.Context())
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
	if _, err := w.Runtime.Store.Items().Put(t.Context(), model.Item{
		RepoKey: key, Name: path, Size: info.Size,
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256},
	}); err != nil {
		t.Fatal(err)
	}
}
