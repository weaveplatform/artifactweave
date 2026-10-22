package helm_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/helm"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

const remoteEndpoint = "/artifactory/api/helm/remote/"

type remoteFixture struct {
	*fixture
	up    *fakeupstream.Server
	clock *fakeupstream.Clock
	svc   *source.Service
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newFixture(t, false)
	up := fakeupstream.New(t, fakeupstream.Options{})
	c, err := f.items.Store.Repos().Get(t.Context(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	c.URL = up.URL + "/charts/"
	ttl := 5
	c.RetrievalCachePeriodSecs = &ttl
	if err = f.items.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for user, pattern := range map[string]string{"reader": "**", "partial": "visible/**"} {
		err = f.items.Store.Permissions().
			Create(t.Context(), types.Permission{Name: "remote-" + user, Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{user: types.ActionRead}}, Targets: map[string]types.Target{"remote": {Include: []string{pattern}}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := remote.NewResolver(
		f.items.Store,
		nil,
		remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := fakeupstream.NewClock(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC))
	policy := &remote.Policy{
		Store:   f.items.Store,
		Blobs:   f.items.Blobs,
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
			return f.items.Store.RemoteCache().Source(ctx, c.Key)
		},
	}
	out := &remoteFixture{fixture: f, up: up, clock: clock, svc: svc}
	out.mount(t, "https://proxy.example", false)
	return out
}

func (f *remoteFixture) mount(t *testing.T, public string, relative bool) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(public), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == "anonymous" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      f.items.Store.Permissions(),
					Groups:           f.items.Store.Groups(),
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
	pkgroute.Mount(
		r,
		pkgroute.Options{
			Store: f.items.Store,
			Items: func() *item.Service { return f.items },
			ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
				body, _, err := f.items.Blobs.Open(r.Context(), it.Actual.SHA256)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				defer body.Close()
				if r.Method != http.MethodHead {
					_, _ = io.Copy(w, body)
				}
			},
		},
		helm.Protocol(
			helm.Options{Format: helmfmt.Settings{RelativeURLs: &relative}, RemoteSources: f.svc},
		),
	)
	f.h = r
}

func (f *remoteFixture) get(path, user string, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, remoteEndpoint+path, nil)
	r.Header.Set("X-User", user)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *remoteFixture) configure(change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.items.Store.Repos().Get(f.t.Context(), "remote")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err = f.items.Store.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *remoteFixture) index(entries ...helmfmt.ChartVersion) []byte {
	f.t.Helper()
	i := helmfmt.Index{APIVersion: "v1", Entries: map[string][]helmfmt.ChartVersion{}}
	for _, e := range entries {
		i.Entries[e.Name] = append(i.Entries[e.Name], e)
	}
	data, err := helmfmt.EncodeIndex(i)
	if err != nil {
		f.t.Fatal(err)
	}
	f.up.Set(
		"/charts/index.yaml",
		fakeupstream.Resource{
			Body: data,
			Header: http.Header{
				"Content-Type": []string{"application/x-yaml"},
				"ETag":         []string{fmt.Sprintf(`"%x"`, sha256.Sum256(data))},
			},
		},
	)
	return data
}

func (f *remoteFixture) chart(name, version, path string) (helmfmt.ChartVersion, []byte) {
	data := archive(name, version)
	f.up.Set(
		"/charts/"+path,
		fakeupstream.Resource{
			Body:   data,
			Header: http.Header{"Content-Type": []string{"application/gzip"}},
		},
	)
	return helmfmt.ChartVersion{
		Chart:  helmfmt.Chart{Name: name, Version: version, APIVersion: "v2"},
		URLs:   []string{path},
		Digest: fmt.Sprintf("%x", sha256.Sum256(data)),
	}, data
}

func expectRemote(t *testing.T, rec *httptest.ResponseRecorder, status int, contains ...string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	for _, want := range contains {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %q: %s", want, rec.Body)
		}
	}
}

