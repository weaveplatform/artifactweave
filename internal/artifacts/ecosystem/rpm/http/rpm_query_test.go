package rpmhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// as serves method on path through the kit's item GET as caller: "admin"
// is kittest's administrator; "reader" a user allowed to read, and nothing
// else, everywhere; "user" a user allowed nothing; and "" the anonymous
// user, allowed nothing.
func (w *itemWorld) as(method, path, caller string) *httptest.ResponseRecorder {
	w.t.Helper()
	read := func(_ accessapi.ResourceType, _ accessapi.Repo, _ string, want accessapi.Actions) bool {
		return want&^accessapi.ActionRead == 0
	}
	var p kernel.Principal
	var d accessapi.Decider
	switch caller {
	case "admin":
		p, d = kittest.Admin, kittest.AdminAccess().Decider
	case "reader":
		p, d = kernel.Principal{Name: caller}, &accessapitest.Decider{
			IsRepoVisibleFunc: func(accessapi.Repo) bool { return true },
			AllowsFunc:        read, AllowsFolderFunc: read,
		}
	case "":
		p, d = kernel.Principal{Name: kernel.AnonymousUser, Anonymous: true},
			&accessapitest.Decider{}
	default:
		p, d = kernel.Principal{Name: caller}, &accessapitest.Decider{}
	}
	r := httptest.NewRequestWithContext(w.t.Context(), method, path, nil)
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
}

// rpmAsk is one request the upstream had: the path, and the matrix
// parameters and query the source request carried for it.
type rpmAsk struct{ path, matrix, query string }

// rpmAsked serves the createrepo_c fixture as an upstream, recording what
// it was asked.
type rpmAsked struct {
	mu    sync.Mutex
	asked []rpmAsk
}

// service returns a remote source service whose upstream serves the
// fixture's repomd.xml, primary and package, recording each request in a.
func (a *rpmAsked) service(t *testing.T) *source.Service {
	manifest, primary, primaryPath := rpmUpstreamFixture(t)
	responses := map[string][]byte{
		"repodata/repomd.xml": manifest, primaryPath: primary, rpmAlphaPath: rpmAlpha(t),
	}
	return &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(func(
		_ context.Context, q source.Request, _ source.Permit,
	) (source.Result, error) {
		a.mu.Lock()
		a.asked = append(a.asked, rpmAsk{q.Resource.Path, q.MatrixParams, q.Resource.Query})
		a.mu.Unlock()
		body, ok := responses[q.Resource.Path]
		if !ok {
			return source.Result{}, kernel.ErrNotFound
		}
		return source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
			Representation: source.Representation{Size: int64(len(body))},
		}}, nil
	})}
}

// take returns the requests the upstream had since the last call.
func (a *rpmAsked) take() []rpmAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	return out
}

// tag sets the properties of the file at path in the repository key to
// x=1.
func (w *itemWorld) tag(key, path string) {
	w.t.Helper()
	it, err := w.db.Items().Get(w.t.Context(), key, false, path)
	if err == nil {
		err = w.db.Items().SetProperties(w.t.Context(), it.ID,
			model.Properties{"x": {"1"}}, time.Now().UTC())
	}
	if err != nil {
		w.t.Fatal(err)
	}
}

const rpmRepomd = "repodata/repomd.xml"

// rpmQueryWorld is a local repository holding the createrepo_c fixture, a
// second whose repomd.xml and package carry the property x=1, a remote over
// the fixture, a remote whose cache holds the package without the
// property, and virtual repositories over them.
func rpmQueryWorld(t *testing.T) (*itemWorld, *rpmAsked) {
	t.Helper()
	asked := &rpmAsked{}
	w := newItemWorld(t, func(c *Config) { c.RemoteSources = asked.service(t) })
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{Key: "rpm-tagged", Class: repo.Local},
		repo.Config{Key: "rpm-remote", Class: repo.Remote},
		repo.Config{Key: "rpm-held", Class: repo.Remote},
		repo.Config{
			Key:          "rpm-virtual",
			Class:        repo.Virtual,
			Repositories: []string{"rpm-local", "rpm-tagged"},
		},
		repo.Config{Key: "rpm-untagged", Class: repo.Virtual, Repositories: []string{"rpm-local"}},
		repo.Config{
			Key: "rpm-over-remote", Class: repo.Virtual, Repositories: []string{"rpm-remote"},
		},
		repo.Config{
			Key:          "rpm-over-held",
			Class:        repo.Virtual,
			Repositories: []string{"rpm-held", "rpm-tagged"},
		},
	)
	w.publish("rpm-local", "")
	w.publish("rpm-tagged", "")
	w.tag("rpm-tagged", rpmRepomd)
	w.tag("rpm-tagged", rpmAlphaPath)
	w.storeFile("rpm-held", rpmAlphaPath, true, rpmAlpha(t))
	return w, asked
}

