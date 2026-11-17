package opkg_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccOpkgVirtualFeedWalksRemoteAndNestedMembers (#1551): through the
// assembled server a virtual repository over [remote, nested virtual ->
// local], whose nested member lists the outer one again, merges the
// remote's and the local's feeds and skips the cycle (decision 0008, R2),
// where it answered 500. The remote's reader, who may read no other member
// and no virtual repository, reads the feed and installs the remote's IPK
// through it (R3), where it was refused 403; a user who may read no member
// is still refused.
func TestAccOpkgVirtualFeedWalksRemoteAndNestedMembers(t *testing.T) {
	const feed, xlat = "feed/Packages", "feed/464xlat_13_x86_64.ipk"
	index := repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/openwrt-Packages")
	ipk := repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/464xlat_13_x86_64.ipk")
	files := map[string][]byte{feed: index, xlat: ipk}
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
	w.Repo.PackageType = repo.Opkg
	w.Repo.URL = up.URL
	w.Update()
	for _, c := range []repo.Config{
		{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg},
		{
			Key: "opkg-inner", Class: repo.Virtual, PackageType: repo.Opkg,
			Repositories: []string{"opkg-local", "opkg-virtual"},
		},
		{
			Key: "opkg-virtual", Class: repo.Virtual, PackageType: repo.Opkg,
			Repositories: []string{"remote", "opkg-inner"},
		},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, w, "opkg-local", feed, index)

	for _, user := range []string{"admin", "reader"} {
		res := w.Request(http.MethodGet, "/artifactory/opkg-virtual/"+feed, user, "")
		if res.Code != http.StatusOK {
			t.Fatalf("%s's feed answered %d %s, want 200", user, res.Code, res.Body)
		}
		packages, err := opkg.DecodeIndex(res.Body.Bytes(), feed)
		if err != nil || len(packages) != 2 {
			t.Errorf("%s's feed lists %v (%v), want the two packages", user, packages, err)
		}
	}
	res := w.Request(http.MethodGet, "/artifactory/opkg-virtual/"+xlat, "reader", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), ipk) {
		t.Errorf("the remote's IPK through the virtual repository answered %d", res.Code)
	}
	wantRefusal(t, w.Request(http.MethodGet, "/artifactory/opkg-virtual/"+feed, "denied", ""),
		http.StatusForbidden, "Forbidden")
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

// wantRefusal fails the test unless rec answered status with the error
// envelope that carries message.
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
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