func TestRemoteIndexPreservesVersionsAndFiltersLocations(t *testing.T) {
	f := newRemoteFixture(t)
	a, _ := f.chart("web", "1.0.0", "visible/web-1.0.0.tgz")
	b, _ := f.chart("web", "2.0.0", "hidden/web-2.0.0.tgz")
	a.Annotations = map[string]string{"retained": "upstream-value"}
	f.index(a, b)
	full := f.get("index.yaml", "reader")
	expectRemote(
		t,
		full,
		200,
		"retained: upstream-value",
		"https://proxy.example"+remoteEndpoint+"hidden/web-2.0.0.tgz",
	)
	var index helmfmt.Index
	if err := yaml.Unmarshal(full.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Entries["web"]) != 2 || index.Entries["web"][0].Version != "2.0.0" {
		t.Fatal(index)
	}
	partial := f.get("index.yaml", "partial")
	expectRemote(t, partial, 200, "visible/web-1.0.0.tgz")
	if strings.Contains(partial.Body.String(), "2.0.0") {
		t.Fatal("forbidden chart leaked")
	}
	expectRemote(t, f.get("hidden/web-2.0.0.tgz", "partial"), 403)
	expectRemote(t, f.get("index.yaml", "nobody"), 403)
	expectRemote(t, f.get("index.yaml", "anonymous"), 401)
	if f.up.Count("GET", "/charts/index.yaml") != 1 {
		t.Fatal("raw index should be shared")
	}
	f.mount(t, "https://other.example", false)
	other := f.get("index.yaml", "reader")
	expectRemote(t, other, 200, "https://other.example"+remoteEndpoint)
	if other.Header().Get("ETag") == full.Header().Get("ETag") {
		t.Fatal("rewritten ETags aliased")
	}
	expectRemote(t, f.get("index.yaml", "reader", "If-None-Match", other.Header().Get("ETag")), 304)
	f.mount(t, "https://proxy.example", true)
	expectRemote(t, f.get("index.yaml", "reader"), 200, "- hidden/web-2.0.0.tgz")
}

func TestRemoteChartColdWarmOfflineAndIndexRefresh(t *testing.T) {
	f := newRemoteFixture(t)
	a, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
	f.index(a)
	cold := f.get("web-1.0.0.tgz", "reader")
	expectRemote(t, cold, 200)
	if !bytes.Equal(cold.Body.Bytes(), data) {
		t.Fatal("archive changed")
	}
	expectRemote(t, f.get("web-1.0.0.tgz", "reader"), 200)
	if f.up.Count("GET", "/charts/web-1.0.0.tgz") != 1 {
		t.Fatal("immutable chart refetched")
	}
	b, _ := f.chart("web", "2.0.0", "web-2.0.0.tgz")
	f.index(a, b)
	f.clock.Advance(6 * time.Second)
	expectRemote(t, f.get("index.yaml", "reader"), 200, "2.0.0")
	if f.up.Count("GET", "/charts/web-2.0.0.tgz") != 0 {
		t.Fatal("index eagerly fetched archives")
	}
	c, _ := f.items.Store.Repos().Get(t.Context(), "remote")
	if err := (helmfmt.Handler{}).Reindex(t.Context(), f.items.Env(), c); err != nil {
		t.Fatal(err)
	}
	f.up.Close()
	f.configure(func(c *repo.Config) { v := true; c.Offline = &v })
	expectRemote(t, f.get("index.yaml", "reader"), 200, "2.0.0")
	warm := f.get("web-1.0.0.tgz", "reader")
	expectRemote(t, warm, 200)
	if !bytes.Equal(warm.Body.Bytes(), data) {
		t.Fatal("offline archive changed")
	}
	expectRemote(t, f.get("web-2.0.0.tgz", "reader"), 404)
}

func TestRemoteRejectsPoisonedIndexAndChartBeforeCache(t *testing.T) {
	for _, bad := range []string{"not: [yaml", "apiVersion: v2\nentries: {}", "apiVersion: v1\nentries: {}\n---\nother: true", "<html>login</html>"} {
		t.Run(bad, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.up.Set(
				"/charts/index.yaml",
				fakeupstream.Resource{
					Body:   []byte(bad),
					Header: http.Header{"Content-Type": []string{"application/x-yaml"}},
				},
			)
			expectRemote(t, f.get("index.yaml", "reader"), 502)
			a, _ := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			f.index(a)
			expectRemote(t, f.get("index.yaml", "reader"), 200, "1.0.0")
		})
	}
	for _, fault := range []string{"digest", "identity", "truncated"} {
		t.Run(fault, func(t *testing.T) {
			f := newRemoteFixture(t)
			a, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			bad := []byte("not a chart")
			if fault == "identity" {
				bad = archive("wrong", "1.0.0")
				a.Digest = fmt.Sprintf("%x", sha256.Sum256(bad))
			}
			if fault == "truncated" {
				bad = data[:len(data)-5]
				a.Digest = fmt.Sprintf("%x", sha256.Sum256(bad))
			}
			f.index(a)
			f.up.Set("/charts/web-1.0.0.tgz", fakeupstream.Resource{Body: bad})
			expectRemote(t, f.get("web-1.0.0.tgz", "reader"), 502)
			if _, err := f.items.Store.Items().
				Get(t.Context(), "remote", true, "web-1.0.0.tgz"); err == nil {
				t.Fatal("invalid chart published")
			}
		})
	}
}

