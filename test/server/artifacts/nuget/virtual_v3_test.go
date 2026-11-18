package nuget_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func nugetVirtualWorld(t *testing.T, mutate ...func(*config.Config)) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t, append(nugetBackend(t), mutate...)...)
	w.Repo.PackageType = repo.NuGet
	w.Repo.V3FeedURL = w.Upstream.URL + "/v3/index.json"
	w.Update()
	for _, c := range []repo.Config{{Key: "local", Class: repo.Local, PackageType: repo.NuGet}, {Key: "nested", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"local", "remote"}}, {Key: "virtual", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"nested", "local", "remote"}}} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	w.H = harness.Router(harness.Config{
		Store:         w.Runtime.Store,
		Blobs:         w.Runtime.Blobs,
		Auth:          w.Runtime.Auth,
		Tokens:        w.Runtime.Tokens,
		Keys:          w.Runtime.Keys,
		RemoteSources: w.Runtime.Artifacts.RemoteSources(),
		RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
		VirtualCache:  w.Runtime.Artifacts.VirtualCache(),
		Work:          w.Runtime.Work,
		Logger:        slog.New(slog.DiscardHandler),
	})
	return w
}

func nugetVirtualRemote(t *testing.T, w *harness.RemoteWorld) []byte {
	t.Helper()
	put := func(path string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		w.Upstream.Set(path, fakeupstream.Resource{Body: b, Header: http.Header{"Content-Type": []string{"application/json"}}})
	}
	put(
		"/v3/index.json",
		map[string]any{
			"version": "3.0.0",
			"resources": []map[string]string{
				{"@id": w.Upstream.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"},
				{"@id": w.Upstream.URL + "/reg/", "@type": "RegistrationsBaseUrl/3.6.0"},
				{"@id": w.Upstream.URL + "/reg/", "@type": "RegistrationsBaseUrl/3.4.0"},
				{"@id": w.Upstream.URL + "/query", "@type": "SearchQueryService/3.5.0"},
				{"@id": w.Upstream.URL + "/autocomplete", "@type": "SearchAutocompleteService/3.5.0"},
			},
		},
	)
	body := nugetPackage("Acme.Mixed", "3.0.0", "<description>rich remote</description>")
	target := w.Upstream.URL + "/content/package.nupkg"
	w.Upstream.Set("/content/package.nupkg", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}})
	put("/flat/acme.mixed/index.json", map[string]any{"versions": []string{"3.0.0"}})
	put(
		"/reg/acme.mixed/index.json",
		map[string]any{
			"count": 1,
			"items": []any{
				map[string]any{
					"@id":   w.Upstream.URL + "/reg/acme.mixed/index.json#page/3.0.0",
					"count": 1,
					"lower": "3.0.0",
					"upper": "3.0.0",
					"items": []any{
						map[string]any{
							"@id":            w.Upstream.URL + "/leaf",
							"packageContent": target,
							"catalogEntry": map[string]any{
								"@id":                  w.Upstream.URL + "/catalog",
								"id":                   "Acme.Mixed",
								"version":              "3.0.0",
								"description":          "rich remote",
								"packageContent":       target,
								"customSignatureField": "preserve",
							},
						},
					},
				},
			},
		},
	)
	put(
		"/query",
		map[string]any{
			"totalHits": 500,
			"data":      []any{map[string]any{"id": "Acme.Mixed", "version": "3.0.0", "description": "rich remote", "versions": []any{map[string]string{"version": "3.0.0"}}}},
		},
	)
	put("/autocomplete", map[string]any{"totalHits": 500, "data": []string{"Acme.Mixed"}})
	return body
}

