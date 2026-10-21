package pkgroute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestServeResultRecordsOnlyCompleteDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, method, condition string
		status                  int
		used                    bool
	}{
		{"get", "GET", "", 200, true}, {"head", "HEAD", "", 200, false}, {"conditional", "GET", `"tag"`, 304, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used := false
			k := &Kit{p: Protocol{Surface: apierror.NPM}}
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

func TestServeResultRevalidatesAndAbortsBrokenStream(t *testing.T) {
	k := &Kit{p: Protocol{Surface: apierror.NPM}}
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
