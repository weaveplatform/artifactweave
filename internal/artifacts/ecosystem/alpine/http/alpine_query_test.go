package alpinehttp

import (
	"bytes"
	"context"
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

// alpineAsk is one request the upstream had: the path, and the matrix
// parameters and query the source request carried for it.
type alpineAsk struct{ path, matrix, query string }

// alpineAsked records what a source backend was asked.
type alpineAsked struct {
	mu    sync.Mutex
	asked []alpineAsk
}

// over returns backend, recording each request in a.
func (a *alpineAsked) over(backend sourceBackend) sourceBackend {
	return func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
		a.mu.Lock()
		a.asked = append(a.asked, alpineAsk{q.Resource.Path, q.MatrixParams, q.Resource.Query})
		a.mu.Unlock()
		return backend(ctx, q, p)
	}
}

// take returns the requests the backend had since the last call.
func (a *alpineAsked) take() []alpineAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	return out
}

// tag sets the properties of it to x=1.
func (w *itemWorld) tag(it model.Item) {
	w.t.Helper()
	if err := w.db.Items().SetProperties(w.t.Context(), it.ID,
		model.Properties{"x": {"1"}}, time.Now().UTC()); err != nil {
		w.t.Fatal(err)
	}
}

// alpineQueryWorld is a local repository holding an index that lists
// hello and its package, a second whose index and package carry the
// property x=1, a remote over the same, a remote whose cache holds both
// without the property, and virtual repositories over them.
func alpineQueryWorld(t *testing.T) (*itemWorld, *alpineAsked) {
	t.Helper()
	index := apkIndex(t, "hello")
	asked := &alpineAsked{}
	w := newItemWorld(t, withSource(asked.over(upstream(map[string]func() (source.Result, error){
		apkIndexPath: streamed(index, nil),
		apkHelloPath: streamed(apkBody, nil),
	}))))
	w.repos(
		alpineRepo(alpineLocal, repo.Local),
		alpineRepo("alpine-tagged", repo.Local),
		alpineRepo(alpineRemote, repo.Remote),
		alpineRepo("alpine-held", repo.Remote),
		alpineRepo(alpineVirtual, repo.Virtual, alpineLocal, "alpine-tagged"),
		alpineRepo("alpine-untagged", repo.Virtual, alpineLocal),
		alpineRepo("alpine-over-remote", repo.Virtual, alpineRemote),
		alpineRepo("alpine-over-held", repo.Virtual, "alpine-held", "alpine-tagged"),
	)
	w.put(alpineLocal, apkIndexPath, index)
	w.put(alpineLocal, apkHelloPath, apkBody)
	w.tag(w.put("alpine-tagged", apkIndexPath, index))
	w.tag(w.put("alpine-tagged", apkHelloPath, apkBody))
	w.world.Put(t, repo.Config{Key: "alpine-held"}, true, apkIndexPath, index)
	w.world.Put(t, repo.Config{Key: "alpine-held"}, true, apkHelloPath, apkBody)
	return w, asked
}

