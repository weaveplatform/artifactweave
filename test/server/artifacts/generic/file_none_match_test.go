package generic_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// noneMatchCase is a request's conditional header lines, in order, and
// whether the file download answers it 304.
type noneMatchCase struct {
	label       string
	lines       [][2]string
	notModified bool
}

// noneMatchCases are #1469's rows for a file whose SHA-1 is sha and whose
// MD5 is md5. 7.x's download compares each If-None-Match line as a whole,
// less a W/ prefix and its quotes, with the file's SHA-1, or with its MD5
// when X-Artifactory-If-None-Match-MD5 asks (DownloadServiceImpl
// .isNotModified and isNonMatch, ArtifactoryRequestBase.isNoneMatch and
// parseETagValue, Artifactory OSS 7.161.26 sources), so "*" and a list on
// one line match nothing and a second line can.
func noneMatchCases(sha, md5 string) []noneMatchCase {
	return []noneMatchCase{
		{"If-None-Match *", [][2]string{{"If-None-Match", "*"}}, false},
		{"a list naming the SHA-1", [][2]string{{"If-None-Match", `"other", ` + sha}}, false},
		{"the SHA-1 on a second line", [][2]string{
			{"If-None-Match", `"other"`}, {"If-None-Match", sha},
		}, true},
		{"the MD5, asked for", [][2]string{
			{"If-None-Match", md5}, {"X-Artifactory-If-None-Match-MD5", "true"},
		}, true},
		{"the MD5, not asked for", [][2]string{{"If-None-Match", md5}}, false},
	}
}

// sendLines serves a request carrying lines, each its own header line, as
// user with the password every harness principal has.
func sendLines(h http.Handler, method, target, user string, lines [][2]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.SetBasicAuth(user, "pw")
	for _, l := range lines {
		r.Header.Add(l[0], l[1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// wantNoneMatch fails t unless the file at target answers each of
// noneMatchCases as 7.x's download does, GET and HEAD alike, and, when
// checksums is true, as its .sha1 answers it; and a GET's range that does
// not overlap with 416, Content-Range alone and no body, as 7.x's
// RequestResponseHelper.sendBodyResponse answers it. A redirecting
// repository redirects what it would serve whole.
func wantNoneMatch(
	t *testing.T,
	h http.Handler,
	target, user, data string,
	file sums,
	redirects, checksums bool,
) {
	t.Helper()
	for _, c := range noneMatchCases(file.sha1, file.md5) {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			label := method + " " + target + " " + c.label
			rec := sendLines(h, method, target, user, c.lines)
			want := condAnswer{http.StatusOK, data, ""}
			etag := file.sha1
			switch {
			case c.notModified:
				want = condAnswer{http.StatusNotModified, "", ""}
			case method == http.MethodHead:
				want.body = ""
			case redirects:
				want, etag = condAnswer{http.StatusFound, "", ""}, ""
			}
			wantCondAnswer(t, label, rec, want, etag)
			if !checksums {
				continue
			}
			sum := sendLines(h, method, target+".sha1", user, c.lines)
			if wantSum := map[bool]int{true: http.StatusNotModified, false: http.StatusOK}[c.notModified]; sum.Code != wantSum {
				t.Errorf("%s: its .sha1 answered %d, want %d", label, sum.Code, wantSum)
			}
		}
	}
	if redirects {
		return
	}
	size := strconv.Itoa(len(data))
	rec := sendLines(h, http.MethodGet, target, user, [][2]string{{"Range", "bytes=1000-2000"}})
	wantCondAnswer(t, "GET "+target+" past the end", rec,
		condAnswer{http.StatusRequestedRangeNotSatisfiable, "", "bytes */" + size}, "")
	for _, k := range []string{
		"Content-Type", "X-Checksum-Sha1", "X-Artifactory-Filename",
		"Content-Disposition", "Accept-Ranges", "Last-Modified",
	} {
		if _, sent := rec.Header()[k]; sent {
			t.Errorf("GET %s past the end: %s %q sent", target, k, rec.Header().Get(k))
		}
	}
	rec = sendLines(h, http.MethodHead, target, user, [][2]string{{"Range", "bytes=1000-2000"}})
	wantCondAnswer(t, "HEAD "+target+" past the end", rec, condAnswer{http.StatusOK, "", ""}, file.sha1)
}

// TestAccFileDownloadAnswersIfNoneMatchAsTheDownload: a file on a local, a
// virtual and a redirecting repository answers If-None-Match and a range
// past its end as 7.x's download does, as its checksum file has since
// #1459 (#1469).
func TestAccFileDownloadAnswersIfNoneMatchAsTheDownload(t *testing.T) {
	w, _, _ := downloadWorld(t)
	const data = "a file whose download is asked for on a condition"
	file := fileSums(data)
	// The repositories keep client checksums, so the deploy sends them for
	// the checksum files to answer.
	for _, key := range []string{"libs-local", "redirect-local"} {
		harness.WantStatus(t, w.Do(http.MethodPut, "/artifactory/"+key+"/dir/match.bin", "admin", data,
			"X-Checksum-Sha1", file.sha1, "X-Checksum-Sha256", file.sha256), http.StatusCreated)
	}
	wantNoneMatch(t, w.H, "/artifactory/libs-local/dir/match.bin", "reader", data, file, false, true)
	wantNoneMatch(t, w.H, "/artifactory/virt/dir/match.bin", "reader", data, file, false, true)
	wantNoneMatch(t, w.H, "/artifactory/redirect-local/dir/match.bin", "reader", data, file, true, true)
}

// TestAccRemoteFileDownloadAnswersIfNoneMatchAsTheDownload: a remote file
// answers as a local one, on the first fetch and again, whether the
// upstream's stream serves it (a remote that stores nothing) or the cached
// copy does. The upstream sends the file's checksums, and its SHA-1 as
// the ETag, as Artifactory does, so a HEAD that streams nothing knows the
// SHA-1, and the stream's ETag, the upstream's, is the cached copy's. The
// checksum files of a remote that stores nothing are the upstream's, so
// only the cached copy's are compared (#1469).
func TestAccRemoteFileDownloadAnswersIfNoneMatchAsTheDownload(t *testing.T) {
	modified := time.Unix(1700000000, 0).UTC()
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			if !store {
				w.Repo.StoreArtifactsLocally = new(false)
				w.Update()
			}
			for _, pass := range []string{"first", "again"} {
				data := "a remote file asked for on a condition, " + pass
				file := fileSums(data)
				name := "/dir/match-" + pass + ".bin"
				w.Upstream.Set(name, fakeupstream.Resource{
					Body: []byte(data), Modified: modified, ETag: file.sha1,
					Header: http.Header{
						"X-Checksum-Sha1": {file.sha1}, "X-Checksum-Sha256": {file.sha256},
						"X-Checksum-Md5": {file.md5},
					}})
				target := "/artifactory/remote" + name
				if pass == "again" {
					harness.WantStatus(t, w.Request(http.MethodGet, target, "reader", ""), http.StatusOK)
				}
				wantNoneMatch(t, w.H, target, "reader", data, file, false, store && pass == "again")
			}
		})
	}
}
