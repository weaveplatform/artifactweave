package opkghttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
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

// opkgAsk is one request the upstream had: the path, and the matrix
// parameters and query the source request carried for it.
type opkgAsk struct{ path, matrix, query string }

// opkgAsked records what a source backend was asked.
type opkgAsked struct {
	mu    sync.Mutex
	asked []opkgAsk
}

// over returns backend, recording each request in a.
func (a *opkgAsked) over(backend sourceBackend) sourceBackend {
	return func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
		a.mu.Lock()
		a.asked = append(a.asked, opkgAsk{q.Resource.Path, q.MatrixParams, q.Resource.Query})
		a.mu.Unlock()
		return backend(ctx, q, p)
	}
}

// take returns the requests the backend had since the last call.
func (a *opkgAsked) take() []opkgAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	return out
}

// tag sets the properties of the file at path in the repository key, or in
// its cache when cached is true, to x=1.
func (w *itemWorld) tag(key string, cached bool, path string) {
	w.t.Helper()
	it, err := w.db.Items().Get(w.t.Context(), key, cached, path)
	if err == nil {
		err = w.db.Items().SetProperties(w.t.Context(), it.ID,
			model.Properties{"x": {"1"}}, time.Now().UTC())
	}
	if err != nil {
		w.t.Fatal(err)
	}
}

// cache stores body as the file at path in the remote repository key's
// cache, blob first, as a download leaves it.
func (w *itemWorld) cache(key, path string, body []byte) {
	w.t.Helper()
	upload, err := w.blobs.BeginUpload(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := upload.Append(w.t.Context(), bytes.NewReader(body)); err != nil {
		w.t.Fatal(err)
	}
	info, err := upload.Commit(w.t.Context(), blob.Digests{})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.db.Items().Put(w.t.Context(), model.Item{
		RepoKey: key, Cached: true, Name: path, Size: int64(len(body)),
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256},
	}); err != nil {
		w.t.Fatal(err)
	}
}

// opkgQueryWorld is a local repository holding the 464xlat feed and IPK, a
// second whose feed and IPK carry the property x=1, a remote over the
// published feed, and virtual repositories over them.
func opkgQueryWorld(t *testing.T) (*itemWorld, *opkgAsked, []byte) {
	t.Helper()
	index, ipk := opkgUpstreamFixture(t)
	asked := &opkgAsked{}
	w := newItemWorld(t, withSource(asked.over(feedOf(t, index, ipk))))
	w.opkgRepos(
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{Key: "opkg-tagged", Class: repo.Local},
		repo.Config{Key: "opkg-remote", Class: repo.Remote},
		repo.Config{Key: "opkg-held", Class: repo.Remote},
		repo.Config{
			Key: "opkg-virtual", Class: repo.Virtual,
			Repositories: []string{"opkg-local", "opkg-tagged"},
		},
		repo.Config{
			Key: "opkg-untagged", Class: repo.Virtual, Repositories: []string{"opkg-local"},
		},
		repo.Config{
			Key: "opkg-over-remote", Class: repo.Virtual, Repositories: []string{"opkg-remote"},
		},
		repo.Config{
			Key: "opkg-over-held", Class: repo.Virtual,
			Repositories: []string{"opkg-held", "opkg-tagged"},
		},
	)
	for _, key := range []string{"opkg-local", "opkg-tagged"} {
		w.put(key, opkgFeed, index)
		w.put(key, opkgXlat, ipk)
	}
	w.tag("opkg-tagged", false, opkgFeed)
	w.tag("opkg-tagged", false, opkgXlat)
	w.cache("opkg-held", opkgFeed, index)
	w.cache("opkg-held", opkgXlat, ipk)
	return w, asked, ipk
}

