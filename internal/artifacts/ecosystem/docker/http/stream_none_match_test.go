package dockerhttp

import (
	"crypto/md5" //nolint:gosec // the wire checksum 7.x compares
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// A blob the registry streams itself, for a server without
// Options.ServeBlob, by both addressing forms, GET and HEAD, answers
// If-None-Match and If-Modified-Since as the server's download does
// (#1483): each If-None-Match line on its own, less a W/ prefix and every
// quote, against the blob's SHA-1, or its MD5 when
// X-Artifactory-If-None-Match-MD5 is true, and If-Modified-Since only
// without If-None-Match (DownloadServiceImpl.isNotModified, 7.161.26). Its
// answers are otherwise as they were: no ETag, and a 304 with
// Last-Modified and without the body's headers.
func TestAccStreamedBlobNoneMatchAsTheDownload(t *testing.T) {
	w := newBlobWorld(t)
	const layer = "a layer the registry streams"
	d := digestOf(layer)
	expect(t, w.do(http.MethodPost, "/v2/docker-local/team/app/blobs/uploads/?digest="+d,
		layer, nil), http.StatusCreated, "")
	sha1 := sha1Of(layer)
	sum := md5.Sum([]byte(layer)) //nolint:gosec // the wire checksum
	md5hex := hex.EncodeToString(sum[:])
	const after, before = "Fri, 01 Jan 2100 00:00:00 GMT", "Mon, 01 Jan 2001 00:00:00 GMT"
	for _, c := range []struct {
		label       string
		lines       [][2]string
		notModified bool
	}{
		// Changed by #1483.
		{"If-None-Match *", [][2]string{{"If-None-Match", "*"}}, false},
		{"If-None-Match second line", [][2]string{
			{"If-None-Match", `"other"`}, {"If-None-Match", sha1},
		}, true},
		{"If-None-Match MD5", [][2]string{
			{"If-None-Match", md5hex}, {"X-Artifactory-If-None-Match-MD5", "true"},
		}, true},
		{"If-None-Match bare", [][2]string{{"If-None-Match", sha1}}, true},
		{"If-None-Match quoted", [][2]string{{"If-None-Match", `"` + sha1 + `"`}}, true},
		{"If-None-Match weak", [][2]string{{"If-None-Match", "W/" + sha1}}, true},
		// Kept.
		{"plain", nil, false},
		{"If-None-Match list", [][2]string{{"If-None-Match", `"other", ` + sha1}}, false},
		{"If-None-Match MD5 without the flag", [][2]string{{"If-None-Match", md5hex}}, false},
		{"If-Modified-Since after", [][2]string{{"If-Modified-Since", after}}, true},
		{"If-Modified-Since before", [][2]string{{"If-Modified-Since", before}}, false},
		{"If-None-Match other If-Modified-Since after", [][2]string{
			{"If-None-Match", `"other"`}, {"If-Modified-Since", after},
		}, false},
		{"If-Match other", [][2]string{{"If-Match", `"other"`}}, false},
		{"Range", [][2]string{{"Range", "bytes=1-3"}}, false},
	} {
		for _, target := range []string{
			"/v2/docker-local/team/app/blobs/" + d,
			"/artifactory/api/docker/docker-local/v2/team/app/blobs/" + d,
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req := httptest.NewRequest(method, target, nil)
				for _, l := range c.lines {
					req.Header.Add(l[0], l[1])
				}
				rec := httptest.NewRecorder()
				w.h.ServeHTTP(rec, req)
				h := rec.Header()
				label := method + " " + target + " " + c.label
				if h.Get("ETag") != "" || h.Get(contentDigestHeader) != d ||
					h.Get("Last-Modified") == "" {
					t.Fatalf("%s: headers %v", label, h)
				}
				switch {
				case c.notModified:
					if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 ||
						h.Get("Content-Type") != "" || h.Get("Content-Length") != "" {
						t.Fatalf("%s: %d %q %v, want 304", label, rec.Code, rec.Body, h)
					}
				default:
					want := layer
					if method == http.MethodHead {
						want = ""
					}
					if rec.Code != http.StatusOK || rec.Body.String() != want ||
						h.Get("Content-Type") != "application/octet-stream" ||
						h.Get("Content-Length") != strconv.Itoa(len(layer)) {
						t.Fatalf("%s: %d %q %v, want 200", label, rec.Code, rec.Body, h)
					}
				}
			}
		}
	}
}
