package vcs_test

import (
	"archive/zip"
	"bytes"
	"crypto/md5"  //nolint:gosec // the checksum header 7.x sends
	"crypto/sha1" //nolint:gosec // the checksum header 7.x sends
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// vcsZip is a provider's zip archive of the test commit's tree.
func vcsZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"project-012345/README.md":   "module example\n",
		"project-012345/src/main.go": "package main\n",
	} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// wantArchiveHeaders fails unless rec, a GET or HEAD of an archive whose
// bytes are body, answered 200 with exactly the headers 7.x sends on a
// downloadTag (bazelbuild/bazel#22421, conan-io/conan#11656): media,
// X-Artifactory-Filename and Content-Disposition naming the archive name,
// the length and the three checksums, beside the request id.
func wantArchiveHeaders(
	t *testing.T,
	label string,
	rec *httptest.ResponseRecorder,
	body []byte,
	media, name string,
) {
	t.Helper()
	wantStatus(t, rec, http.StatusOK)
	s1, s256, m5 := sha1.Sum(
		body,
	), sha256.Sum256(
		body,
	), md5.Sum(
		body,
	) //nolint:gosec // checksum headers
	want := map[string]string{
		"Content-Type":           media,
		"Content-Length":         strconv.Itoa(len(body)),
		"X-Artifactory-Filename": name,
		"Content-Disposition":    `attachment; filename="` + name + `"`,
		"X-Checksum-Sha1":        hex.EncodeToString(s1[:]),
		"X-Checksum-Sha256":      hex.EncodeToString(s256[:]),
		"X-Checksum-Md5":         hex.EncodeToString(m5[:]),
	}
	var got, names []string
	for k := range rec.Header() {
		if k != "X-Request-Id" {
			got = append(got, k)
		}
	}
	for k, v := range want {
		names = append(names, k)
		if rec.Header().Get(k) != v {
			t.Errorf("%s: %s %q, want %q", label, k, rec.Header().Get(k), v)
		}
	}
	slices.Sort(got)
	slices.Sort(names)
	if !slices.Equal(got, names) {
		t.Errorf("%s: headers %v, want %v", label, got, names)
	}
}

// TestAccVCSArchiveDownloadsNameAndTypeTheArchive (#1580): every archive
// download, tag, release, branch and commit, in each path form, tar.gz and
// zip, on its first fetch and from the cache, answers the
// archive's type by its extension, its name in X-Artifactory-Filename and
// Content-Disposition, and its checksums. A tag archive answered
// X-Artifactory-Filename: rules_java-7.5.0.tar.gz, Content-Type:
// application/x-gzip and Content-Disposition: attachment;
// filename="rules_java-7.5.0.tar.gz" (bazelbuild/bazel#22421), with
// X-Checksum-Sha256 (conan-io/conan#11656); 7.x's GitRef names a branch's
// archive <repo>-<branch>-<commit>.<ext>. The downloads answered
// application/octet-stream with none of them.
func TestAccVCSArchiveDownloadsNameAndTypeTheArchive(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.VCS, URL: w.Upstream.URL,
		VCSGitProvider: repo.GitHub,
	})
	w.Update()
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "gitlab", Class: repo.Remote, PackageType: repo.VCS, URL: w.Upstream.URL,
		VCSGitProvider: repo.GitLab,
	})); err != nil {
		t.Fatal(err)
	}
	archives := map[string][]byte{"tar.gz": vcsTar(t), "zip": vcsZip(t)}
	media := map[string]string{"tar.gz": "application/x-gzip", "zip": "application/zip"}
	for _, project := range []string{"/team/project", "/team/team", "/team/sub/project"} {
		w.Upstream.Set(project+".git/info/refs", fakeupstream.Resource{Body: vcsTestRefs()})
	}
	for ext, body := range archives {
		resource := fakeupstream.Resource{Body: body}
		w.Upstream.Set("/team/project/archive/"+vcsTestOID+"."+ext, resource)
		w.Upstream.Set("/team/team/archive/"+vcsTestOID+"."+ext, resource)
		w.Upstream.Set("/team/sub/project/-/archive/"+vcsTestOID+"/project-"+vcsTestOID+"."+ext,
			resource)
	}
	const base = "/artifactory/api/vcs/"
	for _, tc := range []struct{ path, name string }{
		{"downloadTag/remote/team/project/v1.0.0", "project-v1.0.0"},
		{"downloadRelease/remote/team/project/v1.0.0", "project-v1.0.0"},
		{"downloadTag/remote/team/v1.0.0", "team-v1.0.0"},
		{"downloadTag/remote/team/project/archive/refs/v1.0.0", "project-v1.0.0"},
		{"downloadTag/gitlab/team/sub/project/archive/refs/v1.0.0", "project-v1.0.0"},
		{"downloadBranch/remote/team/project/main", "project-main-" + vcsTestOID},
		{"downloadBranch/remote/team/main", "team-main-" + vcsTestOID},
		{"downloadBranch/remote/team/project/archive/refs/main", "project-main-" + vcsTestOID},
		{"downloadBranch/gitlab/team/sub/project/archive/refs/main", "project-main-" + vcsTestOID},
		{"downloadCommit/remote/team/project/" + vcsTestOID, "project-" + vcsTestOID},
	} {
		for _, ext := range []string{"tar.gz", "zip"} {
			path := base + tc.path + "?ext=" + ext
			// The first read fetches the archive and the second serves the
			// cached copy, which keeps the name. A HEAD answers 405, as
			// every generated route's does (drafted on its own).
			for _, read := range []string{"first fetch", "cached"} {
				rec := w.Request(http.MethodGet, path, "admin", "")
				wantArchiveHeaders(t, "GET "+path+" "+read, rec, archives[ext], media[ext],
					tc.name+"."+ext)
				if !bytes.Equal(rec.Body.Bytes(), archives[ext]) {
					t.Errorf("GET %s %s: %d bytes of body", path, read, rec.Body.Len())
				}
			}
		}
	}
	// Each of the tag, the branch and the commit is fetched once, then
	// served from the cache.
	for ext := range archives {
		archive := "/team/project/archive/" + vcsTestOID + "." + ext
		if n := w.Upstream.Count(http.MethodGet, archive); n != 3 {
			t.Errorf("%s fetched %d times, want 3", archive, n)
		}
	}
}
