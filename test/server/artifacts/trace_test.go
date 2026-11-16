package artifacts_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
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
func TestAccTraceResolution(t *testing.T) {
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
		expected []string
	}{
		// The trace ends with the answer the download would give (KB "The
		// ?trace API"): 7.161.26's RepoFilter runs the download itself under
		// a TraceLoggingResponse, which records the download's own status
		// and message, 7.x's miss with its path (#1572).
		{"/artifactory/libs-local/d/none?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Unable to find resource in libs-local:d/none",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message " +
				"'File not found.; Path: 'libs-local:d/none''",
		}},
		// A caller who may not read the path learns nothing of the
		// repository, only the refusal the download would answer.
		{local, "user", []string{
			"Received request",
			"Sending a response with the status '403' and the message 'Download request for " +
				"repo:path 'libs-local:d/a.txt' is forbidden for user: 'user'.; " +
				"Path: 'libs-local:d/a.txt''",
		}},
		{"/artifactory/missing/a.txt?trace", "admin", []string{
			"Received request",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message " +
				"'Repository 'missing' does not exist'",
		}},
		// Matrix parameters are matched as DbStoringRepoMixin.getInfo logs
		// it (7.161.26, 516-530), as the walk through a virtual repository
		// does (#1630): a conflict is unfound, and a key the file lacks
		// still serves it.
		{"/artifactory/libs-local/d/a.txt;team=other?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Request query properties conflict with those that annotate the artifact - " +
				"returning unfound resource",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'File " +
				"'libs-local:d/a.txt' was found, but mandatory properties do not match.; " +
				"Path: 'libs-local:d/a.txt''",
		}},
		{"/artifactory/libs-local/d/a.txt;other=x?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'libs-local' type generic",
			"Request query properties don't match those that annotate the artifact",
			"Returning found resource from libs-local:d/a.txt",
			"Identified requested resource as a file",
			"Responding with found resource",
			"Request succeeded",
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
			"Sending a response with the status '404' and the message " +
				"'File not found.; Path: 'up-cache:x.txt''",
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
			"Sending a response with the status '404' and the message " +
				"'File not found.; Path: 'libs-local:d/none''",
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
			"Sending a response with the status '404' and the message " +
				"'File not found.; Path: 'libs-local:z/none.zip''",
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
		wantSteps(t, c.path, steps, c.expected...)
	}

	// Anonymous, with anonymous access on, is traced as any caller is.
	w2 := newItemWorld(t, true)
	w2.put("/artifactory/libs-local/d/a.txt", "a")
	_, steps = traceOf(t, w2, http.MethodGet, "/artifactory/libs-local/d/a.txt?trace", "")
	wantSteps(t, "anonymous", steps,
		"Received request",
		"Sending a response with the status '401' and the message 'Unauthorized'",
	)
	// Without anonymous access, credentials are asked for before any trace.
	wantStatus(t, w.do(http.MethodGet, local, "", ""), http.StatusUnauthorized)
}

// A member that refuses the download is passed over with its reason, as
// RealRepoBase.createUnfoundRepoResource 224-227 logs it (#1533), and a
// path the format rewrites is named as rewritten.
func TestAccTraceRefusalsAndRewrites(t *testing.T) {
	w, _ := followingWorld(t)
	w.put("/artifactory/libs-local/a/f.txt", "f")
	w.put("/artifactory/other-local/a/f.txt", "o")
	w.put("/artifactory/libs-local/z/a.zip", string(zipBytes(t, map[string]string{"in.txt": "x"})))
	w.put("/artifactory/other-local/z/a.zip", string(zipBytes(t, map[string]string{"in.txt": "x"})))
	w.patchRepo("other-local", func(c *repo.Config) { c.BlackedOut = ptr(true) })

	const latest = "/artifactory/virt/latest.txt?trace"
	_, steps := traceOf(t, w, http.MethodGet, latest, "admin")
	wantSteps(t, latest, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt' type generic",
		"Searching for the resource within other-local",
		"Download denied (The repository 'other-local' is blacked out and cannot serve artifact "+
			"'other-local:latest.txt'.) - returning un-found resource",
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

	w.patchRepo("virt", func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.secret") })
	refusal := func(artifact string) string {
		return "The repository 'other-local' is blacked out and cannot serve artifact " +
			"'other-local:" + artifact + "'."
	}
	for _, c := range []struct {
		path, as string
		expected []string
	}{
		// A blacked-out repository refuses the download outright, as
		// RealRepoBase.createUnfoundRepoResource 224-227 logs it and with
		// the path the download names (#1630): a checksum's and an archive
		// entry's are the file they name.
		{"/artifactory/other-local/a/f.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'other-local' type generic",
			"Download denied (" + refusal("a/f.txt") + ") - returning un-found resource",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message '" + refusal("a/f.txt") +
				"; Path: 'other-local:a/f.txt''",
		}},
		{"/artifactory/other-local/a/f.txt.sha1?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'other-local' type generic",
			"Download denied (" + refusal("a/f.txt") + ") - returning un-found resource",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message '" + refusal("a/f.txt") +
				"; Path: 'other-local:a/f.txt''",
		}},
		{"/artifactory/other-local/z/a.zip!/in.txt?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'other-local' type generic",
			"Download denied (" + refusal("z/a.zip") + ") - returning un-found resource",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message '" + refusal("z/a.zip") +
				"; Path: 'other-local:z/a.zip''",
		}},
		// 7.x lists a folder without tracing it (RepoFilter.doRepoRequest),
		// so a folder's refusal keeps this server's own step.
		{"/artifactory/other-local/a/?trace", "admin", []string{
			"Received request",
			"Retrieving info from local repository 'other-local' type generic",
			"Request is forbidden by other-local",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message '" + refusal("a") + "'",
		}},
		// A virtual repository's own patterns leave it no member to search
		// (getInfoFromSearchableRepositories 158-169).
		{"/artifactory/virt/a.secret?trace", "admin", []string{
			"Received request",
			"Retrieving info from virtual repository 'virt' type generic",
			"Unable to find aggregated repositories to search within - returning unfound resource",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message 'No repository found " +
				"to serve the request for virt:a.secret; Path: 'virt:a.secret''",
		}},
	} {
		_, steps := traceOf(t, w, http.MethodGet, c.path, c.as)
		wantSteps(t, c.path, steps, c.expected...)
	}
}

