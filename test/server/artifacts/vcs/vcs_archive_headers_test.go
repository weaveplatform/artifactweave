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
	"strings"
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

// wantArchiveHeaders fails unless rec, a GET of an archive whose bytes are
// body, answered 200 with exactly the headers 7.x sends on a downloadTag
// (bazelbuild/bazel#22421, conan-io/conan#11656): media,
// X-Artifactory-Filename and Content-Disposition naming the archive name,
// the length and the three checksums, beside the request id, and the
// validators every 7.x file download carries: the bare SHA-1 as the ETag,
// the archive's time and Accept-Ranges [I].
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
		"Etag":                   hex.EncodeToString(s1[:]),
		"Accept-Ranges":          "bytes",
		"Last-Modified":          rec.Header().Get("Last-Modified"),
	}
	if _, err := http.ParseTime(rec.Header().Get("Last-Modified")); err != nil {
		t.Errorf("%s: Last-Modified %q: %v", label, rec.Header().Get("Last-Modified"), err)
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
			// every generated route's does (#1598).
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

// vcsArchiveWorld is a GitHub remote "remote" and a GitLab remote "gitlab"
// over one upstream that holds the test commit's tar.gz archive and refs.
func vcsArchiveWorld(t *testing.T) *harness.RemoteWorld {
	t.Helper()
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
	archive := fakeupstream.Resource{Body: vcsTar(t)}
	for _, project := range []string{"/team/project", "/team/sub/project"} {
		w.Upstream.Set(project+".git/info/refs", fakeupstream.Resource{Body: vcsTestRefs()})
	}
	w.Upstream.Set("/team/project/archive/"+vcsTestOID+".tar.gz", archive)
	w.Upstream.Set(
		"/team/sub/project/-/archive/"+vcsTestOID+"/project-"+vcsTestOID+".tar.gz",
		archive,
	)
	return w
}

// TestAccVCSDownloadsAnswerValidatorsAndRanges: an archive or a selected
// file, by a generated route or in the nested subgroup form, answers as a
// 7.x file download does [I]: a 304 to its bare or quoted SHA-1 or its
// time, carrying the ETag; a range with 206 and Content-Range, also under a
// matching If-Range and whole under another; and 416 with Content-Range
// alone past its end. The downloads answered every one of them 200 with
// the whole body. A HEAD answers per #1598.
func TestAccVCSDownloadsAnswerValidatorsAndRanges(t *testing.T) {
	w := vcsArchiveWorld(t)
	const base = "/artifactory/api/vcs/"
	for _, path := range []string{
		base + "downloadTag/remote/team/project/v1.0.0",
		base + "downloadTag/gitlab/team/sub/project/archive/refs/v1.0.0",
		base + "downloadBranchFile/remote/team/project/main!README.md",
		base + "downloadBranchFile/gitlab/team/sub/project/archive/refs/main!README.md",
		base + "downloadTagFile/remote/team/project/v1.0.0!src/main.go?ext=zip",
	} {
		full := w.Request(http.MethodGet, path, "admin", "")
		wantStatus(t, full, http.StatusOK)
		body := full.Body.Bytes()
		etag, modified := full.Header().Get("Etag"), full.Header().Get("Last-Modified")
		if s1 := sha1.Sum(
			body,
		); etag != hex.EncodeToString(s1[:]) ||
			modified == "" { //nolint:gosec // the ETag 7.x sends
			t.Fatalf("%s: ETag %q, Last-Modified %q", path, etag, modified)
		}
		size := strconv.Itoa(len(body))
		for _, tc := range []struct {
			name    string
			headers []string
			status  int
			body    []byte
			span    string
		}{
			{"bare tag", []string{"If-None-Match", etag}, http.StatusNotModified, nil, ""},
			{"quoted tag", []string{"If-None-Match", `"` + etag + `"`}, http.StatusNotModified, nil, ""},
			{"time", []string{"If-Modified-Since", modified}, http.StatusNotModified, nil, ""},
			{"other tag", []string{"If-None-Match", `"other"`}, http.StatusOK, body, ""},
			{"range", []string{"Range", "bytes=0-3"}, http.StatusPartialContent, body[:4], "bytes 0-3/" + size},
			{
				"if-range",
				[]string{"Range", "bytes=0-3", "If-Range", etag},
				http.StatusPartialContent, body[:4], "bytes 0-3/" + size,
			},
			{"if-range other", []string{"Range", "bytes=0-3", "If-Range", `"other"`}, http.StatusOK, body, ""},
			{
				"past the end",
				[]string{"Range", "bytes=" + size + "-"},
				http.StatusRequestedRangeNotSatisfiable, nil, "bytes */" + size,
			},
		} {
			rec := w.Request(http.MethodGet, path, "admin", "", tc.headers...)
			label := path + " " + tc.name
			wantStatus(t, rec, tc.status)
			if !bytes.Equal(rec.Body.Bytes(), tc.body) && len(tc.body)+rec.Body.Len() > 0 {
				t.Errorf("%s: %d bytes of body", label, rec.Body.Len())
			}
			if got := rec.Header().Get("Content-Range"); got != tc.span {
				t.Errorf("%s: Content-Range %q, want %q", label, got, tc.span)
			}
			switch tc.status {
			case http.StatusNotModified:
				if rec.Header().Get("Etag") != etag {
					t.Errorf("%s: ETag %q", label, rec.Header().Get("Etag"))
				}
			case http.StatusRequestedRangeNotSatisfiable:
				if rec.Header().Get("Etag") != "" || rec.Header().Get("X-Checksum-Sha1") != "" {
					t.Errorf("%s: headers %v", label, rec.Header())
				}
			}
		}
	}
}

// TestAccVCSSelectedFileAnswersTheFile (#1580): a file within a tag or
// branch answers the file itself, as 7.x does (RTFACT-29495), typed by its
// extension and named by its base name, with its checksums; with ext=zip it
// answers the ZIP that holds it at its path, named <file>.zip [I], as
// JFrog's VCS page offers "as a zip". A path the archive does not hold
// answers 404. The downloads answered every file as a ZIP.
func TestAccVCSSelectedFileAnswersTheFile(t *testing.T) {
	w := vcsArchiveWorld(t)
	const base = "/artifactory/api/vcs/"
	for _, tc := range []struct{ path, name, media, body string }{
		{"downloadTagFile/remote/team/project/v1.0.0!README.md", "README.md", "text/plain", "module example\n"},
		{"downloadBranchFile/remote/team/project/main!src/main.go", "main.go", "application/octet-stream", "package main\n"},
		{
			"downloadTagFile/gitlab/team/sub/project/archive/refs/v1.0.0!vendor/pkg/README.md",
			"README.md", "text/plain", "vendored\n",
		},
	} {
		for _, read := range []string{"first fetch", "cached"} {
			rec := w.Request(http.MethodGet, base+tc.path, "admin", "")
			wantArchiveHeaders(t, tc.path+" "+read, rec, []byte(tc.body), tc.media, tc.name)
			if rec.Body.String() != tc.body {
				t.Errorf("%s %s: %q", tc.path, read, rec.Body)
			}
		}
		rec := w.Request(http.MethodGet, base+tc.path+"?ext=zip", "admin", "")
		wantArchiveHeaders(
			t,
			tc.path+"?ext=zip",
			rec,
			rec.Body.Bytes(),
			"application/zip",
			tc.name+".zip",
		)
		z, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		_, file, _ := strings.Cut(tc.path, "!")
		if err != nil || len(z.File) != 1 || z.File[0].Name != file {
			t.Fatalf("%s?ext=zip: %v", tc.path, err)
		}
	}
	wantStatus(
		t,
		w.Request(
			http.MethodGet,
			base+"downloadTagFile/remote/team/project/v1.0.0!ONLY.md",
			"admin",
			"",
		),
		http.StatusNotFound,
	)
}

// TestAccVCSPeerSelectedFilePassesTheFileThrough: a remote over a peer
// Artifactory VCS repository asks it for the file itself, with no ext, and
// answers those bytes with the file's own headers; ext=zip zips them here.
// The remote asked the peer for ext=zip and validated a ZIP.
func TestAccVCSPeerSelectedFilePassesTheFileThrough(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "peer", Class: repo.Remote, PackageType: repo.VCS,
		URL: w.Upstream.URL + "/artifactory/api/vcs/source", VCSGitProvider: repo.ArtifactoryVCS,
	})); err != nil {
		t.Fatal(err)
	}
	const peer = "/artifactory/api/vcs/downloadBranchFile/source/team/project/main!conf/registry.json"
	w.Upstream.Set(peer, fakeupstream.Resource{Body: []byte(`{"mirrors":[]}`)})
	path := "/artifactory/api/vcs/downloadBranchFile/peer/team/project/main!conf/registry.json"
	rec := w.Request(http.MethodGet, path, "admin", "")
	wantArchiveHeaders(t, path, rec, []byte(`{"mirrors":[]}`), "application/json", "registry.json")
	zipped := w.Request(http.MethodGet, path+"?ext=zip", "admin", "")
	wantArchiveHeaders(
		t,
		path+"?ext=zip",
		zipped,
		zipped.Body.Bytes(),
		"application/zip",
		"registry.json.zip",
	)
	if n := w.Upstream.Count(http.MethodGet, peer); n != 1 {
		t.Errorf("peer asked %d times, want 1", n)
	}
}
