package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestBuildPublishBuildRepo: publish answers 204 with the document's
// SHA-256, and takes buildRepo when it names the project's build-info
// repository (#412).
func TestBuildPublishBuildRepo(t *testing.T) {
	w := newItemWorld(t, false)
	doc := buildDoc("app", "1", testStarted)
	rec := w.do(http.MethodPut, buildsPath+"?buildRepo=artifactory-build-info", "admin", doc,
		"Content-Type", vendorJSON)
	wantStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 || rec.Header().Get("X-Checksum-Sha256") != sha256Hex(doc) {
		t.Fatalf("publish answered %q with %q", rec.Body, rec.Header().Get("X-Checksum-Sha256"))
	}
	rec = w.do(http.MethodPut, buildsPath+"?buildRepo=libs-local", "admin",
		buildDoc("app", "2", testStarted), "Content-Type", vendorJSON)
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(),
		"The build-info repository 'libs-local' does not hold this project's builds; "+
			"'artifactory-build-info' does") {
		t.Fatalf("body %s", rec.Body)
	}
	wantStatus(t, w.do(http.MethodGet, buildsPath+"/app/2", "admin", ""), http.StatusNotFound)
}

// TestBuildDiff: comparing two runs is not served (DV-088). The request is
// checked as Artifactory 6.20 checks it, and one that passes answers 501
// rather than the run (#412).
func TestBuildDiff(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(buildDoc("app", "1", startedAt(0)))
	w.publish(buildDoc("app", "2", startedAt(1)))
	for _, c := range []struct {
		path    string
		status  int
		message string
	}{
		{"/app/2?diff=1", http.StatusNotImplemented, "Comparing build runs (diff) is not supported"},
		{"/app/2?diff=2", http.StatusNotImplemented, "Comparing build runs (diff) is not supported"},
		{
			"/app/1?diff=2", http.StatusBadRequest,
			"Build number should be greater than the build number to compare against.",
		},
		{"/app/2?diff=null", http.StatusBadRequest, "Parameter 'diff' must contain a value, if specified."},
		{"/app/2?diff=9", http.StatusNotFound, "No build was found for build name: app , build number: 9"},
		{"/app/2?diff=", http.StatusNotFound, "No build was found for build name: app , build number: "},
		{"/app/3?diff=1", http.StatusNotFound, "No build was found for build name: app, build number: 3"},
	} {
		rec := w.do(http.MethodGet, buildsPath+c.path, "admin", "")
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"message":"`+c.message) {
			t.Errorf("%s: %d %s", c.path, rec.Code, rec.Body)
		}
	}
	// A caller who may not read the run to compare with is refused.
	wantStatus(
		t,
		w.do(http.MethodGet, buildsPath+"/app/2?diff=1", "user", ""),
		http.StatusForbidden,
	)
}
