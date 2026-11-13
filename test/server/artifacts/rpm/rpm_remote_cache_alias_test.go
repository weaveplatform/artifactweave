package rpm_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccRPMRemoteCacheAliasServesCachedRepodata (#1382, the same mechanism
// as a Debian remote's Release): once an RPM remote repository has fetched
// its upstream's repomd.xml and the primary, filelists and other metadata it
// lists, its -cache alias serves the cached bytes, where it answered 404
// "Could not find resource", without asking the upstream: JFrog's Remote
// Repositories page reads what is "already stored in the cache" at
// <remote-repository-name>-cache "to avoid remote update checks". Before
// the remote's GET the alias still answers 404 "Could not find resource"
// and asks nothing. A user without read permission is refused 403 and an
// anonymous caller 401.
func TestAccRPMRemoteCacheAliasServesCachedRepodata(t *testing.T) {
	dir := repopath.Path(t, "test/acceptance/testdata/rpm-upstream/repodata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files["repodata/"+e.Name()] = body
	}
	if len(files) != 4 {
		t.Fatalf("the upstream fixture holds %d repodata files, want 4", len(files))
	}
	for p, body := range files {
		t.Run(p, func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			take := func() []string {
				mu.Lock()
				defer mu.Unlock()
				out := asked
				asked = nil
				return out
			}
			r := chi.NewRouter()
			r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				asked = append(asked, r.URL.RequestURI())
				mu.Unlock()
				b, ok := files[chi.URLParam(r, "*")]
				if !ok {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(b)
			})
			up := httptest.NewServer(r)
			t.Cleanup(up.Close)
			w := harness.NewRemoteWorld(t)
			w.Repo.PackageType = repo.RPM
			w.Repo.URL = up.URL
			w.Update()
			alias := "/artifactory/remote-cache/" + p
			wantRefusal(t, w.Request(http.MethodGet, alias, "admin", ""),
				http.StatusNotFound, "Could not find resource")
			if got := take(); got != nil {
				t.Errorf("the alias asked the upstream %q before anything was cached", got)
			}
			got := w.Request(http.MethodGet, "/artifactory/remote/"+p, "reader", "")
			if got.Code != http.StatusOK || got.Body.String() != string(body) {
				t.Fatalf("remote answered %d %q, want 200 and the upstream's bytes",
					got.Code, got.Body)
			}
			if got := take(); !slices.Equal(got, []string{"/" + p}) {
				t.Errorf("the remote asked the upstream %q, want %q", got, "/"+p)
			}
			for _, caller := range []string{"denied", "", "reader", "admin"} {
				r := w.Request(http.MethodGet, alias, caller, "")
				switch caller {
				case "denied":
					wantRefusal(t, r, http.StatusForbidden, "Forbidden")
				case "":
					wantRefusal(t, r, http.StatusUnauthorized, "Authentication is required")
				default:
					if r.Code != http.StatusOK || r.Body.String() != string(body) {
						t.Errorf("%s on the alias answered %d, want 200 and the cached bytes",
							caller, r.Code)
					}
				}
				if got := take(); got != nil {
					t.Errorf("%s on the alias asked the upstream %q", caller, got)
				}
			}
		})
	}
}

// wantRefusal fails the test unless rec answered status with the error
// envelope that carries message.
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
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