func TestRemoteChartURLsStayBehindRepository(t *testing.T) {
	for _, mode := range []string{"absolute", "charts-base", "external"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			a, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			var external *fakeupstream.Server
			switch mode {
			case "absolute":
				a.URLs = []string{f.up.URL + "/charts/web-1.0.0.tgz"}
			case "charts-base", "external":
				external = fakeupstream.New(t, fakeupstream.Options{})
				external.Set("/releases/web-1.0.0.tgz", fakeupstream.Resource{Body: data})
				if mode == "charts-base" {
					f.configure(
						func(c *repo.Config) { c.ChartsBaseURL = external.URL + "/releases/" },
					)
				} else {
					a.URLs = []string{external.URL + "/releases/web-1.0.0.tgz?download=true"}
					f.index(a)
					expectRemote(t, f.get("index.yaml", "reader"), 502)
					if external.Count("", "") != 0 {
						t.Fatal("disabled external URL fetched")
					}
					f.configure(func(c *repo.Config) {
						v := true
						c.ExternalDependenciesEnabled = &v
						c.ExternalDependenciesPatterns = []string{external.URL + "/releases/**"}
					})
					f.allowPrivate(t, external.URL)
				}
			}
			f.index(a)
			index := f.get("index.yaml", "reader")
			expectRemote(t, index, 200)
			var parsed helmfmt.Index
			if err := yaml.Unmarshal(index.Body.Bytes(), &parsed); err != nil {
				t.Fatal(err)
			}
			location := parsed.Entries["web"][0].URLs[0]
			if !strings.HasPrefix(location, "https://proxy.example"+remoteEndpoint) {
				t.Fatal("client bypasses repository", location)
			}
			path := strings.TrimPrefix(location, "https://proxy.example"+remoteEndpoint)
			got := f.get(path, "reader")
			expectRemote(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), data) {
				t.Fatal("chart bytes changed")
			}
			if mode == "external" {
				if strings.Contains(location, "download=") {
					t.Fatal("upstream query leaked into client metadata")
				}
				expectRemote(t, f.get(path+"?download=other", "reader"), 404)
				expectRemote(t, f.get("_external/http/other.invalid/arbitrary.tgz", "reader"), 404)
			}
		})
	}
}

func TestRemoteURLRefusals(t *testing.T) {
	for _, raw := range []string{"../web.tgz", "%2e%2e/web.tgz", "https://user:pass@example.invalid/web.tgz", "file:///tmp/web.tgz", "https://example.invalid/web.tgz#fragment", "https://example.invalid/%2e%2e/web.tgz", "web.tgz\\evil", "https://example.invalid/other.zip", "https://example.invalid/web.tgz"} {
		t.Run(raw, func(t *testing.T) {
			f := newRemoteFixture(t)
			a, _ := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			a.URLs = []string{raw}
			f.index(a)
			expectRemote(t, f.get("index.yaml", "reader"), 502)
		})
	}
}

func TestRemoteNoStoreAndZap(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.configure(func(c *repo.Config) { c.StoreArtifactsLocally = &store })
			a, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			f.index(a)
			expectRemote(t, f.get("web-1.0.0.tgz", "reader"), 200)
			if store {
				b, _ := f.chart("web", "2.0.0", "web-2.0.0.tgz")
				f.index(a, b)
				c, _ := f.items.Store.Repos().Get(t.Context(), "remote")
				if _, err := f.items.Zap(
					t.Context(),
					item.Target{Repo: c, Cached: true},
				); err != nil {
					t.Fatal(err)
				}
				expectRemote(t, f.get("index.yaml", "reader"), 200, "2.0.0")
			} else {
				if _, err := f.items.Store.Items().
					Get(t.Context(), "remote", true, "web-1.0.0.tgz"); err == nil {
					t.Fatal("no-store chart persisted")
				}
				expectRemote(t, f.get("web-1.0.0.tgz", "reader"), 200)
				if f.up.Count("GET", "/charts/web-1.0.0.tgz") != 2 {
					t.Fatal("no-store chart reused")
				}
			}
			if got := f.get("web-1.0.0.tgz", "reader"); !bytes.Equal(got.Body.Bytes(), data) {
				t.Fatal("chart changed", got.Code)
			}
			for _, method := range []string{"PUT", "POST", "DELETE"} {
				expectRemote(t, f.do(method, remoteEndpoint+"web-1.0.0.tgz", "admin"), 405)
			}
			expectRemote(t, f.get("README.txt", "reader"), 404)
		})
	}
}

