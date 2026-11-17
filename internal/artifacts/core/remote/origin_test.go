package remote_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

func TestUnitOriginTrace(t *testing.T) {
	for _, tc := range []struct {
		name            string
		headers         http.Header
		from, recursive bool
		trace           []string
	}{
		{name: "ordinary"},
		{name: "legacy", headers: http.Header{remote.OriginHeader: {"other"}}, from: true},
		{name: "empty marker", headers: http.Header{remote.OriginatedHeader: {""}}, from: true, trace: []string{""}},
		{name: "multiple lines and commas", headers: http.Header{remote.OriginatedHeader: {"a, b", "c"}}, from: true, trace: []string{"a", "b", "c"}},
		{name: "single own marker", headers: http.Header{remote.OriginatedHeader: {remote.OriginHostID()}}, from: true, trace: []string{remote.OriginHostID()}},
		{name: "recursive", headers: http.Header{remote.OriginatedHeader: {"other," + remote.OriginHostID()}}, from: true, recursive: true, trace: []string{"other", remote.OriginHostID()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header = tc.headers
			remote.OriginTrace(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				v := repo.Config{}
				if got := remote.NoOriginUpstream(r.Context(), v); got != tc.from {
					t.Fatalf("restricted %v", got)
				}
				enabled := true
				v.ArtifactoryRequestsCanRetrieveRemoteArtifacts = &enabled
				if remote.NoOriginUpstream(r.Context(), v) {
					t.Fatal("enabled virtual restricted")
				}
				if got := remote.RecursiveOrigin(r.Context()); got != tc.recursive {
					t.Fatalf("recursive %v", got)
				}
				out := httptest.NewRequest("GET", "/", nil).WithContext(r.Context())
				out.Header.Set(remote.OriginHeader, "configured")
				out.Header.Set(remote.OriginatedHeader, "configured")
				remote.SetOriginHeaders(out)
				if got := out.Header.Values(remote.OriginatedHeader); !slices.Equal(got, append(tc.trace, remote.OriginHostID())) {
					t.Fatalf("trace %v", got)
				}
				if out.Header.Get(remote.OriginHeader) != remote.OriginHostID() {
					t.Fatal(out.Header)
				}
			})).ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

func TestUnitOriginFlightsDoNotJoinReturningHop(t *testing.T) {
	var g remote.FlightGroup[int]
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.Do(t.Context(), "file", func(context.Context) (int, error) { close(started); <-release; return 1, nil })
	}()
	<-started
	defer func() { close(release); <-done }()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(remote.OriginatedHeader, remote.OriginHostID())
	remote.OriginTrace(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		got, err := g.Do(ctx, "file", func(context.Context) (int, error) { return 2, nil })
		if err != nil || got != 2 {
			t.Fatalf("returning hop joined original fill: %d %v", got, err)
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
}
