package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

func TestAccOutboundHooksOnRequestsAndRedirects(t *testing.T) {
	// /first redirects on its own origin to /same, which redirects to /item
	// on another origin.
	chain := func(t *testing.T) (*fakeupstream.Server, *fakeupstream.Server, *Client) {
		t.Helper()
		other := fakeupstream.New(t, fakeupstream.Options{})
		other.Set("/item", fakeupstream.Resource{Body: []byte("ok")})
		s := fakeupstream.New(t, fakeupstream.Options{})
		s.Set("/first", fakeupstream.Resource{Redirect: s.URL + "/same"})
		s.Set("/same", fakeupstream.Resource{Redirect: other.URL + "/item"})
		return s, other, clientFor(t, repo.Config{URL: s.URL}, Options{})
	}

	t.Run(
		"header on the first request and same-origin redirects, never cross-origin",
		func(t *testing.T) {
			s, other, c := chain(t)
			var seen []string
			// The value is the request's own path: a redirect also carries a copy
			// of the first request's headers, so only the hook writes "/same".
			ctx := WithOutbound(t.Context(), Outbound{Header: func(req *http.Request) {
				seen = append(seen, req.URL.String())
				req.Header.Set("X-Outbound", req.URL.Path)
			}})
			res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "first"})
			if err != nil {
				t.Fatal(err)
			}
			if got := body(t, res); got != "ok" {
				t.Fatal(got)
			}
			if want := []string{s.URL + "/first", s.URL + "/same"}; !slices.Equal(seen, want) {
				t.Fatal(seen)
			}
			hops := s.Requests()
			if len(hops) != 2 || !hops[0].HeaderMatches("X-Outbound", "/first") ||
				!hops[1].HeaderMatches("X-Outbound", "/same") {
				t.Fatal(hops)
			}
			if hops = other.Requests(); len(hops) != 1 || hops[0].Header.Get("X-Outbound") != "" {
				t.Fatal(hops)
			}
		},
	)

	t.Run("allow refuses the first request and a redirect target", func(t *testing.T) {
		s, other, c := chain(t)
		var targets []string
		ctx := WithOutbound(t.Context(), Outbound{Allow: func(target string) bool {
			targets = append(targets, target)
			return target != s.URL+"/refused" && target != other.URL+"/item"
		}})
		_, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "refused"})
		if !errors.Is(err, ErrSecurity) || s.Count("", "") != 0 {
			t.Fatal(err, s.Requests())
		}
		_, err = c.Do(ctx, Request{Method: http.MethodGet, Path: "first"})
		if !errors.Is(err, ErrSecurity) || s.Count("GET", "/same") != 1 ||
			other.Count("", "") != 0 {
			t.Fatal(err, s.Requests(), other.Requests())
		}
		want := []string{s.URL + "/refused", s.URL + "/first", s.URL + "/same", other.URL + "/item"}
		if !slices.Equal(targets, want) {
			t.Fatal(targets)
		}
	})

	t.Run("a zero Outbound changes nothing", func(t *testing.T) {
		if out := OutboundFrom(t.Context()); out.Header != nil || out.Allow != nil {
			t.Fatal("hooks in a context without WithOutbound")
		}
		// Without hooks and with zero hooks alike, the caller's header reaches
		// both hops on the first origin and is dropped at the other.
		want := []string{"GET /first true", "GET /same true", "GET /item false"}
		for _, ctx := range []context.Context{t.Context(), WithOutbound(t.Context(), Outbound{})} {
			s, other, c := chain(t)
			res, err := c.Do(
				ctx,
				Request{
					Method: http.MethodGet,
					Path:   "first",
					Header: http.Header{"X-Caller": {"v"}},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := body(t, res); got != "ok" {
				t.Fatal(got)
			}
			var got []string
			for _, r := range append(s.Requests(), other.Requests()...) {
				got = append(
					got,
					fmt.Sprintf("%s %s %t", r.Method, r.Path, r.HeaderMatches("X-Caller", "v")),
				)
			}
			if !slices.Equal(got, want) {
				t.Fatal(got)
			}
		}
	})
}
