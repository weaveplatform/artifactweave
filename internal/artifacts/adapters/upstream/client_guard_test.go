package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

func TestAccRequestDestinationGuardIsolatedAcrossConcurrentFetches(t *testing.T) {
	upstream := fakeupstream.New(t, fakeupstream.Options{})
	for _, name := range []string{"a", "b"} {
		upstream.Set("/"+name+"/start", fakeupstream.Resource{Redirect: "/" + name + "/next"})
		upstream.Set("/"+name+"/next", fakeupstream.Resource{Body: []byte(name)})
	}
	client := clientFor(t, repo.Config{URL: upstream.URL}, remote.Options{})
	for i := 0; i < 20; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			name := []string{"a", "b"}[i%2]
			guard := func(_ context.Context, u *url.URL) error {
				if !strings.HasPrefix(u.Path, "/"+name+"/") {
					return remote.ErrSecurity
				}
				return nil
			}
			response, err := client.Fetch(
				t.Context(),
				remote.Request{Method: http.MethodGet, Path: name + "/start", CheckURL: guard},
			)
			if err != nil {
				t.Fatal(err)
			}
			if body(t, response) != name {
				t.Fatal("request guard contaminated shared client")
			}
		})
	}
}

func TestAccRequestDestinationGuardRejectsInitialAndPreservesRedirectPolicy(t *testing.T) {
	upstream := fakeupstream.New(t, fakeupstream.Options{})
	upstream.Set("/start", fakeupstream.Resource{Redirect: "/next"})
	upstream.Set("/next", fakeupstream.Resource{Body: []byte("bytes")})
	client := clientFor(
		t,
		repo.Config{URL: upstream.URL},
		remote.Options{
			CheckRedirect: func(*http.Request, []*http.Request) error { return remote.ErrRedirect },
		},
	)
	_, err := client.Fetch(
		t.Context(),
		remote.Request{
			Method:   http.MethodGet,
			Path:     "start",
			CheckURL: func(context.Context, *url.URL) error { return remote.ErrSecurity },
		},
	)
	if !errors.Is(err, remote.ErrSecurity) || upstream.Count("", "") != 0 {
		t.Fatal("initial destination guard bypassed", err)
	}
	_, err = client.Fetch(
		t.Context(),
		remote.Request{
			Method:   http.MethodGet,
			Path:     "start",
			CheckURL: func(context.Context, *url.URL) error { return nil },
		},
	)
	if !errors.Is(err, remote.ErrRedirect) || upstream.Count("", "/next") != 0 {
		t.Fatal("existing redirect policy bypassed", err)
	}
}
