package cran_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// cranAsk is one request the upstream had: the path, and the matrix
// parameters and query the source request carried for it.
type cranAsk struct{ path, matrix, query string }

// cranAsked is cranUpstream with a record of what it was asked.
type cranAsked struct {
	mu    sync.Mutex
	asked []cranAsk
}

// service returns cranUpstream's service, recording each request in a.
func (a *cranAsked) service(t *testing.T) *source.Service {
	svc := cranUpstream(t)
	inner := svc.HTTP
	svc.HTTP = sourceBackend(
		func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			a.mu.Lock()
			a.asked = append(a.asked, cranAsk{q.Resource.Path, q.MatrixParams, q.Resource.Query})
			a.mu.Unlock()
			return inner.Read(ctx, q, p)
		})
	return svc
}

// take returns the requests the upstream had since the last call.
func (a *cranAsked) take() []cranAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	return out
}

// cranTag sets the properties of the file at path in the repository key to
// x=1.
func cranTag(t *testing.T, w *itemWorld, key, path string) {
	t.Helper()
	it := w.item(key, false, path)
	if err := w.db.Items().SetProperties(t.Context(), it.ID,
		artmodel.Properties{"x": {"1"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// cranQueryWorld is a local repository holding the assertthat archive, a
// second one whose archive and PACKAGES carry the property x=1, a remote
// over cranUpstream, a remote whose cache holds both without the property,
// and virtual repositories over them.
func cranQueryWorld(t *testing.T) (*itemWorld, *cranAsked, []byte) {
	t.Helper()
	asked := &cranAsked{}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = asked.service(t) })
	archive := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
	cranPublish(t, w, "cran-local", func(*repo.Config) {}, string(archive))
	cranPublish(t, w, "cran-tagged", func(*repo.Config) {}, string(archive))
	for _, c := range []repo.Config{
		{Key: "cran-remote", Class: repo.Remote, URL: "https://up.example"},
		{Key: "cran-held", Class: repo.Remote, URL: "https://up.example"},
		{Key: "cran-over-held", Repositories: []string{"cran-held", "cran-tagged"}},
		{Key: "cran-virtual", Repositories: []string{"cran-local", "cran-tagged"}},
		{Key: "cran-untagged", Repositories: []string{"cran-local"}},
		{Key: "cran-over-remote", Repositories: []string{"cran-remote"}},
	} {
		if c.Class == "" {
			c.Class = repo.Virtual
		}
		c.PackageType = repo.Cran
		cranRepo(t, w, c)
	}
	// The PACKAGES GET settles the index the publish left to build, so the
	// properties set next stay on the item it serves.
	wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-tagged/"+cranIndex, "admin", ""),
		http.StatusOK)
	cranTag(t, w, "cran-tagged", cranIndex)
	cranTag(t, w, "cran-tagged", cranAssertthat)
	// cran-held's cache holds both files, without the property.
	for _, path := range []string{cranIndex, cranAssertthat} {
		if _, err := w.db.Items().Put(t.Context(), artmodel.Item{
			RepoKey: "cran-held", Cached: true, Name: path,
		}); err != nil {
			t.Fatal(err)
		}
	}
	asked.take()
	return w, asked, archive
}

// TestAccCRANRoutesTakeAQueryAndMatrixParameters (#1350): a CRAN index and
// package, of a local, remote or virtual repository, take a query and matrix
// parameters as any file's GET takes them. Before, the CRAN routes answered
// 404 "Could not find resource" to either, before the read check, so an
// administrator got 404 for a file that exists and a caller without read
// got 404 instead of 403 or 401. 7.x documents skipUpdateStats and trace for
// a GET of any file (RTFACT-25636, 7.21.3), and matrix parameters "make
// resolution conditional on properties" (JFrog's guide, Using Properties in
// Deployment and Resolution); neither names a package type it leaves out.
// The read check comes first (DV-057): a user without read is refused 403
// and an anonymous caller 401, without asking the upstream. Outside a
// virtual repository the user gets 7.x's download answer (cranRefused),
// from the item route and the CRAN overlay alike (#1599).
func TestAccCRANRoutesTakeAQueryAndMatrixParameters(t *testing.T) {
	w, asked, archive := cranQueryWorld(t)
	for _, key := range []string{"cran-local", "cran-remote", "cran-virtual"} {
		for _, path := range []string{cranIndex, cranAssertthat} {
			for _, suffix := range []string{"?q=1", ";x=1"} {
				target := "/artifactory/" + key + "/" + path + suffix
				for _, caller := range []string{"admin", "reader", "user", ""} {
					asked.take()
					got := w.do(http.MethodGet, target, caller, "")
					switch caller {
					case "user":
						wantCRANError(t, got, http.StatusForbidden, cranRefused(key, path))
					case "":
						wantCRANError(t, got, http.StatusUnauthorized,
							"Authentication is required")
					default:
						if got.Code != http.StatusOK {
							t.Errorf("%s as %s answered %d %.200s, want 200", target, caller,
								got.Code, got.Body)
						} else if path == cranAssertthat && !bytes.Equal(got.Body.Bytes(), archive) {
							t.Errorf("%s as %s changed the archive", target, caller)
						}
					}
					if caller == "user" || caller == "" {
						if a := asked.take(); len(a) != 0 {
							t.Errorf("%s as %q asked the upstream %v", target, caller, a)
						}
					}
				}
			}
		}
	}
}

// TestAccCRANRemotePassesItsQueryAndMatrixParametersUpstream (#1350,
// #1371): a remote's index or package GET asks the upstream with the
// client's query, which the source boundary passes on only under Propagate
// Query Params, and its matrix parameters, as for any remote file
// (HttpRepo.downloadResource, 7.161.26). The index a package GET reads to
// pin the archive's MD5, and a member's index a virtual repository composes,
// are documents the client did not ask for, so they go without either [I].
// A virtual package GET over the remote passes the client's matrix
// parameters and query on to the member, as 7.x hands its member the
// request's context.
func TestAccCRANRemotePassesItsQueryAndMatrixParametersUpstream(t *testing.T) {
	w, asked, _ := cranQueryWorld(t)
	for _, tc := range []struct {
		target string
		want   []cranAsk
	}{
		{
			"/artifactory/cran-remote/" + cranIndex + ";x=1?q=1",
			[]cranAsk{{cranIndex, ";x=1", "q=1"}},
		},
		{
			"/artifactory/cran-remote/" + cranAssertthat + ";x=1?q=1",
			[]cranAsk{{cranIndex, "", ""}, {cranAssertthat, ";x=1", "q=1"}},
		},
		{
			"/artifactory/cran-over-remote/" + cranIndex + ";x=1?q=1",
			[]cranAsk{{cranIndex, "", ""}},
		},
		{
			"/artifactory/cran-over-remote/" + cranAssertthat + ";x=1?q=1",
			[]cranAsk{{cranIndex, "", ""}, {cranAssertthat, ";x=1", "q=1"}},
		},
	} {
		wantStatus(t, w.do(http.MethodGet, tc.target, "admin", ""), http.StatusOK)
		if got := asked.take(); !slices.Equal(got, tc.want) {
			t.Errorf("%s asked the upstream %v, want %v", tc.target, got, tc.want)
		}
	}
}

// cranChecked is the repository whose path 7.161.26's download checks and
// matches for key, a remote's cache, or "" for a virtual repository, whose
// answers are P10's (#1533).
func cranChecked(key string) string {
	switch key {
	case "cran-local", "cran-tagged":
		return key
	case "cran-remote", "cran-held":
		return key + "-cache"
	}
	return ""
}

// cranRefused is the answer to user, who may not read key, for path:
// 7.161.26's download refusal (RealRepoBase.assertReadPermissions and the
// path DownloadServiceImpl.respondResourceNotFound appends), on the item
// route (#1572) and on the CRAN overlay's index and remote package alike
// (#1599); in a virtual repository, "Forbidden".
func cranRefused(key, path string) string {
	checked := cranChecked(key)
	if checked == "" {
		return "Forbidden"
	}
	return "Download request for repo:path '" + checked + ":" + path +
		"' is forbidden for user: 'user'.; Path: '" + key + ":" + path + "'"
}

// cranMismatch is the answer to a file under key that lacks a mandatory
// matrix key: 7.161.26's DbStoringRepoMixin.getInfo PROPERTY_MISMATCH
// reason for the repository that holds it, a remote's cache, and the path
// DownloadServiceImpl.respondResourceNotFound appends (#1572, #1599); in a
// virtual repository, "Could not find resource".
func cranMismatch(key, path string) string {
	key = cranChecked(key)
	if key == "" {
		return "Could not find resource"
	}
	return "File '" + key + ":" + path + "' was found, but mandatory properties do not match.; Path: '" +
		key + ":" + path + "'"
}

// TestAccCRANMandatoryMatrixParameters (#1350): a mandatory matrix
// parameter ("key+=value") serves a stored index or package only when the
// file carries the property, as 7.x's PropertiesImpl.matchQuery answers
// CONFLICT, and DbStoringRepoMixin.getInfo PROPERTY_MISMATCH, for a
// mandatory key the file lacks (7.161.26), with its text outside a virtual
// repository (cranMismatch, #1599). A virtual repository's package
// GET passes over a member whose file lacks it to the next member that
// lists the package, as 7.x's ArtifactoryVirtualDownloadStrategy passes
// over a member that does not find the file [I]; a remote's cached copy
// is matched the same way, without asking the upstream (#1371). A virtual
// repository's composed index carries no properties [I], so a mandatory key
// is not found there and a key it may lack is served.
func TestAccCRANMandatoryMatrixParameters(t *testing.T) {
	w, asked, archive := cranQueryWorld(t)
	for _, tc := range []struct {
		key, path string
		status    int
	}{
		{"cran-local", cranIndex, http.StatusNotFound},
		{"cran-local", cranAssertthat, http.StatusNotFound},
		{"cran-tagged", cranIndex, http.StatusOK},
		{"cran-tagged", cranAssertthat, http.StatusOK},
		{"cran-virtual", cranIndex, http.StatusNotFound},
		{"cran-virtual", cranAssertthat, http.StatusOK},
		{"cran-untagged", cranAssertthat, http.StatusNotFound},
		{"cran-held", cranIndex, http.StatusNotFound},
		{"cran-held", cranAssertthat, http.StatusNotFound},
		{"cran-over-held", cranAssertthat, http.StatusOK},
	} {
		target := "/artifactory/" + tc.key + "/" + tc.path + ";x+=1"
		for _, caller := range []string{"admin", "reader"} {
			got := w.do(http.MethodGet, target, caller, "")
			if tc.status == http.StatusNotFound {
				wantCRANError(t, got, http.StatusNotFound, cranMismatch(tc.key, tc.path))
				continue
			}
			if got.Code != tc.status {
				t.Errorf("%s as %s answered %d %.200s, want %d", target, caller, got.Code,
					got.Body, tc.status)
			} else if tc.path == cranAssertthat && !bytes.Equal(got.Body.Bytes(), archive) {
				t.Errorf("%s as %s changed the archive", target, caller)
			}
		}
		wantCRANError(t, w.do(http.MethodGet, target, "user", ""), http.StatusForbidden,
			cranRefused(tc.key, tc.path))
		// A virtual repository reads its remote member's PACKAGES to find
		// the archive; the file the client asked for is matched in the
		// cache alone.
		for _, a := range asked.take() {
			if a.path == tc.path {
				t.Errorf("%s asked the upstream for %v, want the cached copy matched", target, a)
			}
		}
	}
	// A key the file carries with another value does not match either.
	for _, tc := range []struct{ key, matrix string }{
		{"cran-tagged", ";x=2"},
		{"cran-virtual", ";x+=2"},
	} {
		target := "/artifactory/" + tc.key + "/" + cranAssertthat + tc.matrix
		wantCRANError(t, w.do(http.MethodGet, target, "admin", ""), http.StatusNotFound,
			cranMismatch(tc.key, cranAssertthat))
	}
	// A key the file may lack is served by the remote whose cached copies
	// lack the mandatory one, which asks the upstream.
	for _, path := range []string{cranIndex, cranAssertthat} {
		target := "/artifactory/cran-held/" + path + ";x=1"
		if got := w.do(http.MethodGet, target, "admin", ""); got.Code != http.StatusOK {
			t.Errorf("%s answered %d %.200s, want 200", target, got.Code, got.Body)
		}
	}
}

// TestAccCRANTraceFollowsTheOverlay (#1595): ?trace on a remote CRAN
// package that the cache does not hold traces what the CRAN overlay would
// answer, as 7.161.26's RepoFilter.doGetOrHeadRequest answers ?trace by
// running the download itself under a TraceLoggingResponse: the index and
// the package are fetched from the upstream, as the download fetches
// them, and the package is traced as found, the upstream's stream as
// validated, as the generic route traces one (traceRemoteItem), but nothing
// is served or counted as a download. Before, the trace skipped the overlay
// and traced the generic route's read instead.
func TestAccCRANTraceFollowsTheOverlay(t *testing.T) {
	w, asked, archive := cranQueryWorld(t)
	target := "/artifactory/cran-remote/" + cranAssertthat
	rec := w.do(http.MethodGet, target+"?trace", "admin", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("trace answered %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	_, steps, _ := strings.Cut(rec.Body.String(), "Steps: \n")
	var got []string
	for _, line := range strings.Split(strings.TrimSuffix(steps, "\n"), "\n") {
		_, step, _ := strings.Cut(line, " ")
		got = append(got, step)
	}
	want := []string{
		"Received request",
		"Retrieving info from remote repository 'cran-remote' type cran",
		"Returning validated transient resource",
		"Identified requested resource as a file",
		"Responding with found resource",
		"Request succeeded",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("steps\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if a := asked.take(); !slices.Equal(
		a,
		[]cranAsk{{cranIndex, "", ""}, {cranAssertthat, "", ""}},
	) {
		t.Errorf("the trace asked the upstream %v, want the index and the package", a)
	}
	downloads := func() int64 {
		t.Helper()
		it, err := w.db.Items().Get(t.Context(), "cran-remote", true, cranAssertthat)
		if errors.Is(err, kernel.ErrNotFound) {
			return 0 // the stream the trace closed unread was never kept
		}
		if err != nil {
			t.Fatal(err)
		}
		st, err := w.db.Items().Stats(t.Context(), it.ID)
		if err != nil {
			t.Fatal(err)
		}
		return st.DownloadCount
	}
	if n := downloads(); n != 0 {
		t.Errorf("the trace counted %d downloads, want none", n)
	}
	// The trace changed nothing the download then serves.
	if rec := w.do(http.MethodGet, target, "admin", ""); rec.Code != http.StatusOK ||
		!bytes.Equal(rec.Body.Bytes(), archive) {
		t.Fatalf("download answered %d", rec.Code)
	}
}
