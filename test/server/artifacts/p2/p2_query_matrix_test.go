package p2_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// p2Ask is one request the upstream had: the path, and the matrix
// parameters and query the source request carried for it.
type p2Ask struct{ path, matrix, query string }

// p2Asked is an upstream that serves the published p2.index and
// content.jar at a remote's own URL, and an update-site archive holding
// that p2.index at any other, recording what it was asked.
type p2Asked struct {
	mu    sync.Mutex
	asked []p2Ask
}

// service returns a remote source service over the upstream.
func (a *p2Asked) service(t *testing.T) *source.Service {
	index, content := p2Fixture(t, "p2.index"), p2Fixture(t, "content.jar")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("p2.index")
	if err == nil {
		_, err = entry.Write(index)
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			a.mu.Lock()
			a.asked = append(a.asked, p2Ask{q.Resource.Path, q.MatrixParams, q.Resource.Query})
			a.mu.Unlock()
			body := map[string][]byte{"p2.index": index, "content.jar": content}[q.Resource.Path]
			if q.Resource.ExternalURL != "" {
				body = archive.Bytes()
			}
			if body == nil {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)}
}

// take returns the requests the upstream had since the last call.
func (a *p2Asked) take() []p2Ask {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	return out
}

// wantP2Error fails t unless rec answered status with the Artifactory
// error envelope that carries message.
func wantP2Error(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// p2QueryWorld is a Generic local update site holding the published
// p2.index and content.jar, a second whose content.jar carries the
// property x=1, a P2 remote, a P2 remote over an update-site archive whose
// cache holds its p2.index without the property, and a P2 virtual
// repository over them.
func p2QueryWorld(t *testing.T) (*itemWorld, *p2Asked) {
	t.Helper()
	asked := &p2Asked{}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = asked.service(t) })
	for _, c := range []repo.Config{
		{Key: "p2-local", Class: repo.Local, PackageType: repo.Generic},
		{Key: "p2-tagged", Class: repo.Local, PackageType: repo.Generic},
		{Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2, URL: "https://p2.example/site/"},
		{
			Key: "p2-zip", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://p2.example/site.zip!/",
		},
		{
			Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-local", "p2-tagged", "p2-remote"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"p2-local", "p2-tagged"} {
		for _, name := range []string{"p2.index", "content.jar"} {
			res := w.do(http.MethodPut, "/artifactory/"+key+"/"+name, "admin",
				string(p2Fixture(t, name)))
			if res.Code != http.StatusCreated {
				t.Fatalf("publish %s/%s: %d %s", key, name, res.Code, res.Body)
			}
		}
	}
	it := w.item("p2-tagged", false, "content.jar")
	if err := w.db.Items().SetProperties(t.Context(), it.ID,
		artmodel.Properties{"x": {"1"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Items().Put(t.Context(), artmodel.Item{
		RepoKey: "p2-zip", Cached: true, Name: "p2.index",
	}); err != nil {
		t.Fatal(err)
	}
	return w, asked
}

// TestAccP2RoutesTakeAQueryAndMatrixParameters (#1350): a P2 remote's
// files, an update-site archive's entries, a P2 virtual repository's own
// p2.index and composite metadata, and the member files it serves under
// each member's key, take a query and matrix parameters as any file's GET
// takes them. Before, every P2 route answered 404 "Could not find
// resource" to either, before the read check, so an administrator got 404
// for a file that exists and a caller without read got 404 instead of 403
// or 401. 7.x documents skipUpdateStats and trace for a GET of any file
// (RTFACT-25636, 7.21.3), and matrix parameters "make resolution
// conditional on properties" (JFrog's guide, Using Properties in
// Deployment and Resolution); neither names a package type it leaves out.
// The read check comes first (DV-057): a user without read is refused 403
// and an anonymous caller 401, without asking the upstream.
func TestAccP2RoutesTakeAQueryAndMatrixParameters(t *testing.T) {
	w, asked := p2QueryWorld(t)
	for _, path := range []string{
		"p2-remote/content.jar",
		"p2-remote/p2.index",
		"p2-zip/p2.index",
		"p2-virtual/p2.index",
		"p2-virtual/compositeContent.xml",
		"p2-virtual/p2-local/content.jar",
		"p2-virtual/p2-remote/content.jar",
	} {
		for _, suffix := range []string{"?q=1", ";x=1"} {
			target := "/artifactory/" + path + suffix
			for _, caller := range []string{"admin", "reader", "user", ""} {
				asked.take()
				got := w.do(http.MethodGet, target, caller, "")
				switch caller {
				case "user":
					wantP2Error(t, got, http.StatusForbidden, "Forbidden")
				case "":
					wantP2Error(t, got, http.StatusUnauthorized, "Authentication is required")
				default:
					if got.Code != http.StatusOK {
						t.Errorf("%s as %s answered %d %.200s, want 200", target, caller,
							got.Code, got.Body)
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

// TestAccP2RemotePassesItsQueryAndMatrixParametersUpstream (#1350, #1371):
// a P2 remote's file GET asks the upstream with the client's query, which
// the source boundary passes on only under Propagate Query Params, and its
// matrix parameters, as for any remote file (HttpRepo.downloadResource,
// 7.161.26), directly and under its key in a virtual repository, which
// hands its member the request's context. An update-site archive is
// fetched from a URL other than the remote's own, so its entry's GET passes
// neither [I].
func TestAccP2RemotePassesItsQueryAndMatrixParametersUpstream(t *testing.T) {
	w, asked := p2QueryWorld(t)
	for _, tc := range []struct {
		target string
		want   []p2Ask
	}{
		{"/artifactory/p2-remote/content.jar;x=1?q=1", []p2Ask{{"content.jar", ";x=1", "q=1"}}},
		{
			"/artifactory/p2-virtual/p2-remote/content.jar;x=1?q=1",
			[]p2Ask{{"content.jar", ";x=1", "q=1"}},
		},
		{"/artifactory/p2-zip/p2.index;x=1?q=1", []p2Ask{{"p2.index", "", ""}}},
	} {
		if got := w.do(http.MethodGet, tc.target, "admin", ""); got.Code != http.StatusOK {
			t.Errorf("%s answered %d %.200s, want 200", tc.target, got.Code, got.Body)
		}
		if got := asked.take(); !slices.Equal(got, tc.want) {
			t.Errorf("%s asked the upstream %v, want %v", tc.target, got, tc.want)
		}
	}
}

// TestAccP2MandatoryMatrixParameters (#1350): a mandatory matrix parameter
// ("key+=value") serves a member's file under its key only when the file
// carries the property, as 7.x's PropertiesImpl.matchQuery answers
// CONFLICT, and DbStoringRepoMixin.getInfo PROPERTY_MISMATCH, for a
// mandatory key the file lacks (7.161.26); an update-site archive's cached
// entry is matched the same way, without asking the upstream (#1371). A
// virtual repository's own p2.index and composite metadata carry no
// properties [I], so a mandatory key is not found there, and a key they
// may lack is served.
func TestAccP2MandatoryMatrixParameters(t *testing.T) {
	w, asked := p2QueryWorld(t)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"p2-virtual/p2-local/content.jar", http.StatusNotFound},
		{"p2-virtual/p2-tagged/content.jar", http.StatusOK},
		{"p2-virtual/p2.index", http.StatusNotFound},
		{"p2-virtual/compositeArtifacts.xml", http.StatusNotFound},
		{"p2-zip/p2.index", http.StatusNotFound},
	} {
		target := "/artifactory/" + tc.path + ";x+=1"
		for _, caller := range []string{"admin", "reader"} {
			got := w.do(http.MethodGet, target, caller, "")
			if tc.status == http.StatusNotFound {
				wantP2Error(t, got, http.StatusNotFound, "Could not find resource")
			} else if got.Code != tc.status {
				t.Errorf("%s as %s answered %d %.200s, want %d", target, caller, got.Code,
					got.Body, tc.status)
			}
		}
		wantP2Error(t, w.do(http.MethodGet, target, "user", ""), http.StatusForbidden,
			"Forbidden")
		if got := asked.take(); len(got) != 0 {
			t.Errorf("%s asked the upstream %v", target, got)
		}
	}
	wantP2Error(t, w.do(http.MethodGet, "/artifactory/p2-virtual/p2-tagged/content.jar;x=2",
		"admin", ""), http.StatusNotFound, "Could not find resource")
	for _, target := range []string{
		"/artifactory/p2-virtual/compositeArtifacts.xml;x=1",
		"/artifactory/p2-zip/p2.index;x=1",
	} {
		if got := w.do(http.MethodGet, target, "admin", ""); got.Code != http.StatusOK {
			t.Errorf("%s answered %d %.200s, want 200", target, got.Code, got.Body)
		}
	}
}