// Maven's release and snapshot handling refuses a download 409 as
// 7.161.26 answers any refusal of the download policy: logged by
// createUnfoundRepoResource and responded to as an unfound resource, with
// the path the download names (#1630). The policy is checked on the file a
// checksum or an archive entry names, as checkDownloadIsAllowed checks it
// before getInfo looks.
func TestAccTraceMavenPolicyRefusal(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "mvn-rel", Class: repo.Local, PackageType: repo.Maven, HandleSnapshots: ptr(false)}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	const jar = "com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar"
	const refusal = "The repository 'mvn-rel' rejected the resolution of an artifact " +
		"'mvn-rel:" + jar + "' due to conflict in the snapshot release handling policy."
	denied := "Download denied (" + refusal + ") - returning un-found resource"
	answer := "Sending a response with the status '409' and the message '" + refusal +
		"; Path: 'mvn-rel:" + jar + "''"
	for _, c := range []struct {
		path     string
		expected []string
	}{
		{jar, []string{
			"Received request",
			"Retrieving info from local repository 'mvn-rel' type maven",
			denied,
			"Responding with unfound resource",
			answer,
		}},
		{jar + ".sha1", []string{
			"Received request",
			"Retrieving info from local repository 'mvn-rel' type maven",
			"Unable to find resource in mvn-rel:" + jar + ".sha1",
			"Request is for a checksum",
			denied,
			"Responding with unfound resource",
			answer,
		}},
		{jar + "!/f.txt", []string{
			"Received request",
			"Retrieving info from local repository 'mvn-rel' type maven",
			"Unable to find resource in mvn-rel:" + jar + "!/f.txt",
			"Requested resource is contained within an archive",
			denied,
			"Responding with unfound resource",
			answer,
		}},
	} {
		path := "/artifactory/mvn-rel/" + c.path + "?trace"
		_, steps := traceOf(t, w, http.MethodGet, path, "admin")
		wantSteps(t, path, steps, c.expected...)
	}
}

// The trace names the proxy headers 7.161.26 prints (TraceLoggingResponse's
// "Headers:" block, from RepoFilter.TRACE_WHITELISTED_HEADERS) between the
// user and the time: in that order, only those not blank, each on one line.
// Any other header is left out, and a request with none of them has no
// block, as every other trace test's parse checks.
func TestAccTraceNamesTheProxyHeaders(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/a.txt", "a")

	const path = "/artifactory/libs-local/d/a.txt?trace"
	rec := w.do(http.MethodGet, path, "admin", "",
		"X-Forwarded-Proto", "https",
		"X-Forwarded-Host", "proxy.example.com",
		"X-JFrog-Override-Base-Url", "  ",
		"X-Orig-Client-Uri", "/orig/a.txt\r\nInjected: x",
		"X-Forwarded-For", "10.0.0.1",
	)
	wantStatus(t, rec, http.StatusOK)
	const block = "User: admin\nHeaders:\n" +
		"  X-Orig-Client-Uri: /orig/a.txt Injected: x\n" +
		"  X-Forwarded-Host: proxy.example.com\n" +
		"  X-Forwarded-Proto: https\n" +
		"Time: "
	if !strings.Contains(rec.Body.String(), block) {
		t.Fatalf("%s: body\n%s\nwant\n%s", path, rec.Body, block)
	}
}
