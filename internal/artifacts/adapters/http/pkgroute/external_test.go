package pkgroute_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitExternalURL(t *testing.T) {
	c := repo.Config{Class: repo.Virtual, ExternalDependenciesEnabled: new(true), ExternalDependenciesPatterns: []string{"https://allowed.example/**"}}
	for _, raw := range []string{"https://allowed.example/file.tgz", "https://allowed.example/file.tgz?download=1"} {
		if _, err := pkgroute.ExternalURL(c, raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"https://foreign.example/a", "http://allowed.example/a", "https://u:p@allowed.example/a", "https://allowed.example/../a", "https://allowed.example/%2e%2e/a", "https://allowed.example/a#ref", "ssh://allowed.example/a", "https://allowed.example/a\\b", "https://allowed.example/./a", strings.Repeat("x", 4097)} {
		t.Run(raw[:min(len(raw), 90)], func(t *testing.T) {
			if _, err := pkgroute.ExternalURL(c, raw); err == nil {
				t.Fatal(raw)
			}
		})
	}
	c.ExternalDependenciesPatterns = nil
	if _, err := pkgroute.ExternalURL(c, "https://allowed.example/a"); err == nil {
		t.Fatal("empty allowlist")
	}
}

type externalSource struct{ body io.ReadCloser }

func (s externalSource) Item(context.Context, pkgroute.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s externalSource) Read(*http.Request, repo.Config, source.Resource) (source.Result, error) {
	return source.Result{Stream: &source.Stream{Body: s.body, Status: 200}}, nil
}

func TestUnitReadExternalBoundsAndServedFault(t *testing.T) {
	for _, tc := range []string{"bounded", "oversize", "readfault", "validation", "late"} {
		t.Run(tc, func(t *testing.T) {
			body := &remoteBody{Reader: strings.NewReader("123")}
			if tc == "oversize" {
				body.Reader = strings.NewReader("1234")
			}
			if tc == "readfault" {
				body.Reader = brokenReader{}
			}
			f := newKitFixture(t, map[repo.Class]pkgroute.Source{repo.Local: pkgroute.Local{}, repo.Remote: externalSource{body: body}})
			if err := f.db.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "external-reader", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}}, Targets: map[string]accesstest.Target{"gen-remote": {}}}}}); err != nil {
				t.Fatal(err)
			}
			proto := testFormat(map[repo.Class]pkgroute.Source{repo.Local: pkgroute.Local{}, repo.Remote: externalSource{body: body}})
			proto.Routes = func(router chi.Router, k *pkgroute.Kit) {
				router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
					bad := apierror.New(502, "invalid archive")
					_, err := k.ReadExternal(r, r, source.Resource{Kind: source.ResourceKindArtifact, Path: "file", Method: "GET"}, false, 3, bad, func([]byte) error {
						if tc == "validation" {
							return bad
						}
						return nil
					}, func(context.Context) error {
						if tc == "late" {
							return source.ErrSuperseded
						}
						return nil
					})
					if err != nil {
						k.Error(w, r, pkgroute.SourceError(err, false))
						return
					}
					w.WriteHeader(200)
				})
			}
			f.h = f.router(t, f.o, proto)
			got := f.do("GET", base+"gen-remote/file", "reader", "")
			want := 200
			switch tc {
			case "oversize", "validation":
				want = 502
			case "readfault":
				want = 500
			case "late":
				want = 503
			}
			if got.Code != want || body.closes.Load() != 1 {
				t.Fatalf("%s: %d %s closes%d want%d", tc, got.Code, got.Body, body.closes.Load(), want)
			}
		})
	}
}

func TestUnitExternalRedirect(t *testing.T) {
	c := repo.Config{Class: repo.Virtual, ExternalDependenciesEnabled: new(true), ExternalDependenciesPatterns: []string{"**"}}
	start, _ := pkgroute.ExternalURL(c, "https://allowed.example/a")
	denied := errors.New("changed authority")
	check := pkgroute.ExternalRedirect(c, start, func(context.Context) error { return denied })
	if err := check(t.Context(), start); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	check = pkgroute.ExternalRedirect(c, start, func(context.Context) error { return nil })
	next, _ := pkgroute.ExternalURL(c, "http://allowed.example/a")
	if err := check(t.Context(), next); !errors.Is(err, source.ErrDenied) {
		t.Fatal(err)
	}
}
