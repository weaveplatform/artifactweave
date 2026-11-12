package cran_test

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
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
// and an anonymous caller 401, without asking the upstream.
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
						wantCRANError(t, got, http.StatusForbidden, "Forbidden")
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

// TestAccCRANMandatoryMatrixParameters (#1350): a mandatory matrix
// parameter ("key+=value") serves a stored index or package only when the
// file carries the property, as 7.x's PropertiesImpl.matchQuery answers
// CONFLICT, and DbStoringRepoMixin.getInfo PROPERTY_MISMATCH, for a
// mandatory key the file lacks (7.161.26). A virtual repository's package
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
				wantCRANError(t, got, http.StatusNotFound, "Could not find resource")
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
			"Forbidden")
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
	for _, target := range []string{
		"/artifactory/cran-tagged/" + cranAssertthat + ";x=2",
		"/artifactory/cran-virtual/" + cranAssertthat + ";x+=2",
	} {
		wantCRANError(t, w.do(http.MethodGet, target, "admin", ""), http.StatusNotFound,
			"Could not find resource")
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
