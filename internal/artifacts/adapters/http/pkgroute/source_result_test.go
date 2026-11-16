package pkgroute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestAccServeResultRecordsOnlyCompleteDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, method, condition string
		status                  int
		used                    bool
	}{
		{"get", "GET", "", 200, true}, {"head", "HEAD", "", 200, false}, {"conditional", "GET", `"tag"`, 304, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used := false
			k := &Kit{p: Protocol{ErrorProfile: apierror.ProfileNPM}}
			r := httptest.NewRequest(tc.method, "/", nil)
			r.Header.Set("If-None-Match", tc.condition)
			w := httptest.NewRecorder()
			result := source.Result{
				Stream: &source.Stream{
					Body:           io.NopCloser(strings.NewReader("body")),
					Status:         200,
					Representation: source.Representation{Size: 4, ETag: `"tag"`},
				},
				Used: func(context.Context) error { used = true; return nil },
			}
			// A seekable body lets standard conditional handling evaluate the ETag.
			result.Stream.Body = &seekCloser{Reader: strings.NewReader("body")}
			k.ServeResult(w, r, result)
			if w.Code != tc.status || used != tc.used {
				t.Fatalf("status %d used %v", w.Code, used)
			}
		})
	}
}

type seekCloser struct{ *strings.Reader }

func (*seekCloser) Close() error { return nil }

func TestAccServeResultRevalidatesAndAbortsBrokenStream(t *testing.T) {
	k := &Kit{p: Protocol{ErrorProfile: apierror.ProfileNPM}}
	r := httptest.NewRequest("GET", "/", nil)
	result := source.Result{
		Stream: &source.Stream{
			Body:           io.NopCloser(strings.NewReader("body")),
			Status:         200,
			Representation: source.Representation{Size: 4},
		},
		Revalidate: func(context.Context) error { return source.ErrDenied },
	}
	w := httptest.NewRecorder()
	k.ServeResult(w, r, result)
	if w.Code != 403 {
		t.Fatalf("denied: %d", w.Code)
	}
	result.Revalidate = nil
	result.Stream.Body = io.NopCloser(strings.NewReader("short"))
	result.Stream.Size = 20
	var fault bool
	k.o.Fault = func(*http.Request, error) { fault = true }
	defer func() {
		if got := recover(); !errors.Is(asError(got), http.ErrAbortHandler) || !fault {
			t.Fatalf("panic %v fault %v", got, fault)
		}
	}()
	k.ServeResult(httptest.NewRecorder(), r, result)
}
func asError(v any) error { e, _ := v.(error); return e }

// ServeResult names a remote file's stream by Options.NameStream for the
// request's repository and the stream's path, and asks nothing for a
// stream without a path (#1594).
func TestUnitServeResultNamesARemoteFilesStream(t *testing.T) {
	var asked []string
	k := &Kit{p: Protocol{ErrorProfile: apierror.ProfileNPM}}
	k.o.NameStream = func(c repo.Config, path string) FileNames {
		asked = append(asked, c.Key+":"+path)
		return func(h http.Header, _ *http.Request, _ bool) {
			h.Set("X-Artifactory-Filename", "a.whl")
		}
	}
	for _, path := range []string{"pkg/a.whl", ""} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), requestKey{},
			Request{Repo: repo.Config{Key: "remote"}}))
		w := httptest.NewRecorder()
		k.ServeResult(w, r, source.Result{Stream: &source.Stream{
			Body: &seekCloser{Reader: strings.NewReader("body")}, Status: http.StatusOK,
			Representation: source.Representation{Size: 4, Path: path},
		}})
		named := w.Header().Get("X-Artifactory-Filename") == "a.whl"
		if w.Code != http.StatusOK || named != (path != "") {
			t.Errorf("path %q: %d, named %v", path, w.Code, named)
		}
	}
	if len(asked) != 1 || asked[0] != "remote:pkg/a.whl" {
		t.Fatalf("NameStream asked for %v", asked)
	}
}
