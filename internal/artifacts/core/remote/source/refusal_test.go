package source_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// TestUnitMIMERefusalRemembered: a MIME refusal of a read with ClientMIME
// is answered again for its path without the backend until the period
// passes, as 7.x's missed-retrieval cache answers it (#1376); a read
// without ClientMIME, a cache-only read and a period of zero remember
// nothing, and ForgetRefusals, which a zap calls, clears the path and
// what is below it.
func TestUnitMIMERefusalRemembered(t *testing.T) {
	refusal := &source.MIMERefusal{
		RejectionError: &format.RejectionError{Status: http.StatusConflict, Message: "refused"},
	}
	calls := 0
	refuse := backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		calls++
		return source.Result{}, refusal
	})
	s := &source.Service{Resolve: identity, HTTP: refuse, Cache: refuse}
	read := func(path string, client, cacheOnly bool, period *int) {
		t.Helper()
		q := request(t, "")
		q.Resource.Path, q.Resource.EscapedPath, q.CacheOnly = path, path, cacheOnly
		q.Repo.MissedRetrievalCachePeriodSecs = period
		if client {
			q.Resource.ClientMIME = &source.ClientMIME{}
		}
		_, err := s.Read(t.Context(), q)
		var rejection *format.RejectionError
		if !errors.As(err, &rejection) || rejection.Status != http.StatusConflict ||
			rejection.Message != "refused" {
			t.Fatalf("%s: %v, want the refusal", path, err)
		}
	}
	wantCalls := func(path string, want int) {
		t.Helper()
		if calls != want {
			t.Fatalf("%s: %d backend reads, want %d", path, calls, want)
		}
	}
	read("remembered/a.pkg", true, false, nil)
	read("remembered/a.pkg", true, false, nil)
	wantCalls("remembered", 1)
	read("format/a.pkg", false, false, nil)
	read("format/a.pkg", false, false, nil)
	wantCalls("format route", 3)
	read("cache/a.pkg", true, true, nil)
	read("cache/a.pkg", true, true, nil)
	wantCalls("cache-only", 5)
	zero := 0
	read("zero/a.pkg", true, false, &zero)
	read("zero/a.pkg", true, false, &zero)
	wantCalls("zero period", 7)

	s.ForgetRefusals("other", "")
	read("remembered/a.pkg", true, false, nil)
	wantCalls("another repository forgotten", 7)
	s.ForgetRefusals("up", "remembered/a")
	read("remembered/a.pkg", true, false, nil)
	wantCalls("a sibling's prefix forgotten", 7)
	s.ForgetRefusals("up", "remembered")
	read("remembered/a.pkg", true, false, nil)
	wantCalls("folder forgotten", 8)
	s.ForgetRefusals("up", "")
	read("remembered/a.pkg", true, false, nil)
	wantCalls("repository forgotten", 9)
}

// TestUnitMIMERefusalOnServedResult: a result the backend served from an
// expired cached file despite a refusal is delivered, and its refusal is
// answered for the path's next reads without the backend, as 7.x serves
// the expired file for a rejected resource and its missed-retrieval cache
// answers the requests after it (RemoteRepoBase.internalGetInfo,
// addRejectedResourceToMissedCache).
func TestUnitMIMERefusalOnServedResult(t *testing.T) {
	refusal := &source.MIMERefusal{
		RejectionError: &format.RejectionError{Status: http.StatusConflict, Message: "refused"},
	}
	calls := 0
	served := backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		calls++
		return source.Result{
			Stream: &source.Stream{
				Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("expired bytes")),
			},
			Refused: refusal,
		}, nil
	})
	s := &source.Service{Resolve: identity, HTTP: served}
	q := request(t, "")
	q.Resource.Path, q.Resource.EscapedPath = "served/a.pkg", "served/a.pkg"
	q.Resource.ClientMIME = &source.ClientMIME{}
	result, err := s.Read(t.Context(), q)
	if err != nil || result.Stream == nil {
		t.Fatalf("first read: %v, want the expired bytes", err)
	}
	if b, _ := io.ReadAll(result.Stream.Body); string(b) != "expired bytes" {
		t.Fatalf("first read: %q", b)
	}
	_ = result.Stream.Body.Close()
	if _, err = s.Read(t.Context(), q); !errors.Is(err, refusal) || calls != 1 {
		t.Fatalf("second read: %v after %d backend reads, want the remembered refusal", err, calls)
	}
	s.ForgetRefusals("up", "")
}
