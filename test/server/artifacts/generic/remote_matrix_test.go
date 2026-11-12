package generic_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// matrixFile is the file the matrix tests fetch, and the bytes the upstream
// serves for it.
const (
	matrixPath = "/a/file.bin"
	matrixBody = "matrix bytes"
)

// matrixUpstream serves matrixBody at matrixPath and at that path with each
// of params after it, as an upstream Artifactory that resolves matrix
// parameters serves the file it finds: the fake upstream matches the
// decoded request path, so ";x%2B=1" arrives as ";x+=1".
func matrixUpstream(w *remoteWorld, params ...string) {
	w.up.Set(matrixPath, fakeupstream.Resource{Body: []byte(matrixBody)})
	for _, p := range params {
		w.up.Set(matrixPath+p, fakeupstream.Resource{Body: []byte(matrixBody)})
	}
}

// upstreamPaths returns the decoded path of every request the upstream has
// had since the last call, and forgets them.
func upstreamPaths(w *remoteWorld, seen *int) []string {
	all := w.up.Requests()
	var out []string
	for _, r := range all[*seen:] {
		out = append(out, r.Path)
	}
	*seen = len(all)
	return out
}

// wantMatrixError fails t unless rec is the JSON error answer with status
// and message.
func wantMatrixError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
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

// onlyPath reports whether paths is not empty and names want alone.
func onlyPath(paths []string, want string) bool {
	for _, p := range paths {
		if p != want {
			return false
		}
	}
	return len(paths) != 0
}