// TestAccOpkgRoutesTakeAQueryAndMatrixParameters (#1350): an Opkg feed
// and IPK, of a local, remote or virtual repository, take a query and
// matrix parameters as any file's GET takes them. Before, the Opkg routes
// answered 404 "Could not find resource" to either, before the read
// check, so an administrator got 404 for a file that exists and a caller
// without read got 404 instead of 403 or 401. 7.x documents
// skipUpdateStats and trace for a GET of any file (RTFACT-25636, 7.21.3),
// and matrix parameters "make resolution conditional on properties"
// (JFrog's guide, Using Properties in Deployment and Resolution); neither
// names a package type it leaves out. The read check comes first (DV-057):
// a user without read is refused 403 and an anonymous caller 401, without
// asking the upstream.
func TestAccOpkgRoutesTakeAQueryAndMatrixParameters(t *testing.T) {
	w, asked, ipk := opkgQueryWorld(t)
	for _, key := range []string{"opkg-local", "opkg-remote", "opkg-virtual"} {
		for _, path := range []string{opkgFeed, opkgXlat} {
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
						} else if path == opkgXlat && !bytes.Equal(got.Body.Bytes(), ipk) {
							t.Errorf("%s as %s changed the IPK", target, caller)
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

// TestAccOpkgRemotePassesItsQueryAndMatrixParametersUpstream (#1350,
// #1371): a remote's feed or IPK GET asks the upstream with the client's
// query, which the source boundary passes on only under Propagate Query
// Params, and its matrix parameters, as for any remote file
// (HttpRepo.downloadResource, 7.161.26). The feed an IPK GET reads to pin
// the IPK's SHA-256, and a member's feed a virtual repository composes,
// are documents the client did not ask for, so they go without either
// [I]. A virtual IPK GET over the remote passes the client's matrix
// parameters and query on to the member, as 7.x hands its member the
// request's context.
func TestAccOpkgRemotePassesItsQueryAndMatrixParametersUpstream(t *testing.T) {
	w, asked, _ := opkgQueryWorld(t)
	for _, tc := range []struct {
		target string
		want   []opkgAsk
	}{
		{
			"/artifactory/opkg-remote/" + opkgFeed + ";x=1?q=1",
			[]opkgAsk{{opkgFeed, ";x=1", "q=1"}},
		},
		{
			"/artifactory/opkg-remote/" + opkgXlat + ";x=1?q=1",
			[]opkgAsk{{opkgFeed, "", ""}, {opkgXlat, ";x=1", "q=1"}},
		},
		{
			"/artifactory/opkg-over-remote/" + opkgFeed + ";x=1?q=1",
			[]opkgAsk{{opkgFeed, "", ""}},
		},
		{
			"/artifactory/opkg-over-remote/" + opkgXlat + ";x=1?q=1",
			[]opkgAsk{{opkgFeed, "", ""}, {opkgXlat, ";x=1", "q=1"}},
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

// TestAccOpkgMandatoryMatrixParameters (#1350): a mandatory matrix
// parameter ("key+=value") serves a stored feed or IPK only when the file
// carries the property, as 7.x's PropertiesImpl.matchQuery answers
// CONFLICT, and DbStoringRepoMixin.getInfo PROPERTY_MISMATCH, for a
// mandatory key the file lacks (7.161.26); a remote's cached copy is
// matched the same way, without asking the upstream (#1371). A virtual
// repository's IPK GET passes over a member whose file lacks it to the next
// member that lists the IPK, as 7.x's ArtifactoryVirtualDownloadStrategy
// passes over a member that does not find the file [I]. A virtual
// repository's composed feed carries no properties [I], so a mandatory key
// is not found there.
func TestAccOpkgMandatoryMatrixParameters(t *testing.T) {
	w, asked, ipk := opkgQueryWorld(t)
	for _, tc := range []struct {
		key, path string
		status    int
	}{
		{"opkg-local", opkgFeed, http.StatusNotFound},
		{"opkg-local", opkgXlat, http.StatusNotFound},
		{"opkg-tagged", opkgFeed, http.StatusOK},
		{"opkg-tagged", opkgXlat, http.StatusOK},
		{"opkg-held", opkgFeed, http.StatusNotFound},
		{"opkg-held", opkgXlat, http.StatusNotFound},
		{"opkg-virtual", opkgFeed, http.StatusNotFound},
		{"opkg-virtual", opkgXlat, http.StatusOK},
		{"opkg-over-held", opkgXlat, http.StatusOK},
		{"opkg-untagged", opkgXlat, http.StatusNotFound},
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
			} else if tc.path == opkgXlat && !bytes.Equal(got.Body.Bytes(), ipk) {
				t.Errorf("%s as %s changed the IPK", target, caller)
			}
		}
		wantError(t, w.as(http.MethodGet, target, "user"), http.StatusForbidden, "Forbidden")
		// A virtual repository reads its remote member's feed to find the
		// IPK; the file the client asked for is matched in the cache alone.
		for _, a := range asked.take() {
			if a.path == tc.path {
				t.Errorf("%s asked the upstream for %v, want the cached copy matched", target, a)
			}
		}
	}
	// A key the file carries with another value does not match either.
	for _, target := range []string{
		"/artifactory/opkg-tagged/" + opkgXlat + ";x=2",
		"/artifactory/opkg-virtual/" + opkgXlat + ";x+=2",
	} {
		wantError(t, w.as(http.MethodGet, target, "admin"), http.StatusNotFound,
			"Could not find resource")
	}
	// A key the file may lack is served, by the composed feed and by the
	// remote whose cached copies lack the mandatory one.
	for _, target := range []string{
		"/artifactory/opkg-virtual/" + opkgFeed + ";x=1",
		"/artifactory/opkg-held/" + opkgFeed + ";x=1",
		"/artifactory/opkg-held/" + opkgXlat + ";x=1",
	} {
		got := w.as(http.MethodGet, target, "admin")
		if got.Code != http.StatusOK || strings.HasSuffix(target, opkgFeed+";x=1") &&
			!strings.Contains(got.Body.String(), "464xlat") {
			t.Errorf("%s answered %d %.200s, want 200", target, got.Code, got.Body)
		}
	}
}