func TestAccNuGetVirtualV3Backends(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var mutate []func(*config.Config)
			if backend == "sqlite" {
				mutate = append(mutate, func(c *config.Config) {
					c.Database = config.Database{Driver: "sqlite", DSN: t.TempDir() + "/nuget.sqlite"}
				})
			}
			if backend == "postgres" {
				dsn := platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
				mutate = append(mutate, func(c *config.Config) { c.Database.Driver = "postgres"; c.Database.DSN = dsn })
			}
			w := nugetVirtualWorld(t, mutate...)
			remote := nugetVirtualRemote(t, w)
			local := nugetPackage("Acme.Mixed", "1.0.0", "<description>local owner</description>")
			harness.WantStatus(t, w.Request("PUT", "/artifactory/local/Acme.Mixed.1.0.0.nupkg", "admin", string(local)), 201)
			root := "/artifactory/api/nuget/v3/virtual/"
			for _, path := range []string{"index.json", "flatcontainer/acme.mixed/index.json", "registration/acme.mixed/index.json", "registration-semver2/acme.mixed/page/1.0.0/3.0.0.json", "registration-semver2/acme.mixed/3.0.0.json", "flatcontainer/acme.mixed/1.0.0/acme.mixed.nuspec", "flatcontainer/acme.mixed/3.0.0/acme.mixed.nuspec", "query", "autocomplete"} {
				r := w.Request("GET", root+path, "admin", "")
				harness.WantStatus(t, r, 200)
				if strings.Contains(r.Body.String(), "/nuget/v3/local/") || strings.Contains(r.Body.String(), "/nuget/v3/remote/") ||
					strings.Contains(r.Body.String(), w.Upstream.URL) {
					t.Fatal(path, r.Body)
				}
				if strings.Contains(path, "page/") && !strings.Contains(r.Body.String(), "customSignatureField") {
					t.Fatal(r.Body)
				}
			}
			for _, v := range []struct {
				version string
				body    []byte
			}{{"1.0.0", local}, {"3.0.0", remote}} {
				r := w.Request("GET", root+"flatcontainer/acme.mixed/"+v.version+"/acme.mixed."+v.version+".nupkg", "admin", "")
				harness.WantStatus(t, r, 200)
				if !bytes.Equal(r.Body.Bytes(), v.body) {
					t.Fatal("wrong owner bytes")
				}
			}
			before := len(w.Upstream.Requests())
			harness.WantStatus(t, w.Request("GET", root+"registration/Download/Acme.Mixed/1.0.0", "admin", ""), 200)
			if len(w.Upstream.Requests()) != before {
				t.Fatal("local download called upstream")
			}
			for _, path := range []string{"flatcontainer/acme.mixed/index.json", "registration-semver2/acme.mixed/3.0.0.json", "flatcontainer/acme.mixed/3.0.0/acme.mixed.3.0.0.nupkg"} {
				harness.WantStatus(t, w.Request("GET", root+path, "denied", ""), 404)
			}
			harness.WantStatus(t, w.Request("GET", root+"registration-semver2/acme.mixed/3.0.0.json", "reader", ""), 200)
			harness.WantStatus(t, w.Request("GET", root+"flatcontainer/acme.mixed/3.0.0/acme.mixed.3.0.0.nupkg", "reader", ""), 200)
			newer := nugetPackage("Acme.Mixed", "4.0.0", "")
			harness.WantStatus(t, w.Request("PUT", "/artifactory/local/Acme.Mixed.4.0.0.nupkg", "admin", string(newer)), 201)
			r := w.Request("GET", root+"query", "admin", "")
			harness.WantStatus(t, r, 200)
			if !strings.Contains(r.Body.String(), `"version":"4.0.0"`) {
				t.Fatal("next read did not see deploy", r.Body)
			}
			w.Repo.PriorityResolution = repo.Flex(true)
			w.Update()
			r = w.Request("GET", root+"flatcontainer/acme.mixed/index.json", "admin", "")
			harness.WantStatus(t, r, 200)
			if !strings.Contains(r.Body.String(), `["3.0.0"]`) {
				t.Fatal("priority id did not shadow", r.Body)
			}
			for _, path := range []string{"a.pdb/invalid/a.pdb", "symbols"} {
				method, want := "GET", 404
				if path == "symbols" {
					method, want = "PUT", 405
				}
				r = w.Request(method, root+path, "admin", "malformed")
				harness.WantStatus(t, r, want)
			}
		})
	}
}