// TestAccAlpineRoutesTakeAQueryAndMatrixParameters (#1350): an Alpine
// index, of a local, remote or virtual repository, and a package, take a
// query and matrix parameters as any file's GET takes them. Before, the
// index route, and a virtual repository's package route, answered 404
// "Could not find resource" to either, before the read check, so an
// administrator got 404 for a file that exists and a caller without read
// got 404 instead of 403 or 401. 7.x documents skipUpdateStats and trace
// for a GET of any file (RTFACT-25636, 7.21.3), and matrix parameters "make
// resolution conditional on properties" (JFrog's guide, Using Properties
// in Deployment and Resolution); neither names a package type it leaves
// out. The read check comes first (DV-057): a user without read is refused
// 403 and an anonymous caller 401, without asking the upstream.
func TestAccAlpineRoutesTakeAQueryAndMatrixParameters(t *testing.T) {
	w, asked := alpineQueryWorld(t)
	for _, key := range []string{alpineLocal, alpineRemote, alpineVirtual} {
		for _, path := range []string{apkIndexPath, apkHelloPath} {
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
						} else if path == apkHelloPath && !bytes.Equal(got.Body.Bytes(), apkBody) {
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

// TestAccAlpineRemotePassesItsQueryAndMatrixParametersUpstream (#1350,
// #1371): a remote's index or package GET asks the upstream with the
// client's query, which the source boundary passes on only under Propagate
// Query Params, and its matrix parameters, as for any remote file
// (HttpRepo.downloadResource, 7.161.26). A member's index a virtual
// repository composes, or reads to find a package, is a document the
// client did not ask for, so it goes without either [I]. A virtual package
// GET over the remote passes the client's matrix parameters and query on
// to the member, as 7.x hands its member the request's context.
func TestAccAlpineRemotePassesItsQueryAndMatrixParametersUpstream(t *testing.T) {
	w, asked := alpineQueryWorld(t)
	for _, tc := range []struct {
		target string
		want   []alpineAsk
	}{
		{
			"/artifactory/alpine-remote/" + apkIndexPath + ";x=1?q=1",
			[]alpineAsk{{apkIndexPath, ";x=1", "q=1"}},
		},
		{
			"/artifactory/alpine-remote/" + apkHelloPath + ";x=1?q=1",
			[]alpineAsk{{apkHelloPath, ";x=1", "q=1"}},
		},
		{
			"/artifactory/alpine-over-remote/" + apkIndexPath + ";x=1?q=1",
			[]alpineAsk{{apkIndexPath, "", ""}},
		},
		{
			"/artifactory/alpine-over-remote/" + apkHelloPath + ";x=1?q=1",
			[]alpineAsk{{apkIndexPath, "", ""}, {apkHelloPath, ";x=1", "q=1"}},
		},
	} {
		if got := w.as(http.MethodGet, tc.target, "admin"); got.Code != http.StatusOK {
			t.Errorf("%s answered %d %.200s, want 200", tc.target, got.Code, got.Body)
		}
		if got := asked.take(); !slices.Equal(got, tc.want) {
			t.Errorf("%s asked the upstream %v, want %v", tc.target, got, tc.want)
		}
	}
}

// TestAccAlpineMandatoryMatrixParameters (#1350): a mandatory matrix
// parameter ("key+=value") serves a stored index or package only when the
// file carries the property, as 7.x's PropertiesImpl.matchQuery answers
// CONFLICT, and DbStoringRepoMixin.getInfo PROPERTY_MISMATCH, for a
// mandatory key the file lacks (7.161.26); a remote's cached copy is
// matched the same way, without asking the upstream (#1371). A virtual
// repository's package GET passes over a member whose file lacks it to the
// next member that lists the package, as 7.x's
// ArtifactoryVirtualDownloadStrategy passes over a member that does not
// find the file [I]. A virtual repository's composed index carries no
// properties [I], so a mandatory key is not found there, and a key it may
// lack is served.
func TestAccAlpineMandatoryMatrixParameters(t *testing.T) {
	w, asked := alpineQueryWorld(t)
	for _, tc := range []struct {
		key, path string
		status    int
	}{
		{alpineLocal, apkIndexPath, http.StatusNotFound},
		{alpineLocal, apkHelloPath, http.StatusNotFound},
		{"alpine-tagged", apkIndexPath, http.StatusOK},
		{"alpine-tagged", apkHelloPath, http.StatusOK},
		{"alpine-held", apkIndexPath, http.StatusNotFound},
		{"alpine-held", apkHelloPath, http.StatusNotFound},
		{alpineVirtual, apkIndexPath, http.StatusNotFound},
		{alpineVirtual, apkHelloPath, http.StatusOK},
		{"alpine-over-held", apkHelloPath, http.StatusOK},
		{"alpine-untagged", apkHelloPath, http.StatusNotFound},
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
			} else if tc.path == apkHelloPath && !bytes.Equal(got.Body.Bytes(), apkBody) {
				t.Errorf("%s as %s changed the package", target, caller)
			}
		}
		wantError(t, w.as(http.MethodGet, target, "user"), http.StatusForbidden, "Forbidden")
		// A virtual repository reads its remote member's index to find the
		// package; the file the client asked for is matched in the cache
		// alone.
		for _, a := range asked.take() {
			if a.path == tc.path {
				t.Errorf("%s asked the upstream for %v, want the cached copy matched", target, a)
			}
		}
	}
	// A key the file carries with another value does not match either.
	for _, target := range []string{
		"/artifactory/alpine-tagged/" + apkHelloPath + ";x=2",
		"/artifactory/" + alpineVirtual + "/" + apkHelloPath + ";x+=2",
	} {
		wantError(t, w.as(http.MethodGet, target, "admin"), http.StatusNotFound,
			"Could not find resource")
	}
	// A key the file may lack is served, by the composed index and by the
	// remote whose cached copies lack the mandatory one.
	for _, target := range []string{
		"/artifactory/" + alpineVirtual + "/" + apkIndexPath + ";x=1",
		"/artifactory/alpine-held/" + apkIndexPath + ";x=1",
		"/artifactory/alpine-held/" + apkHelloPath + ";x=1",
	} {
		if got := w.as(http.MethodGet, target, "admin"); got.Code != http.StatusOK {
			t.Errorf("%s answered %d %.200s, want 200", target, got.Code, got.Body)
		}
	}
}
