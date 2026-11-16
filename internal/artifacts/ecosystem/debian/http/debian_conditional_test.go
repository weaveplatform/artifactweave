package debianhttp

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccDebianIndexAnswersConditionalsAsTheDownload: a local repository's
// Release and Packages, and a virtual repository's composed ones, answer
// If-Match and If-Unmodified-Since as the plain request and a HEAD's Range
// with the whole document, as 7.x's download answers the Debian metadata
// it stores in the repository, a virtual one's included (RTDEV-50452)
// (#1438, #1362).
func TestAccDebianIndexAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 pool/alpha_1.0_amd64.deb"))
	for _, key := range []string{"deb-local", "deb-virtual"} {
		for _, path := range []string{debRelease, debPackages} {
			kittest.WantDownloadConditionals(t, key+"/"+path,
				func(method string, headers ...string) *httptest.ResponseRecorder {
					r := w.request(key, path, "admin")
					r.Method = method
					for i := 0; i+1 < len(headers); i += 2 {
						r.Header.Set(headers[i], headers[i+1])
					}
					rec := httptest.NewRecorder()
					w.kit.GetItem(rec, r)
					return rec
				}, true)
		}
	}
}

// debTraceSteps returns the steps of the ?trace answer to the
// administrator's GET of path in the repository key, without their times.
func debTraceSteps(t *testing.T, w *overlayWorld, key, path string) []string {
	t.Helper()
	r := w.request(key, path, "admin")
	q := r.URL.Query()
	q.Set("trace", "")
	r.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("%s/%s trace answered %d %s", key, path, rec.Code, rec.Body)
	}
	_, steps, _ := strings.Cut(rec.Body.String(), "Steps: \n")
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(steps, "\n"), "\n") {
		_, step, _ := strings.Cut(line, " ")
		out = append(out, step)
	}
	return out
}

// TestAccDebianTraceFollowsTheOverlay (#1595): ?trace on a Debian index
// traces what the Debian overlay would answer, as 7.161.26's
// RepoFilter.doGetOrHeadRequest answers ?trace by running the download
// itself under a TraceLoggingResponse: a virtual repository's composed
// Packages index and a local one's stored index are found, and a local
// repository's missing index ends with the download's own miss (#1599). A
// package, which the overlay leaves to the generic lookup, keeps the
// generic trace. Before, the trace skipped the overlay, so the virtual
// index was not found and the local one was traced as a plain file.
func TestAccDebianTraceFollowsTheOverlay(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-empty", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	w.put("deb-local", debAlpha, false, debBody)
	found := func(key, class string) []string {
		return []string{
			"Received request",
			"Retrieving info from " + class + " repository '" + key + "' type debian",
			"Identified requested resource as a file",
			"Responding with found resource",
			"Request succeeded",
		}
	}
	for _, tc := range []struct {
		key, path string
		want      []string
	}{
		{"deb-virtual", debPackages, found("deb-virtual", "virtual")},
		{"deb-local", debPackages, found("deb-local", "local")},
		{"deb-empty", debPackages, []string{
			"Received request",
			"Retrieving info from local repository 'deb-empty' type debian",
			"Unable to find resource in deb-empty:" + debPackages,
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message " +
				"'File not found.; Path: 'deb-empty:" + debPackages + "''",
		}},
		{"deb-local", debAlpha, []string{
			"Received request",
			"Retrieving info from local repository 'deb-local' type debian",
			"Returning found resource from deb-local:" + debAlpha,
			"Identified requested resource as a file",
			"Responding with found resource",
			"Request succeeded",
		}},
	} {
		if got := debTraceSteps(t, w, tc.key, tc.path); !slices.Equal(got, tc.want) {
			t.Errorf("%s/%s: steps\n%s\nwant\n%s", tc.key, tc.path, strings.Join(got, "\n"),
				strings.Join(tc.want, "\n"))
		}
	}
}