// TestAccGenericRemotePassesMatrixParametersUpstream (#1371): a remote
// repository asks its upstream for a file with the request's matrix
// parameters after the path, as 7.x appends them to the remote request URL
// (HttpRepo.downloadResource and assembleRetrieveInfoUrl, 7.161.26), and
// caches the answer at the bare path. Before, the parameters never reached
// the upstream. The next GET, with or without parameters, is matched
// against the cached copy without asking the upstream, as 7.x's
// RemoteRepoBase.internalGetInfo answers from the cache: a key the copy may
// lack is served, a mandatory key it lacks is not found
// (DbStoringRepoMixin.getInfo's PROPERTY_MISMATCH), and one it carries is
// served. A user without read permission is refused 403 and an anonymous
// caller 401 before the upstream is asked (DV-057).
func TestAccGenericRemotePassesMatrixParametersUpstream(t *testing.T) {
	w := newRemoteWorld(t)
	matrixUpstream(w, ";x=1")
	seen := 0
	target := "/artifactory/remote" + matrixPath
	wantMatrixError(t, w.request(http.MethodGet, target+";x=1", "denied", ""),
		http.StatusForbidden, "Forbidden")
	wantMatrixError(t, w.request(http.MethodGet, target+";x=1", "", ""),
		http.StatusUnauthorized, "Authentication is required")
	if got := upstreamPaths(w, &seen); len(got) != 0 {
		t.Fatalf("a caller without read reached the upstream: %q", got)
	}
	got := w.request(http.MethodGet, target+";x=1", "reader", "")
	if got.Code != http.StatusOK || got.Body.String() != matrixBody {
		t.Fatalf("GET with a parameter answered %d %q, want 200 %q", got.Code, got.Body,
			matrixBody)
	}
	if paths := upstreamPaths(w, &seen); !onlyPath(paths, matrixPath+";x=1") {
		t.Fatalf("the upstream was asked %q, want only %q", paths, matrixPath+";x=1")
	}
	if _, err := w.rt.Store.Items().Get(t.Context(), "remote", true, "a/file.bin"); err != nil {
		t.Fatalf("the copy is not cached at the bare path: %v", err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, target, http.StatusOK},
		{http.MethodGet, target + ";x=1", http.StatusOK},
		{http.MethodGet, target + ";y=2", http.StatusOK},
		{http.MethodHead, target + ";x=1", http.StatusOK},
		{http.MethodGet, "/artifactory/remote-cache" + matrixPath + ";x=1", http.StatusOK},
		{http.MethodGet, target + ";x+=1", http.StatusNotFound},
		{http.MethodHead, target + ";x+=1", http.StatusNotFound},
		{http.MethodGet, "/artifactory/remote-cache" + matrixPath + ";x+=1", http.StatusNotFound},
	} {
		for _, caller := range []string{"reader", "admin"} {
			got := w.request(tc.method, tc.path, caller, "")
			switch {
			case tc.status == http.StatusNotFound && tc.method == http.MethodGet:
				wantMatrixError(t, got, http.StatusNotFound, "Could not find resource")
			case got.Code != tc.status:
				t.Errorf("%s %s as %s answered %d, want %d", tc.method, tc.path, caller,
					got.Code, tc.status)
			case tc.method == http.MethodGet && got.Body.String() != matrixBody:
				t.Errorf("%s %s as %s answered %q, want %q", tc.method, tc.path, caller,
					got.Body, matrixBody)
			}
			if paths := upstreamPaths(w, &seen); len(paths) != 0 {
				t.Errorf("%s %s as %s asked the upstream %q, want the cache", tc.method,
					tc.path, caller, paths)
			}
		}
	}
	it, err := w.rt.Store.Items().Get(t.Context(), "remote", true, "a/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Items().SetProperties(t.Context(), it.ID,
		artmodel.Properties{"x": {"1"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got = w.request(http.MethodGet, target+";x+=1", "reader", "")
	if got.Code != http.StatusOK || got.Body.String() != matrixBody {
		t.Errorf("a mandatory key the copy carries answered %d %q, want 200", got.Code,
			got.Body)
	}
	wantMatrixError(t, w.request(http.MethodGet, target+";x=2", "reader", ""),
		http.StatusNotFound, "Could not find resource")
	if paths := upstreamPaths(w, &seen); len(paths) != 0 {
		t.Errorf("a cached copy asked the upstream %q", paths)
	}
}

// TestAccGenericRemoteServesTheUpstreamsAnswerUnmatched (#1351): with no
// copy cached, a mandatory matrix parameter goes to the upstream with the
// rest, and its answer is served unmatched, whether the remote stores it or
// streams it, as 7.x's downloadAndSave hands back the copy it saved and a
// remote that stores nothing streams the upstream's answer (7.161.26).
// Before, both answered 404 "Could not find resource", the GET and its
// ?trace alike. A copy stored that way carries no properties, so the next
// GET with the mandatory key is not found without asking the upstream,
// and its ?trace says why.
func TestAccGenericRemoteServesTheUpstreamsAnswerUnmatched(t *testing.T) {
	for _, store := range []bool{true, false} {
		name := "stored"
		if !store {
			name = "streamed"
		}
		t.Run(name, func(t *testing.T) {
			w := newRemoteWorld(t)
			w.c.StoreArtifactsLocally = &store
			w.update()
			matrixUpstream(w, ";x+=1")
			seen := 0
			target := "/artifactory/remote" + matrixPath + ";x+=1"
			for _, method := range []string{http.MethodHead, http.MethodGet} {
				got := w.request(method, target, "reader", "")
				if got.Code != http.StatusOK ||
					method == http.MethodGet && got.Body.String() != matrixBody {
					t.Fatalf("%s answered %d %q, want 200 and the upstream's bytes", method,
						got.Code, got.Body)
				}
				if paths := upstreamPaths(w, &seen); !onlyPath(paths, matrixPath+";x+=1") {
					t.Fatalf("%s asked the upstream %q, want only %q", method, paths,
						matrixPath+";x+=1")
				}
			}
			trace := w.request(http.MethodGet, target+"?trace", "reader", "")
			if !store {
				if trace.Code != http.StatusOK ||
					!strings.Contains(trace.Body.String(), "Request succeeded") {
					t.Errorf("trace of a streamed file answered %d %q", trace.Code, trace.Body)
				}
				return
			}
			wantMatrixError(t, w.request(http.MethodGet, target, "reader", ""),
				http.StatusNotFound, "Could not find resource")
			if trace.Code != http.StatusOK || !strings.Contains(trace.Body.String(),
				"Found resource in local cache, but property doesn't match.") ||
				!strings.Contains(trace.Body.String(), "Responding with unfound resource") {
				t.Errorf("trace of a cached copy that does not match answered %d %q",
					trace.Code, trace.Body)
			}
			if paths := upstreamPaths(w, &seen); len(paths) != 0 {
				t.Errorf("a cached copy that does not match asked the upstream %q", paths)
			}
		})
	}
}
