package generic_test

// This file holds the cold HEAD suite of #1391: a HEAD on a remote that
// stores artifacts locally downloads and caches the file as a GET does and
// is answered from the copy kept, while a remote that stores nothing
// still sends the upstream a HEAD only.

import (
	"crypto/sha1" //nolint:gosec // 7.x's ETag and X-Checksum-Sha1 are the file's SHA-1.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// coldJar is a .jar its upstream labels application/zip, as S3 and
// Apache's mime.types label one.
const coldJar = "jar bytes, labelled as a zip upstream"

// wantFileHead holds rec, a HEAD's answer, to the headers 7.x's
// sendHeadResponse gives the file body, modified at modified: the jar's
// own type, its length, its SHA-1 as the ETag and its checksums, and no
// body.
func wantFileHead(
	t *testing.T,
	label string,
	rec *httptest.ResponseRecorder,
	body string,
	modified time.Time,
) {
	t.Helper()
	remoteStatus(t, rec, http.StatusOK)
	sha1Sum := sha1.Sum([]byte(body)) //nolint:gosec // 7.x's ETag is the file's SHA-1.
	sha256Sum := sha256.Sum256([]byte(body))
	h := rec.Header()
	for name, want := range map[string]string{
		"Content-Type":      javaArchive,
		"Content-Length":    strconv.Itoa(len(body)),
		"ETag":              hex.EncodeToString(sha1Sum[:]),
		"X-Checksum-Sha1":   hex.EncodeToString(sha1Sum[:]),
		"X-Checksum-Sha256": hex.EncodeToString(sha256Sum[:]),
		"Last-Modified":     modified.Format(http.TimeFormat),
	} {
		if got := h.Get(name); got != want {
			t.Fatalf("%s: %s %q, want %q (%v)", label, name, got, want, h)
		}
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("%s: HEAD body %q", label, rec.Body)
	}
}

// TestAccGenericRemoteColdHeadCachesTheFile: a cold HEAD of a .jar its
// upstream labels application/zip answers 200 with the jar's own type and
// the file's length and checksums, as 7.161.26's DownloadServiceImpl
// downloads the file on a HEAD when the remote stores artifacts locally
// and RequestResponseHelper.sendHeadResponse answers with the file's type.
// The file is then in the cache: a GET makes no upstream request, and a
// warm HEAD answers as the cold one did. The HEAD's download is not
// counted as a download, as StatsServiceImpl.updateDownloadStatsIfNeeded
// skips a HEAD; the GET after it is.
func TestAccGenericRemoteColdHeadCachesTheFile(t *testing.T) {
	w := newRemoteWorld(t)
	modified := time.Unix(1700000000, 0).UTC()
	w.up.Set("/cold/file.jar", fakeupstream.Resource{
		Body:     []byte(coldJar),
		Modified: modified,
		Header:   http.Header{"Content-Type": {"application/zip"}},
	})
	cold := w.request(http.MethodHead, "/artifactory/remote/cold/file.jar", "reader", "")
	wantFileHead(t, "cold HEAD", cold, coldJar, modified)
	if w.up.Count(http.MethodGet, "/cold/file.jar") != 1 {
		t.Fatalf("cold HEAD did not download the file: %v", w.up.Requests())
	}
	it, err := w.rt.Store.Items().Get(t.Context(), "remote", true, "cold/file.jar")
	if err != nil || it.Size != int64(len(coldJar)) || it.MimeType != javaArchive {
		t.Fatalf("cached item %+v, %v; want the jar", it, err)
	}
	stats, err := w.rt.Store.Items().Stats(t.Context(), it.ID)
	if err != nil || stats.DownloadCount != 0 {
		t.Fatalf("after the HEAD: stats %+v, %v; want no download counted", stats, err)
	}
	info := w.request(
		http.MethodGet,
		"/artifactory/api/storage/remote-cache/cold/file.jar",
		"reader",
		"",
	)
	remoteStatus(t, info, http.StatusOK)
	if got := mimeTypeOf(t, info); got != javaArchive {
		t.Fatalf("storage info mimeType %q, want %q", got, javaArchive)
	}

	calls := len(w.up.Requests())
	wantFileHead(
		t,
		"warm HEAD",
		w.request(
			http.MethodHead,
			"/artifactory/remote/cold/file.jar",
			"reader",
			"",
		),
		coldJar,
		modified,
	)
	wantDownload(t, w.request(http.MethodGet, "/artifactory/remote/cold/file.jar", "reader", ""),
		http.MethodGet, javaArchive, coldJar)
	if after := len(w.up.Requests()); after != calls {
		t.Fatalf("the file kept by the HEAD was fetched again: %v", w.up.Requests()[calls:])
	}
	stats, err = w.rt.Store.Items().Stats(t.Context(), it.ID)
	if err != nil || stats.DownloadCount != 1 {
		t.Fatalf("after the GET: stats %+v, %v; want one download", stats, err)
	}
}

// TestAccGenericRemoteColdHeadStoresNothingWithoutLocalStorage: on a
// remote with storeArtifactsLocally false a cold HEAD sends the upstream a
// HEAD only and keeps nothing, as 7.x's DownloadServiceImpl answers it from
// the info request alone.
func TestAccGenericRemoteColdHeadStoresNothingWithoutLocalStorage(t *testing.T) {
	w := newRemoteWorld(t)
	w.c.StoreArtifactsLocally = new(false)
	w.update()
	upstream(w, "/nostore/file.jar", "application/zip", coldJar)
	rec := w.request(http.MethodHead, "/artifactory/remote/nostore/file.jar", "reader", "")
	remoteStatus(t, rec, http.StatusOK)
	if got := rec.Header().
		Get("Content-Length"); got != strconv.Itoa(len(coldJar)) ||
		rec.Body.Len() != 0 {
		t.Fatalf("Content-Length %q body %q", got, rec.Body)
	}
	if w.up.Count(http.MethodHead, "/nostore/file.jar") != 1 ||
		w.up.Count(http.MethodGet, "/nostore/file.jar") != 0 {
		t.Fatalf("upstream requests %v, want one HEAD", w.up.Requests())
	}
	if _, err := w.rt.Store.Items().
		Get(t.Context(), "remote", true, "nostore/file.jar"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("a remote that stores nothing kept the file: %v", err)
	}
	remoteStatus(
		t,
		w.request(http.MethodGet, "/artifactory/api/storage/remote-cache/nostore/file.jar",
			"reader", ""),
		http.StatusNotFound,
	)
}

// TestAccGenericRemoteColdHeadAnswersAFailedDownloadAsTheGET: when the
// upstream answers the HEAD but the download that follows it fails, a cold
// HEAD answers what the GET does and keeps nothing: 7.x's HEAD on a remote
// that stores artifacts locally takes the GET's own respondFoundResource,
// whose download failure both answer alike.
func TestAccGenericRemoteColdHeadAnswersAFailedDownloadAsTheGET(t *testing.T) {
	w := newRemoteWorld(t)
	answers := map[string]int{}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		path := "/broken/" + method + "/file.jar"
		w.up.Set(path, fakeupstream.Resource{
			Body:   []byte(coldJar),
			Header: http.Header{"Content-Type": {javaArchive}},
			Fault:  fakeupstream.Fault{TruncateAfter: 4},
		})
		rec := w.request(method, "/artifactory/remote"+path, "reader", "")
		answers[method] = rec.Code
		if w.up.Count(http.MethodGet, path) == 0 {
			t.Fatalf("%s: the download was not attempted: %v", method, w.up.Requests())
		}
		if _, err := w.rt.Store.Items().Get(t.Context(), "remote", true, path[1:]); !errors.Is(
			err, kernel.ErrNotFound) {
			t.Fatalf("%s: a failed download kept the file: %v", method, err)
		}
	}
	if answers[http.MethodHead] != answers[http.MethodGet] ||
		answers[http.MethodGet] != http.StatusBadGateway {
		t.Fatalf(
			"HEAD %d, GET %d; want the GET's 502 for both",
			answers[http.MethodHead],
			answers[http.MethodGet],
		)
	}
}
