package nuget_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteV3 = "/artifactory/api/nuget/v3/nuget-remote/"

type remoteFixture struct {
	*fixture
	up    *fakeupstream.Server
	svc   *source.Service
	clock *fakeupstream.Clock
	opts  pkgroute.Options
}

func newRemoteFixture(t *testing.T, options ...fakeupstream.Options) *remoteFixture {
	t.Helper()
	f := newFixture(t)
	o := fakeupstream.Options{}
	if len(options) > 0 {
		o = options[0]
	}
	up := fakeupstream.New(t, o)
	c, err := f.db.Repos().Get(t.Context(), "nuget-remote")
	if err != nil {
		t.Fatal(err)
	}
	c.URL = up.URL
	c.V3FeedURL = up.URL + "/v3/index.json"
	ttl := 5
	c.RetrievalCachePeriodSecs = &ttl
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for user, pattern := range map[string]string{"reader": "**", "partial": "acme.mixed/1.*/*"} {
		err = f.db.Permissions().
			Create(t.Context(), types.Permission{Name: "remote-" + user, Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{user: types.ActionRead}}, Targets: map[string]types.Target{"nuget-remote": {Include: []string{pattern}}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	opts := remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))}
	if cert := up.Certificate(); cert != nil {
		opts.RootCAsPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	}
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		opts,
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := fakeupstream.NewClock(time.Now())
	f.items.Now = clock.Now
	policy := &remote.Policy{
		Store:   f.db,
		Blobs:   f.blobs,
		Items:   f.items,
		Pool:    new(remote.Pool),
		Options: resolver.Options,
		Key:     []byte(strings.Repeat("k", 32)),
		Now:     clock.Now,
	}
	t.Cleanup(policy.Pool.Close)
	svc := &source.Service{
		HTTP:  policy,
		Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	out := &remoteFixture{fixture: f, up: up, svc: svc, clock: clock}
	out.opts = pkgroute.Options{
		Store:     f.db,
		Items:     func() *item.Service { return f.items },
		ItemError: itemError,
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, err := f.blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			defer rc.Close()
			_, _ = io.Copy(w, rc)
		},
	}
	out.mount(t, base)
	out.setJSON(
		"/v3/index.json",
		map[string]any{"version": "3.0.0", "resources": []map[string]string{
			{"@id": up.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"},
			{"@id": up.URL + "/reg/", "@type": "RegistrationsBaseUrl/3.6.0"},
			{"@id": up.URL + "/reg/", "@type": "RegistrationsBaseUrl/3.4.0"},
			{"@id": up.URL + "/query", "@type": "SearchQueryService/3.5.0"},
			{"@id": up.URL + "/autocomplete", "@type": "SearchAutocompleteService/3.5.0"},
		}},
	)
	return out
}

func (f *remoteFixture) mount(t *testing.T, public string) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(public))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Test-User")
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == "" || name == "anonymous" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      f.db.Permissions(),
					Groups:           f.db.Groups(),
					AnonymousEnabled: true,
				},
				p,
			)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(r, f.opts, nuget.V3(f.svc))
	pkgroute.Mount(r, f.opts, nuget.V2(f.svc))
	f.h = r
}

func (f *remoteFixture) setJSON(path string, v any) {
	if d, ok := v.(map[string]any); ok {
		if items, ok := d["items"]; ok {
			if _, has := d["count"]; !has {
				raw, _ := json.Marshal(items)
				var rows []any
				_ = json.Unmarshal(raw, &rows)
				d["count"] = len(rows)
			}
		}
	}
	b, _ := json.Marshal(v)
	f.up.Set(
		path,
		fakeupstream.Resource{
			Body:   b,
			Header: http.Header{"Content-Type": []string{"application/json"}},
		},
	)
}

