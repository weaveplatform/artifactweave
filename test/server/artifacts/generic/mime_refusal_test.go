package generic_test

// This file holds the remote MIME refusal suites of #1376 and #1377: the
// 409 and message 7.x answers a file its upstream labels with a blocked
// type, the missed-retrieval cache that answers the path again, and the
// client's Accept types the label is checked against.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

const loginPage = "<!DOCTYPE html><html>login</html>"

// refusalOf is 7.x's message for a refusal of the upstream file at
// upstreamPath, answered for path in the remote repository.
func refusalOf(w *remoteWorld, upstreamPath, requested, label, path string) string {
	return "Error fetching " + w.up.URL + upstreamPath + ": Mismatching mime types found in request: [" +
		requested + "] and response: " + label + ", and this repo blocks " + label +
		" on mismatch; Path: 'remote:" + path + "'"
}

// wantRefusal holds a download to 409 with message. A HEAD's body, which
// the server never sends, is not read.
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, method, message string) {
	t.Helper()
	remoteStatus(t, rec, http.StatusConflict)
	if method == http.MethodHead {
		return
	}
	var answer struct {
		Errors []struct {
			Message string `json:"message"`
			Status  int    `json:"status"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || len(answer.Errors) != 1 ||
		answer.Errors[0].Status != http.StatusConflict || answer.Errors[0].Message != message {
		t.Fatalf("%s: %q (%v), want message %q", method, rec.Body, err, message)
	}
}

// TestAccGenericRemoteMIMERefusal: a .jar its upstream labels text/html
// answers 409 with 7.x's message, GET and HEAD, and nothing is cached
// for it. The refusal is remembered: within missedRetrievalCachePeriodSecs
// the path answers it again without an upstream request, whatever the
// upstream now sends, until a zap clears it; with a period of zero nothing
// is remembered. The message names the URL asked for, before a redirect.
func TestAccGenericRemoteMIMERefusal(t *testing.T) {
	w := newRemoteWorld(t)
	upstreamCalls := func(path string) int { return w.up.Count("", path) }
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		path := "/refused/" + method + "/file.jar"
		upstream(w, path, "text/html; charset=utf-8", loginPage)
		message := refusalOf(w, path, "application/java-archive", "text/html", path[1:])
		rec := w.request(method, "/artifactory/remote"+path, "reader", "")
		wantRefusal(t, rec, method, message)
		rec = w.request(http.MethodGet, "/artifactory/api/storage/remote-cache"+path, "reader", "")
		remoteStatus(t, rec, http.StatusNotFound)

		before := upstreamCalls(path)
		upstream(w, path, octetStream, "jar bytes")
		for _, again := range []string{http.MethodHead, http.MethodGet} {
			wantRefusal(
				t,
				w.request(again, "/artifactory/remote"+path, "reader", ""),
				again,
				message,
			)
		}
		if after := upstreamCalls(path); after != before {
			t.Fatalf("%s: remembered refusal made %d upstream requests", path, after-before)
		}
	}

	// A zap of the folder forgets the refusals below it.
	upstream(w, "/refused/kept.jar", javaArchive, "kept")
	wantDownload(t, w.request(http.MethodGet, "/artifactory/remote/refused/kept.jar", "reader", ""),
		http.MethodGet, javaArchive, "kept")
	remoteStatus(
		t,
		w.request(http.MethodPost, "/artifactory/api/zap/remote-cache/refused", "admin", ""),
		http.StatusOK,
	)
	wantDownload(
		t,
		w.request(http.MethodGet, "/artifactory/remote/refused/GET/file.jar", "reader", ""),
		http.MethodGet,
		javaArchive,
		"jar bytes",
	)

	// The message names the URL the remote asked for, before a redirect.
	w.up.Set(
		"/moved/file.jar",
		fakeupstream.Resource{Status: http.StatusFound, Redirect: "/target/file.jar"},
	)
	upstream(w, "/target/file.jar", "text/html", loginPage)
	wantRefusal(
		t,
		w.request(http.MethodGet, "/artifactory/remote/moved/file.jar", "reader", ""),
		http.MethodGet,
		refusalOf(w, "/moved/file.jar", "application/java-archive", "text/html", "moved/file.jar"),
	)

	zero := 0
	w.c.MissedRetrievalCachePeriodSecs = &zero
	w.update()
	upstream(w, "/unremembered/file.jar", "text/html", loginPage)
	message := refusalOf(
		w,
		"/unremembered/file.jar",
		"application/java-archive",
		"text/html",
		"unremembered/file.jar",
	)
	wantRefusal(
		t,
		w.request(http.MethodHead, "/artifactory/remote/unremembered/file.jar", "reader", ""),
		http.MethodHead,
		message,
	)
	upstream(w, "/unremembered/file.jar", octetStream, "jar bytes")
	wantDownload(
		t,
		w.request(http.MethodGet, "/artifactory/remote/unremembered/file.jar", "reader", ""),
		http.MethodGet,
		javaArchive,
		"jar bytes",
	)
}

// TestAccGenericRemoteMIMERefusalServesExpiredFile: when a cached file
// has expired and its upstream's label is now refused, the request that
// met the refusal is served the cached file, GET or HEAD, and the
// requests after it answer the refusal without an upstream request, as
// 7.x's RemoteRepoBase.internalGetInfo serves an expired file for a
// rejected resource and its missed-retrieval cache answers the path
// (addRejectedResourceToMissedCache).
func TestAccGenericRemoteMIMERefusalServesExpiredFile(t *testing.T) {
	w := newRemoteWorld(t)
	set := func(path, label, body string) {
		// max-age=0 expires the cached file at once.
		w.up.Set(path, fakeupstream.Resource{Body: []byte(body), Header: http.Header{
			"Content-Type": {label}, "Cache-Control": {"max-age=0"},
		}})
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		path := "/expired/" + method + "/file.jar"
		set(path, javaArchive, "jar bytes")
		wantDownload(t, w.request(http.MethodGet, "/artifactory/remote"+path, "reader", ""),
			http.MethodGet, javaArchive, "jar bytes")
		set(path, "text/html", loginPage)
		before := w.up.Count("", path)
		wantDownload(t, w.request(method, "/artifactory/remote"+path, "reader", ""),
			method, javaArchive, "jar bytes")
		if w.up.Count("", path) == before {
			t.Fatalf("%s %s: the expired file was served without asking the upstream", method, path)
		}
		before = w.up.Count("", path)
		message := refusalOf(w, path, javaArchive, "text/html", path[1:])
		for _, again := range []string{http.MethodGet, http.MethodHead} {
			wantRefusal(
				t,
				w.request(again, "/artifactory/remote"+path, "reader", ""),
				again,
				message,
			)
		}
		if after := w.up.Count("", path); after != before {
			t.Fatalf("%s: remembered refusal made %d upstream requests", path, after-before)
		}
	}
}

// TestAccGenericRemoteMIMEAccept: the label is checked against the
// client's Accept types, without parameters and lowercased, so a client
// that accepts text/html is served the file and one that accepts another
// type is refused; Accept: */* is checked against the path's type, as no
// Accept is, not let through as RTDEV-46375 lets it (#1377). The refusal
// lists the types in the order of 7.x's Java HashSet.
func TestAccGenericRemoteMIMEAccept(t *testing.T) {
	w := newRemoteWorld(t)
	for i, tc := range []struct {
		accept, requested string // requested is empty when the file is served
	}{
		{"text/html", ""},
		{"Text/HTML; q=0.9", ""},
		{"application/json, text/html;q=0.5", ""},
		{"application/octet-stream", ""},
		{"application/json", "application/json"},
		{"application/json, application/xml", "application/xml, application/json"},
		{"*/*", "application/java-archive"},
		{"application/json, */*;q=0.1", "application/json"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			path := "/accept/" + string(rune('a'+i)) + "/" + method + "/file.jar"
			upstream(w, path, "text/html", loginPage)
			rec := w.request(method, "/artifactory/remote"+path, "reader", "", "Accept", tc.accept)
			if tc.requested != "" {
				wantRefusal(t, rec, method, refusalOf(w, path, tc.requested, "text/html", path[1:]))
				continue
			}
			// A HEAD on a miss keeps the file as the GET does and answers
			// with the jar's own type (#1391).
			wantDownload(t, rec, method, javaArchive, loginPage)
		}
	}
}