func TestRemoteOriginCredentialsAndProxy(t *testing.T) {
	f := newRemoteFixture(t)
	external := fakeupstream.New(t, fakeupstream.Options{})
	a, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
	external.Set("/web-1.0.0.tgz", fakeupstream.Resource{Body: data})
	a.URLs = []string{external.URL + "/web-1.0.0.tgz"}
	f.configure(func(c *repo.Config) {
		v := true
		c.ExternalDependenciesEnabled = &v
		c.ExternalDependenciesPatterns = []string{external.URL + "/**"}
		c.Username = "upstream-user"
		c.Password = "upstream-secret"
	})
	proxy := fakeupstream.NewProxy(t, fakeupstream.Auth{}, f.up, external)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(proxyURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(
		wire.ProxyConfiguration{Host: proxyURL.Scheme + "://" + proxyURL.Hostname(), Port: port},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.items.Store.Configs().
		Put(t.Context(), types.ConfigDocument{Kind: types.ConfigProxy, Key: "helm-proxy", Enabled: true, Document: doc}); err != nil {
		t.Fatal(err)
	}
	f.configure(func(c *repo.Config) { c.Proxy = "helm-proxy" })
	f.allowPrivate(t, external.URL)
	f.index(a)
	u, _ := url.Parse(external.URL)
	expectRemote(
		t,
		f.get(
			"_external/http/"+u.Host+"/web-1.0.0.tgz",
			"reader",
			"Authorization",
			"Bearer incoming-secret",
		),
		200,
	)
	if proxy.Count("GET", "/charts/index.yaml") != 1 || proxy.Count("GET", "/web-1.0.0.tgz") != 1 {
		t.Fatal("configured proxy bypassed", proxy.Requests())
	}
	for _, request := range external.Requests() {
		if request.Header.Get("Authorization") != "" {
			t.Fatal("origin credentials leaked")
		}
	}
}

func (f *remoteFixture) allowPrivate(t *testing.T, origins ...string) {
	t.Helper()
	resolver, err := remote.NewResolver(
		f.items.Store,
		nil,
		remote.ResolverOptions{
			RevisionKey:           []byte(strings.Repeat("k", 32)),
			AllowedPrivateOrigins: origins,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.HTTP.(*remote.Policy).Options = resolver.Options
}

func TestRemoteIndexRedirectBaseAndMIME(t *testing.T) {
	for _, media := range []string{"application/yaml", "application/x-yaml", "text/yaml", "text/plain", "application/octet-stream"} {
		t.Run(media, func(t *testing.T) {
			f := newRemoteFixture(t)
			a, data := f.chart("web", "1.0.0", "web-1.0.0.tgz")
			index := f.index(a)
			f.up.Set("/charts/index.yaml", fakeupstream.Resource{Redirect: "/moved/index.yaml"})
			f.up.Set(
				"/moved/index.yaml",
				fakeupstream.Resource{
					Body:   index,
					Header: http.Header{"Content-Type": []string{media}},
				},
			)
			f.up.Set("/moved/web-1.0.0.tgz", fakeupstream.Resource{Body: data})
			got := f.get("index.yaml", "reader")
			expectRemote(t, got, 200)
			var parsed helmfmt.Index
			if err := yaml.Unmarshal(got.Body.Bytes(), &parsed); err != nil {
				t.Fatal(err)
			}
			location := strings.TrimPrefix(
				parsed.Entries["web"][0].URLs[0],
				"https://proxy.example"+remoteEndpoint,
			)
			if !strings.HasPrefix(location, "_external/http/") {
				t.Fatal("redirected chart mapped to wrong cache path", location)
			}
			expectRemote(t, f.get(location, "reader"), 200)
			if f.up.Count("GET", "/moved/web-1.0.0.tgz") != 1 ||
				f.up.Count("GET", "/charts/web-1.0.0.tgz") != 0 {
				t.Fatal("relative link used configured URL instead of response URL")
			}
			f.up.Close()
			f.configure(func(c *repo.Config) { v := true; c.Offline = &v })
			expectRemote(t, f.get("index.yaml", "reader"), 200, location)
			expectRemote(t, f.get(location, "reader"), 200)
		})
	}
}