func (f *remoteFixture) packageDocs(id string, versions ...string) []map[string]any {
	name := strings.ToLower(id)
	entries := make([]map[string]any, 0)
	for _, v := range versions {
		body := nupkg(
			id,
			v,
			`<dependencies><dependency id="Acme.Dependency" version="[1.0.0]" /></dependencies>`,
		)
		sum := sha512.Sum512(body)
		target := "/objects/" + name + "-" + v + ".zip"
		f.up.Set(
			target,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
			},
		)
		entries = append(
			entries,
			map[string]any{
				"@id":            f.up.URL + "/opaque/leaves/" + name + v,
				"packageContent": f.up.URL + target,
				"catalogEntry": map[string]any{
					"@id":                  f.up.URL + "/catalog/" + name + v,
					"id":                   id,
					"version":              v,
					"listed":               true,
					"packageHashAlgorithm": "SHA512",
					"packageHash":          base64.StdEncoding.EncodeToString(sum[:]),
					"dependencyGroups": []any{
						map[string]any{
							"dependencies": []any{
								map[string]string{"id": "Acme.Dependency", "range": "[1.0.0]"},
							},
						},
					},
				},
			},
		)
	}
	f.setJSON("/flat/"+name+"/index.json", map[string]any{"versions": versions})
	f.setJSON(
		"/reg/"+name+"/index.json",
		map[string]any{
			"count": 1,
			"items": []any{
				map[string]any{
					"@id":   f.up.URL + "/opaque/page/" + name,
					"count": len(entries),
					"lower": versions[0],
					"upper": versions[len(versions)-1],
				},
			},
		},
	)
	f.setJSON(
		"/opaque/page/"+name,
		map[string]any{
			"@id":   f.up.URL + "/opaque/page/" + name,
			"count": len(entries),
			"items": entries,
		},
	)
	return entries
}

func TestAccRemoteV3DiscoveryPagingPermissionsAndIdentity(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0")
	for _, path := range []string{"index.json", "flatcontainer/Acme.Mixed/index.json", "registration-semver2/ACME.MIXED/index.json", "registration-semver2/acme.mixed/page/1.0.0/2.0.0.json", "registration-semver2/acme.mixed/1.0.0.json"} {
		rec := f.do("GET", remoteV3+path, "partial")
		wantStatus(t, rec, 200)
		if strings.Contains(rec.Body.String(), f.up.URL) {
			t.Fatalf("upstream URL escaped: %s", rec.Body)
		}
		if strings.Contains(rec.Body.String(), `"version":"2.0.0"`) ||
			strings.Contains(rec.Body.String(), `"versions":["1.0.0","2.0.0"]`) {
			t.Fatalf("forbidden version leaked: %s", rec.Body)
		}
	}
	full := f.do("GET", remoteV3+"registration-semver2/acme.mixed/index.json", "reader")
	wantStatus(t, full, 200)
	if !strings.Contains(full.Body.String(), `"version":"2.0.0"`) {
		t.Fatal(full.Body)
	}
	rec := f.do("GET", remoteV3+"flatcontainer/ACME.MIXED/1.0/ACME.MIXED.1.0.nupkg", "partial")
	wantStatus(t, rec, 200)
	if !bytes.Equal(
		rec.Body.Bytes(),
		nupkg(
			"Acme.Mixed",
			"1.0.0",
			`<dependencies><dependency id="Acme.Dependency" version="[1.0.0]" /></dependencies>`,
		),
	) {
		t.Fatal("package bytes changed")
	}
	wantStatus(
		t,
		f.do("GET", remoteV3+"flatcontainer/acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg", "partial"),
		403,
	)
	wantStatus(
		t,
		f.do("GET", remoteV3+"flatcontainer/acme.mixed/1.0.0/acme.mixed.nuspec", "partial"),
		200,
	)
	if f.up.Count("GET", "/objects/acme.mixed-1.0.0.zip") != 1 {
		t.Fatal("canonical identity fetched twice")
	}
	f.mount(t, "https://public.example/proxy")
	out := f.do("GET", remoteV3+"index.json", "reader")
	wantStatus(t, out, 200)
	if !strings.Contains(out.Body.String(), "https://public.example/proxy/") {
		t.Fatal(out.Body)
	}
	for _, method := range []string{"PUT", "POST", "DELETE"} {
		wantStatus(t, f.do(method, remoteV3+"symbols", "reader"), 405)
	}
}

func TestAccRemoteV3GzipAndOffline(t *testing.T) {
	f := newRemoteFixture(t)
	entries := f.packageDocs("Acme.Mixed", "1.0.0")
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_ = json.NewEncoder(gz).Encode(map[string]any{"count": len(entries), "items": entries})
	_ = gz.Close()
	f.up.Set(
		"/opaque/page/acme.mixed",
		fakeupstream.Resource{
			Body: compressed.Bytes(),
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"gzip"},
			},
		},
	)
	wantStatus(
		t,
		f.do("GET", remoteV3+"flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", "reader"),
		200,
	)
	f.up.Close()
	f.clock.Advance(10 * time.Second)
	wantStatus(
		t,
		f.do("GET", remoteV3+"flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", "reader"),
		200,
	)
}
