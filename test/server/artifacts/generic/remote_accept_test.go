package generic_test

// This file holds the client Accept suite of #1390: a remote read sends the
// upstream no Accept of the client's, and keeps one cached file per path
// whatever the next client accepts.

import (
	"net/http"
	"testing"
)

// TestAccGenericRemoteSendsNoClientAccept: the item route asks the upstream
// without the client's Accept, and a file cached by a GET that sent one is
// served from the cache to GETs that send another or none, with no upstream
// request. 7.161.26's HttpRepo.whiteListHeaders sends upstream only the
// headers an internal request delegates, never a client's, and the cache
// holds one file per path (RemoteRepoBase.internalGetInfo looks it up by
// RepoPath). The client's Accept is still the requested type of the MIME
// check (#1377).
func TestAccGenericRemoteSendsNoClientAccept(t *testing.T) {
	w := newRemoteWorld(t)
	upstream(w, "/accept/probe.jar", javaArchive, "probe bytes")
	wantDownload(
		t,
		w.request(http.MethodGet, "/artifactory/remote/accept/probe.jar", "reader", "",
			"Accept", "application/x-probe"),
		http.MethodGet, javaArchive, "probe bytes",
	)
	asked := 0
	for _, r := range w.up.Requests() {
		if r.Path != "/accept/probe.jar" {
			continue
		}
		asked++
		if got := r.Header.Values("Accept"); len(got) != 0 {
			t.Fatalf("%s %s reached the upstream with Accept %q", r.Method, r.Path, got)
		}
	}
	if asked == 0 {
		t.Fatalf("the upstream was not asked: %v", w.up.Requests())
	}

	upstream(w, "/accept/cached.jar", javaArchive, "cached bytes")
	wantDownload(
		t,
		w.request(http.MethodGet, "/artifactory/remote/accept/cached.jar", "reader", "",
			"Accept", "text/html"),
		http.MethodGet, javaArchive, "cached bytes",
	)
	calls := w.up.Count("", "/accept/cached.jar")
	for _, accept := range [][]string{nil, {"Accept", "application/json"}} {
		wantDownload(
			t,
			w.request(http.MethodGet, "/artifactory/remote/accept/cached.jar", "reader", "",
				accept...),
			http.MethodGet, javaArchive, "cached bytes",
		)
	}
	if after := w.up.Count("", "/accept/cached.jar"); after != calls {
		t.Fatalf("a GET with another Accept went upstream %d times", after-calls)
	}
}
