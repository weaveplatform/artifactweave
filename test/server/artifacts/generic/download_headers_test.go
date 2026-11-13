package generic_test

// This file holds the download header suites of #1266 and #1267: a box's
// media type, and a file's ETag and the conditional requests that echo it,
// on the item route and on the Vagrant box file route, which serves the
// item download (Kit.ServeItem).

import (
	"archive/tar"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const boxType = "application/x-vagrant-box"

// downloadWorld is harness.NewItemWorld with the Vagrant repository boxes,
// a box deployed through the item API at boxes/acme/focal-1.0.box and the
// same bytes at libs-local/dir/file.bin. It returns the bytes and their
// SHA-1.
func downloadWorld(t *testing.T) (*harness.ItemWorld, []byte, string) {
	t.Helper()
	w := harness.NewItemWorld(t, false)
	if err := w.DB.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "boxes", Class: repo.Local, PackageType: repo.Vagrant,
	})); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range []struct{ name, body string }{
		{"metadata.json", `{"provider":"virtualbox","architecture":"amd64"}`},
		{"box.ovf", "provider fixture"},
	} {
		if err := tw.WriteHeader(
			&tar.Header{Name: e.name, Size: int64(len(e.body)), Mode: 0o644},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	for _, p := range []string{
		"/artifactory/boxes/acme/focal-1.0.box;box_name=focal;box_provider=virtualbox;box_version=1.0",
		"/artifactory/libs-local/dir/file.bin",
	} {
		harness.WantStatus(t, w.Do(http.MethodPut, p, "admin", string(data)), http.StatusCreated)
	}
	sum := sha1.Sum(data)
	return w, data, hex.EncodeToString(sum[:])
}

// TestAccBoxDownloadsAsAVagrantBox: a .box deployed through the item API
// downloads as application/x-vagrant-box, the type 7.37.15 answered the box
// file route with (RTFACT-27264) and Artifactory's default MIME table gives
// the extension, on the item route and on the box file route, and its
// storage info says so. Another extension keeps its type.
func TestAccBoxDownloadsAsAVagrantBox(t *testing.T) {
	w, data, _ := downloadWorld(t)
	harness.WantStatus(
		t,
		w.Do(http.MethodPut, "/artifactory/libs-local/org/app/app-1.0.jar", "admin", "jar"),
		http.StatusCreated,
	)
	for _, c := range []struct{ path, mediaType, body string }{
		{"/artifactory/boxes/acme/focal-1.0.box", boxType, string(data)},
		{"/artifactory/api/vagrant/boxes/acme/focal-1.0.box", boxType, string(data)},
		{"/artifactory/libs-local/dir/file.bin", "application/octet-stream", string(data)},
		{"/artifactory/libs-local/org/app/app-1.0.jar", "application/java-archive", "jar"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.Do(method, c.path, "reader", "")
			harness.WantStatus(t, rec, http.StatusOK)
			want := c.body
			if method == http.MethodHead {
				want = ""
			}
			got := rec.Header().Get("Content-Type")
			if got != c.mediaType || rec.Body.String() != want {
				t.Errorf("%s %s: Content-Type %q, body of %d bytes; want %q, %d bytes",
					method, c.path, got, rec.Body.Len(), c.mediaType, len(want))
			}
		}
	}
	for path, want := range map[string]string{
		"/artifactory/api/storage/boxes/acme/focal-1.0.box":       boxType,
		"/artifactory/api/storage/libs-local/dir/file.bin":        "application/octet-stream",
		"/artifactory/api/storage/libs-local/org/app/app-1.0.jar": "application/java-archive",
	} {
		rec := w.Do(http.MethodGet, path, "reader", "")
		harness.WantStatus(t, rec, http.StatusOK)
		var info struct {
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.MimeType != want {
			t.Errorf("%s: mimeType %q (%v), want %q", path, info.MimeType, err, want)
		}
	}
}

// TestAccFileDownloadSendsTheBareSHA1AsETag: a file's ETag is its bare
// SHA-1, equal to X-Checksum-Sha1, as 7.x sends it (RTFACT-30785,
// RTFACT-29977, RTFACT-27264), on the item route and on a protocol route.
// If-None-Match naming it bare or quoted answers 304, and If-Range with it
// the range asked for. What answered otherwise before still does: another
// tag, a plain or unsatisfiable range and If-Modified-Since. If-Match is
// ignored, bare or quoted (#1314).
func TestAccFileDownloadSendsTheBareSHA1AsETag(t *testing.T) {
	w, data, sha := downloadWorld(t)
	quoted := `"` + sha + `"`
	size := strconv.Itoa(len(data))
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	type answer struct {
		status       int
		body         string
		contentRange string
		etag         bool
	}
	whole := answer{http.StatusOK, string(data), "", true}
	notModified := answer{http.StatusNotModified, "", "", true}
	part := answer{http.StatusPartialContent, string(data[2:6]), "bytes 2-5/" + size, true}
	for _, path := range []string{
		"/artifactory/libs-local/dir/file.bin",
		"/artifactory/boxes/acme/focal-1.0.box",
		"/artifactory/api/vagrant/boxes/acme/focal-1.0.box",
	} {
		for _, c := range []struct {
			name    string
			method  string
			headers []string
			want    answer
		}{
			{"GET", http.MethodGet, nil, whole},
			{"HEAD", http.MethodHead, nil, answer{http.StatusOK, "", "", true}},
			{"If-None-Match bare", http.MethodGet, []string{"If-None-Match", sha}, notModified},
			{"If-None-Match quoted", http.MethodGet, []string{"If-None-Match", quoted}, notModified},
			{"If-None-Match weak bare", http.MethodGet, []string{"If-None-Match", "W/" + sha}, notModified},
			{"If-None-Match weak quoted", http.MethodGet, []string{"If-None-Match", "W/" + quoted}, notModified},
			{"If-None-Match list", http.MethodGet, []string{"If-None-Match", `"other", ` + sha}, notModified},
			{"HEAD If-None-Match bare", http.MethodHead, []string{"If-None-Match", sha}, notModified},
			{"If-None-Match other", http.MethodGet, []string{"If-None-Match", `"other"`}, whole},
			{"If-Range bare", http.MethodGet, []string{"Range", "bytes=2-5", "If-Range", sha}, part},
			{"If-Range quoted", http.MethodGet, []string{"Range", "bytes=2-5", "If-Range", quoted}, part},
			{"If-Range other", http.MethodGet, []string{"Range", "bytes=2-5", "If-Range", `"other"`}, whole},
			{"Range", http.MethodGet, []string{"Range", "bytes=2-5"}, part},
			{
				"Range unsatisfiable", http.MethodGet,
				[]string{"Range", "bytes=" + size + "-"},
				answer{http.StatusRequestedRangeNotSatisfiable, "invalid range: failed to overlap\n", "bytes */" + size, false},
			},
			{"If-Match quoted", http.MethodGet, []string{"If-Match", quoted}, whole},
			{"If-Match bare", http.MethodGet, []string{"If-Match", sha}, whole},
			{"If-Modified-Since", http.MethodGet, []string{"If-Modified-Since", future}, notModified},
		} {
			rec := w.Do(c.method, path, "reader", "", c.headers...)
			h := rec.Header()
			etag := ""
			if c.want.etag {
				etag = sha
			}
			if rec.Code != c.want.status || rec.Body.String() != c.want.body ||
				h.Get("Content-Range") != c.want.contentRange || h.Get("ETag") != etag ||
				(c.want.etag && h.Get("X-Checksum-Sha1") != sha) {
				t.Errorf("%s %s: %d, ETag %q, Content-Range %q, body of %d bytes",
					path, c.name, rec.Code, h.Get("ETag"), h.Get("Content-Range"), rec.Body.Len())
				t.Errorf("want %d, ETag %q, Content-Range %q, body of %d bytes",
					c.want.status, etag, c.want.contentRange, len(c.want.body))
			}
		}
	}
}

// TestAccRedirectedFileAnswersTheBareETag: a repository that redirects
// downloads answers a conditional GET itself (decision 0029), so the bare
// SHA-1 it hands out on HEAD answers 304 there too; the 302 carries no
// ETag.
func TestAccRedirectedFileAnswersTheBareETag(t *testing.T) {
	w := harness.NewItemWorld(t, false)
	const p = "/artifactory/redirect-local/r.bin"
	harness.WantStatus(t, w.Do(http.MethodPut, p, "admin", "redirected"), http.StatusCreated)
	sum := sha1.Sum([]byte("redirected"))
	sha := hex.EncodeToString(sum[:])
	head := w.Do(http.MethodHead, p, "reader", "")
	harness.WantStatus(t, head, http.StatusOK)
	if head.Header().Get("ETag") != sha {
		t.Fatalf("HEAD ETag %q, want %q", head.Header().Get("ETag"), sha)
	}
	get := w.Do(http.MethodGet, p, "reader", "")
	harness.WantStatus(t, get, http.StatusFound)
	if get.Header().Get("ETag") != "" {
		t.Fatalf("302 ETag %q", get.Header().Get("ETag"))
	}
	for _, tag := range []string{sha, `"` + sha + `"`} {
		rec := w.Do(http.MethodGet, p, "reader", "", "If-None-Match", tag)
		harness.WantStatus(t, rec, http.StatusNotModified)
		if rec.Header().Get("ETag") != sha || rec.Header().Get("Location") != "" {
			t.Fatalf("If-None-Match %s: %v", tag, rec.Header())
		}
	}
}
