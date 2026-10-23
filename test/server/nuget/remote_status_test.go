package nuget_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Adapter activation is explicit. The general status fixture still verifies a
// server without RemoteSources; this table exercises the configured v3 surface.
func TestStatusNuGetV3Remote(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	up.Set(
		"/v3/index.json",
		fakeupstream.Resource{
			Body: []byte(
				`{"version":"3.0.0","resources":[{"@id":"` + up.URL + `/flat/","@type":"PackageBaseAddress/3.0.0"}]}`,
			),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		},
	)
	up.Set(
		"/flat/acme/index.json",
		fakeupstream.Resource{
			Body:   []byte(`{"versions":["1.0.0"]}`),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		},
	)
	h, _, _ := nugetServer(t, func(cfg *server.Config) {
		c := repo.Config{
			Key: "nuget-remote", Class: repo.Remote, PackageType: repo.NuGet,
			URL: up.URL, V3FeedURL: up.URL + "/v3/index.json",
		}
		if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		resolver, err := remote.NewResolver(
			cfg.Store,
			nil,
			remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store:   cfg.Store,
			Blobs:   cfg.Blobs,
			Items:   &item.Service{Store: cfg.Store, Blobs: cfg.Blobs},
			Pool:    new(remote.Pool),
			Options: resolver.Options,
			Key:     []byte(strings.Repeat("k", 32)),
		}
		t.Cleanup(policy.Pool.Close)
		cfg.RemoteSources = &source.Service{
			HTTP:  policy,
			Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return cfg.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
	for _, tc := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", "index.json", "admin", 200},
		{"HEAD", "index.json", "admin", 200},
		{"GET", "index.json", "user", 403},
		{"GET", "index.json", "", 401},
		{"GET", "flatcontainer/acme/index.json", "admin", 200},
		{"GET", "flatcontainer/missing/index.json", "admin", 404},
		{"GET", "registration-semver2/acme/index.json", "admin", 404},
		{"GET", "query", "admin", 404},
		{"GET", "autocomplete", "admin", 404},
		{"GET", "repository-signatures/5.0.0.json", "admin", 404},
		{"GET", "repository-certificates/5.0.0/fingerprint.der", "admin", 404},
		{"PUT", "symbols", "admin", 405},
		{"POST", "query", "admin", 405},
	} {
		t.Run(tc.method+"/"+tc.path+"/"+tc.user, func(t *testing.T) {
			var headers map[string]string
			if tc.user != "" {
				headers = basic(tc.user, "pw")
			}
			rec := do(t, h, tc.method, nugetV3+"nuget-remote/"+tc.path, headers, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.status, rec.Body)
			}
		})
	}
}