// TestAccRPMRoutesTakeAQueryAndMatrixParameters (#1350): a virtual RPM
// repository's repodata and package take a query and matrix parameters as
// any file's GET takes them, and as a local or remote repository's, which
// the generic item route serves, already did. Before, the virtual routes
// answered 404 "Could not find resource" to either, before the read check,
// so an administrator got 404 for a file that exists and a caller without
// read got 404 instead of 403 or 401. 7.x documents skipUpdateStats and
// trace for a GET of any file (RTFACT-25636, 7.21.3), and matrix
// parameters "make resolution conditional on properties" (JFrog's guide,
// Using Properties in Deployment and Resolution); neither names a package
// type it leaves out. The read check comes first (DV-057): a user without
// read is refused 403 and an anonymous caller 401, without asking the
// upstream.
func TestAccRPMRoutesTakeAQueryAndMatrixParameters(t *testing.T) {
	w, asked := rpmQueryWorld(t)
	alpha := rpmAlpha(t)
	for _, key := range []string{"rpm-local", "rpm-remote", "rpm-virtual", "rpm-over-remote"} {
		for _, path := range []string{rpmRepomd, rpmAlphaPath} {
			for _, suffix := range []string{"?q=1", ";x=1"} {
				target := "/artifactory/" + key + "/" + path + suffix
				for _, caller := range []string{"admin", "reader", "user", ""} {
					asked.take()
					got := w.as(http.MethodGet, target, caller)
					switch caller {
					case "user":
						wantError(t, got, http.StatusForbidden, "Forbidden")
					case "":
						wantError(t, got, http.StatusUnauthorized, "Authentication is required")
					default:
						if got.Code != http.StatusOK {
							t.Errorf("%s as %s answered %d %.200s, want 200", target, caller,
								got.Code, got.Body)
						} else if path == rpmAlphaPath && !bytes.Equal(got.Body.Bytes(), alpha) {
							t.Errorf("%s as %s changed the package", target, caller)
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

// TestAccRPMVirtualPassesItsQueryAndMatrixParametersToARemoteMember
// (#1350, #1371): a virtual package GET over a remote member asks the
// upstream with the client's query, which the source boundary passes on
// only under Propagate Query Params, and its matrix parameters, as 7.x
// hands its member the request's context and appends them to the remote
// request URL (HttpRepo.downloadResource, 7.161.26). The member's repodata
// the virtual repository reads to find the package is a document the
// client did not ask for, so it goes without either [I].
func TestAccRPMVirtualPassesItsQueryAndMatrixParametersToARemoteMember(t *testing.T) {
	w, asked := rpmQueryWorld(t)
	_, _, primaryPath := rpmUpstreamFixture(t)
	target := "/artifactory/rpm-over-remote/" + rpmAlphaPath + ";x=1?q=1"
	if got := w.as(http.MethodGet, target, "admin"); got.Code != http.StatusOK {
		t.Errorf("%s answered %d %.200s, want 200", target, got.Code, got.Body)
	}
	want := []rpmAsk{{rpmRepomd, "", ""}, {primaryPath, "", ""}, {rpmAlphaPath, ";x=1", "q=1"}}
	if got := asked.take(); !slices.Equal(got, want) {
		t.Errorf("%s asked the upstream %v, want %v", target, got, want)
	}
}

// TestAccRPMMandatoryMatrixParameters (#1350): a virtual repository's
// package GET with a mandatory matrix parameter ("key+=value") passes over
// a member whose file lacks the property, a remote member's cached copy
// included, to the next member that lists the package, as 7.x's
// ArtifactoryVirtualDownloadStrategy passes over a member that does not
// find the file [I], and PropertiesImpl.matchQuery answers CONFLICT for a
// mandatory key the file lacks (7.161.26); with no member left it is not
// found. A virtual repository's composed repodata carries no properties
// [I], so a mandatory key is not found there, and a key it may lack is
// served.
func TestAccRPMMandatoryMatrixParameters(t *testing.T) {
	w, asked := rpmQueryWorld(t)
	alpha := rpmAlpha(t)
	for _, tc := range []struct {
		key, path string
		status    int
	}{
		{"rpm-virtual", rpmRepomd, http.StatusNotFound},
		{"rpm-virtual", rpmAlphaPath, http.StatusOK},
		{"rpm-untagged", rpmAlphaPath, http.StatusNotFound},
		{"rpm-over-held", rpmAlphaPath, http.StatusOK},
		{"rpm-tagged", rpmAlphaPath, http.StatusOK},
		{"rpm-local", rpmAlphaPath, http.StatusNotFound},
	} {
		target := "/artifactory/" + tc.key + "/" + tc.path + ";x+=1"
		for _, caller := range []string{"admin", "reader"} {
			got := w.as(http.MethodGet, target, caller)
			if tc.status == http.StatusNotFound {
				wantError(t, got, http.StatusNotFound, "Could not find resource")
				continue
			}
			if got.Code != tc.status {
				t.Errorf("%s as %s answered %d %.200s, want %d", target, caller, got.Code,
					got.Body, tc.status)
			} else if tc.path == rpmAlphaPath && !bytes.Equal(got.Body.Bytes(), alpha) {
				t.Errorf("%s as %s changed the package", target, caller)
			}
		}
		wantError(t, w.as(http.MethodGet, target, "user"), http.StatusForbidden, "Forbidden")
		// A virtual repository reads its remote member's repodata to find
		// the package; the package itself is matched in the cache alone.
		for _, a := range asked.take() {
			if a.path == tc.path {
				t.Errorf("%s asked the upstream for %v, want the cached copy matched", target, a)
			}
		}
	}
	// A key the file carries with another value does not match either.
	for _, target := range []string{
		"/artifactory/rpm-tagged/" + rpmAlphaPath + ";x=2",
		"/artifactory/rpm-virtual/" + rpmAlphaPath + ";x+=2",
	} {
		wantError(t, w.as(http.MethodGet, target, "admin"), http.StatusNotFound,
			"Could not find resource")
	}
	// A key the composed repodata may lack is served.
	target := "/artifactory/rpm-virtual/" + rpmRepomd + ";x=1"
	if got := w.as(http.MethodGet, target, "admin"); got.Code != http.StatusOK {
		t.Errorf("%s answered %d %.200s, want 200", target, got.Code, got.Body)
	}
}
