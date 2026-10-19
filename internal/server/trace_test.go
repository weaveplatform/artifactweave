package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// traceOf asks for path's trace and returns its header lines by name and
// its steps without their times, checking the shape Artifactory's trace has.
func traceOf(t *testing.T, w *itemWorld, method, path, as string) (map[string]string, []string) {
	t.Helper()
	rec := w.do(method, path, as, "")
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("%s: Content-Type %q", path, ct)
	}
	head, steps, ok := strings.Cut(rec.Body.String(), "Steps: \n")
	if !ok {
		t.Fatalf("%s: no steps in %q", path, rec.Body)
	}
	header := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(head, "\n"), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("%s: header line %q", path, line)
		}
		header[k] = v
	}
	if header["Request ID"] == "" || header["Request ID"] != rec.Header().Get(requestIDHeader) {
		t.Fatalf("%s: request id %q, header %q", path, header["Request ID"],
			rec.Header().Get(requestIDHeader))
	}
	if _, err := time.Parse(traceTime, header["Time"]); err != nil {
		t.Fatalf("%s: time %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(steps, "\n"), "\n") {
		at, msg, ok := strings.Cut(line, " ")
		if _, err := time.Parse(traceTime, at); !ok || err != nil {
			t.Fatalf("%s: step %q: %v", path, line, err)
		}
		out = append(out, msg)
	}
	return header, out
}

