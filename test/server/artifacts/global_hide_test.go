package artifacts_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestAccGlobalHideUnauthorizedResources (#1903): the security
// configuration's hideUnauthorizedResources, switched by PATCH on a running
// server, turns every download refusal into 404 "Resource not found" with
// the requested path, on a local, a remote and a virtual repository alike,
// to an anonymous caller too, which then gets no challenge
// (DownloadServiceImpl.shouldHideUnauthorisedResponse 1499-1518, which reads
// the global setting before the virtual's, 7.161.26). A miss, a readable
// file and a folder keep their answers, and switching it off brings the
// refusals back. Before, the descriptor refused the field and nothing hid a
// direct refusal.
func TestAccGlobalHideUnauthorizedResources(t *testing.T) {
	w := virtualStatusWorld(t, true)
	patch := func(on string) {
		t.Helper()
		wantStatus(t, w.do(http.MethodPatch, "/artifactory/api/system/configuration", "admin",
			"security:\n  hideUnauthorizedResources: "+on+"\n", "Content-Type", "application/yaml"), http.StatusOK)
		if got := w.do(http.MethodGet, "/artifactory/api/securityconfig", "admin", "").Body.String(); !strings.Contains(got,
			`"hideUnauthorizedResources":`+on) {
			t.Fatalf("securityconfig after %s: %s", on, got)
		}
	}
	unchanged := []virtualAnswer{
		{"b-local/both.bin", "user", http.StatusOK, "b"},
		{"virt-ab/both.bin", "user", http.StatusOK, "b"},
		{"a-local/none.bin", "admin", http.StatusNotFound, "File not found.; Path: 'a-local:none.bin'"},
		{"virt-ab/none.bin", "admin", http.StatusNotFound, "Could not find resource; Path: 'virt-ab:none.bin'"},
	}
	refusals := []virtualAnswer{
		{
			"a-local/a-only.bin", "user", http.StatusForbidden,
			"Download request for repo:path 'a-local:a-only.bin' is forbidden for user: 'user'.; Path: 'a-local:a-only.bin'",
		},
		{"a-local/a-only.bin", "", http.StatusUnauthorized, "Unauthorized"},
		{
			"a-local/a-only.bin.sha1", "user", http.StatusForbidden,
			"Download request for repo:path 'a-local:a-only.bin' is forbidden for user: 'user'.; Path: 'a-local:a-only.bin'",
		},
		{
			"up/c.bin", "deployer", http.StatusForbidden,
			"Download request for repo:path 'up-cache:c.bin' is forbidden for user: 'deployer'.; Path: 'up:c.bin'",
		},
		{
			"virt-ab/a-only.bin", "user", http.StatusForbidden,
			forbiddenIn("a-local", "virt-ab", "a-only.bin", "user"),
		},
		{"virt-ab/a-only.bin", "", http.StatusUnauthorized, "Unauthorized"},
		{"virt-ab/z/a.zip!/in.txt", "", http.StatusUnauthorized, "Unauthorized"},
	}
	hidden := []virtualAnswer{
		{"a-local/a-only.bin", "user", http.StatusNotFound, "Resource not found; Path: 'a-local:a-only.bin'"},
		{"a-local/a-only.bin", "", http.StatusNotFound, "Resource not found; Path: 'a-local:a-only.bin'"},
		{"a-local/a-only.bin.sha1", "user", http.StatusNotFound, "Resource not found; Path: 'a-local:a-only.bin'"},
		{"up/c.bin", "deployer", http.StatusNotFound, "Resource not found; Path: 'up:c.bin'"},
		{"virt-ab/a-only.bin", "user", http.StatusNotFound, "Resource not found; Path: 'virt-ab:a-only.bin'"},
		{"virt-ab/a-only.bin", "", http.StatusNotFound, "Resource not found; Path: 'virt-ab:a-only.bin'"},
		{"virt-ab/z/a.zip!/in.txt", "", http.StatusNotFound, "Resource not found; Path: 'virt-ab:z/a.zip'"},
	}
	wantVirtualAnswers(t, w, append(refusals, unchanged...))
	patch("true")
	wantVirtualAnswers(t, w, append(hidden, unchanged...))
	// A folder is answered through its listing, which the setting leaves
	// alone, as respondResourceNotFound never answers it.
	wantStatus(t, w.do(http.MethodGet, "/artifactory/a-local/", "user", ""), http.StatusForbidden)
	// The trace names the answer the download gives.
	const path = "/artifactory/a-local/a-only.bin?trace"
	if _, steps := traceOf(t, w, http.MethodGet, path, "user"); len(steps) == 0 ||
		steps[len(steps)-1] != "Sending a response with the status '404' and the message "+
			"'Resource not found; Path: 'a-local:a-only.bin''" {
		t.Fatalf("%s: %q", path, steps)
	}
	patch("false")
	wantVirtualAnswers(t, w, append(refusals, unchanged...))
}