func wantSteps(t *testing.T, path string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: steps\n%s\nwant\n%s", path, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// ?trace answers the resolution as text instead of the content (7.x's
// retrieveartifact "trace": "returns verbose output about the resolution
// process", text/plain), in the shape of Artifactory's trace.
func TestTraceResolution(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/a.txt;team=core", "a")
	w.put("/artifactory/libs-local/z/a.zip", string(zipBytes(t, map[string]string{"in.txt": "x"})))
	const local = "/artifactory/libs-local/d/a.txt?trace"

	header, steps := traceOf(t, w, http.MethodGet, local, "admin")
	if header["Repo Path ID"] != "libs-local:d/a.txt" || header["Method Name"] != "GET" ||
		header["User"] != "admin" {
		t.Fatalf("header %v", header)
	}
	wantSteps(t, local, steps,
		"Received request",
		"Retrieving info from local repository 'libs-local' type generic",
		"Returning found resource from libs-local:d/a.txt",
		"Identified requested resource as a file",
		"Responding with found resource",
		"Request succeeded",
	)
	// A trace serves nothing, so it is not a download.
	if st, err := w.db.Items().Stats(context.Background(),
		w.item("libs-local", false, "d/a.txt").ID); err != nil || st.DownloadCount != 0 {
		t.Fatalf("downloads %+v, %v", st, err)
	}

	// A virtual repository names each member it searches, skipping one
	// that does not exist.
	const virt = "/artifactory/virt/d/a.txt?trace"
	_, steps = traceOf(t, w, http.MethodGet, virt, "admin")
	wantSteps(t, virt, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt' type generic",
		"Searching for the resource within other-local",
		"Unable to find resource in other-local:d/a.txt",
		"Searching for the resource within libs-local",
		"Returning found resource from libs-local:d/a.txt",
		"Identified requested resource as a file",
		"Responding with found resource",
		"Request succeeded",
	)

	// HEAD answers the trace's status with no body.
	rec := w.do(http.MethodHead, local, "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body %q", rec.Body)
	}

	for _, c := range []struct {
		path, as string
		want     []string
	}{
		// The trace ends with the answer the download would give, 7.x's
		// "Sending a response with the status '404' and the message 'Could
		// not find resource'" (KB "The ?trace API").
		{"/artifactory/libs-local/d/none?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:d/none",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
		// A caller who may not read the path learns nothing of the
		// repository, only the refusal the download would answer.
		{local, "user", []string{
			"Received request",
			"Sending a response with the status '403' and the message 'Forbidden'",
		}},
		{"/artifactory/missing/a.txt?trace", "admin", []string{
			"Received request",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message " +
				"'Repository 'missing' does not exist'",
		}},
		{"/artifactory/libs-local/d/a.txt;team=other?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Returning found resource from libs-local:d/a.txt",
			"Request query properties don't match those that annotate the artifact",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
		{"/artifactory/libs-local/d?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Returning found resource from libs-local:d",
			"Identified requested resource as a folder",
			"Request succeeded",
		}},
		{"/artifactory/libs-local/?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Identified requested resource as a folder",
			"Request succeeded",
		}},
		{"/artifactory/up-cache/x.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from cache repository 'up-cache' type generic",
			"Unable to find resource in up-cache:x.txt",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
		{"/artifactory/libs-local/d/a.txt.sha256?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:d/a.txt.sha256",
			"Request is for a checksum",
			"Returning found resource from libs-local:d/a.txt",
			"Responding to checksum request",
			"Request succeeded",
		}},
		{"/artifactory/libs-local/d/none.sha1?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:d/none.sha1",
			"Request is for a checksum",
			"Unable to find resource in libs-local:d/none",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
		{"/artifactory/libs-local/z/a.zip!/in.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:z/a.zip!/in.txt",
			"Requested resource is contained within an archive",
			"Returning found resource from libs-local:z/a.zip",
			"Responding with found resource",
			"Request succeeded",
		}},
		{"/artifactory/libs-local/z/a.zip!/none.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:z/a.zip!/none.txt",
			"Requested resource is contained within an archive",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
		{"/artifactory/libs-local/z/none.zip!/in.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:z/none.zip!/in.txt",
			"Requested resource is contained within an archive",
			"Unable to find resource in libs-local:z/none.zip",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
		{"/artifactory/libs-local/d!/in.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:d!/in.txt",
			"Requested resource is contained within an archive",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'Could not find resource'",
		}},
	} {
		_, steps := traceOf(t, w, http.MethodGet, c.path, c.as)
		wantSteps(t, c.path, steps, c.want...)
	}

	// Anonymous, with anonymous access on, is traced as any caller is.
	w2 := newItemWorld(t, true)
	w2.put("/artifactory/libs-local/d/a.txt", "a")
	_, steps = traceOf(t, w2, http.MethodGet, "/artifactory/libs-local/d/a.txt?trace", "")
	wantSteps(t, "anonymous", steps,
		"Received request",
		"Sending a response with the status '401' and the message 'Authentication is required'",
	)
	// Without anonymous access, credentials are asked for before any trace.
	wantStatus(t, w.do(http.MethodGet, local, "", ""), http.StatusUnauthorized)
}

// A member that refuses the download is named and passed over, and a path
// the format rewrites is named as rewritten.
func TestTraceRefusalsAndRewrites(t *testing.T) {
	w, _ := followingWorld(t)
	w.put("/artifactory/libs-local/a/f.txt", "f")
	w.put("/artifactory/other-local/a/f.txt", "o")
	w.put("/artifactory/libs-local/z/a.zip", string(zipBytes(t, map[string]string{"in.txt": "x"})))
	w.patchRepo("other-local", func(c *repo.Config) { c.BlackedOut = ptr(true) })

	const latest = "/artifactory/virt/latest.txt?trace"
	_, steps := traceOf(t, w, http.MethodGet, latest, "admin")
	wantSteps(t, latest, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt' type generic",
		"Searching for the resource within other-local",
		"Request is forbidden by other-local",
		"Searching for the resource within libs-local",
		"Request path was translated to a/f.txt",
		"Returning found resource from libs-local:a/f.txt",
		"Identified requested resource as a file",
		"Responding with found resource",
		"Request succeeded",
	)

	// HEAD sends the trace's status alone.
	rec := w.do(http.MethodHead, "/artifactory/libs-local/a/f.txt?trace", "admin", "")
	wantStatus(t, rec, http.StatusOK)

	for _, c := range []struct {
		path, as string
		want     []string
	}{
		// A blacked-out repository refuses the download outright.
		{"/artifactory/other-local/a/f.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'other-local' type generic",
			"Request is forbidden by other-local",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message " +
				"'The repository 'other-local' is blacked out and cannot serve artifact " +
				"'other-local:a/f.txt'.'",
		}},
	} {
		_, steps := traceOf(t, w, http.MethodGet, c.path, c.as)
		wantSteps(t, c.path, steps, c.want...)
	}
}
