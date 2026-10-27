package npm_test

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteRegistry = "/artifactory/api/npm/npm-remote/"

type remoteFixture struct {
	*fixture
	up     *fakeupstream.Server
	policy *remote.Policy
	svc    *source.Service
	clock  *fakeupstream.Clock
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newFixture(t)
	up := fakeupstream.New(t, fakeupstream.Options{})
	c, err := f.db.Repos().Get(t.Context(), "npm-remote")
	if err != nil {
		t.Fatal(err)
	}
	c.URL = up.URL
	ttl := 5
	c.RetrievalCachePeriodSecs = &ttl
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for user, include := range map[string]string{"reader": "**", "partial": "mixed/-/mixed-1.*"} {
		err = f.db.Permissions().
			Create(t.Context(), types.Permission{Name: "remote-" + user, Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{user: types.ActionRead}}, Targets: map[string]types.Target{"npm-remote": {Include: []string{include}}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := fakeupstream.NewClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	p := &remote.Policy{
		Store:   f.db,
		Blobs:   f.items.Blobs,
		Items:   f.items,
		Pool:    new(remote.Pool),
		Options: resolver.Options,
		Key:     []byte(strings.Repeat("k", 32)),
		Now:     clock.Now,
	}
	t.Cleanup(p.Pool.Close)
	svc := &source.Service{
		HTTP:  p,
		Cache: p,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	f.opts.ServeItem = func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
		body, _, err := f.items.Blobs.Open(r.Context(), it.Actual.SHA256)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		defer body.Close()
		_, _ = io.Copy(w, body)
	}
	out := &remoteFixture{fixture: f, up: up, policy: p, svc: svc, clock: clock}
	out.mount(t, base)
	return out
}

func (f *remoteFixture) mount(t *testing.T, public string) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(public))
	r.Use(f.principal)
	pkgroute.Mount(r, f.opts, npm.Protocol(npm.Options{RemoteSources: f.svc}))
	f.h = r
}

func (f *remoteFixture) packageDoc(t *testing.T, name string, versions ...string) map[string]any {
	t.Helper()
	docs := map[string]any{}
	for _, version := range versions {
		data := tarball(`{"name":"` + name + `","version":"` + version + `"}`)
		sum := sha512.Sum512(data)
		path := name + "/-/" + strings.TrimPrefix(name, "@") + "-" + version + ".tgz"
		docs[version] = map[string]any{
			"name":    name,
			"version": version,
			"dist": map[string]any{
				"tarball":   f.up.URL + "/" + path,
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:]),
				"signatures": []any{
					map[string]string{"keyid": "upstream-key", "sig": "unchanged-signature"},
				},
				"future-field": "retained",
			},
			"custom-field": map[string]any{"kept": true},
		}
		f.up.Set(
			"/"+path,
			fakeupstream.Resource{
				Body:   data,
				Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
			},
		)
	}
	doc := map[string]any{
		"name":      name,
		"versions":  docs,
		"dist-tags": map[string]string{"latest": versions[len(versions)-1]},
		"readme":    "upstream readme",
	}
	f.setJSON("/"+name, doc)
	return doc
}

func (f *remoteFixture) setJSON(path string, v any) {
	b, _ := json.Marshal(v)
	f.up.Set(
		path,
		fakeupstream.Resource{
			Body:   b,
			Header: http.Header{"Content-Type": []string{"application/json"}},
		},
	)
}

func requireRemote(t *testing.T, r *httptest.ResponseRecorder, status int, contains ...string) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status %d want%d body%s", r.Code, status, r.Body)
	}
	for _, s := range contains {
		if !strings.Contains(r.Body.String(), s) {
			t.Fatalf("missing %s in %s", s, r.Body)
		}
	}
}

func TestAccRemoteMetadataVariantsPermissionsAndPublicURLs(t *testing.T) {
	f := newRemoteFixture(t)
	doc := f.packageDoc(t, "mixed", "1.0.0", "2.0.0")
	full := f.do("GET", remoteRegistry+"mixed", "reader")
	requireRemote(
		t,
		full,
		200,
		"upstream readme",
		"unchanged-signature",
		"future-field",
		base+remoteRegistry+"mixed/-/mixed-2.0.0.tgz",
	)
	partial := f.do("GET", remoteRegistry+"mixed", "partial")
	requireRemote(t, partial, 200, "1.0.0")
	if strings.Contains(partial.Body.String(), "2.0.0") {
		t.Fatal("forbidden version disclosed")
	}
	requireRemote(t, f.do("GET", remoteRegistry+"mixed/2.0.0", "partial"), 404)
	requireRemote(t, f.do("GET", remoteRegistry+"mixed/latest", "reader"), 200, `"version":"2.0.0"`)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"-/package/mixed/dist-tags", "reader"),
		200,
		`"latest":"2.0.0"`,
	)
	raw, _ := json.Marshal(doc)
	f.up.Set(
		"/mixed",
		fakeupstream.Resource{
			Body:   raw,
			Header: http.Header{"Content-Type": []string{"application/vnd.npm.install-v1+json"}},
		},
	)
	abbr := f.do("GET", remoteRegistry+"mixed", "reader", "Accept", abbreviatedAccept)
	requireRemote(t, abbr, 200)
	if got := abbr.Header().Get("Content-Type"); got != "application/vnd.npm.install-v1+json" {
		t.Fatal(got)
	}
	if f.up.Count("GET", "/mixed") != 2 {
		t.Fatal("metadata variants aliased")
	}
	requireRemote(t, f.do("GET", remoteRegistry+"mixed", "nobody"), 403)
	f.mount(t, "https://second.example")
	other := f.do("GET", remoteRegistry+"mixed", "reader")
	requireRemote(t, other, 200, "https://second.example/artifactory/api/npm/")
	if other.Header().Get("ETag") == full.Header().Get("ETag") ||
		strings.Contains(other.Body.String(), base) {
		t.Fatal("public URL cache poisoned")
	}
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"mixed", "reader", "If-None-Match", other.Header().Get("ETag")),
		304,
	)
	requireRemote(t, f.do("HEAD", remoteRegistry+"mixed", "reader"), 200)
}

func TestAccRemoteTarballIntegrityColdWarmOfflineAndReindex(t *testing.T) {
	f := newRemoteFixture(t)
	doc := f.packageDoc(t, "@acme/pkg", "1.0.0")
	path := npmfmt.CanonicalPath("@acme/pkg", "1.0.0")
	cold := f.do("GET", remoteRegistry+path, "reader")
	requireRemote(t, cold, 200)
	cached, err := f.db.Items().Get(t.Context(), "npm-remote", true, path)
	if err != nil || cached.Actual.SHA512 == "" {
		t.Fatalf("cache: %+v %v", cached, err)
	}
	// Reindexing the tarball cannot replace the raw upstream dist/signatures.
	c, getErr := f.db.Repos().Get(t.Context(), "npm-remote")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if err = (npmfmt.Handler{}).Reindex(t.Context(), f.items.Env(), c); err != nil {
		t.Fatal(err)
	}
	f.setJSON("/@acme/pkg", doc)
	f.up.Close()
	c, _ = f.db.Repos().Get(t.Context(), "npm-remote")
	off := true
	c.Offline = &off
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	warm := f.do("GET", remoteRegistry+path, "reader")
	requireRemote(t, warm, 200)
	if warm.Body.String() != cold.Body.String() {
		t.Fatal("warm bytes differ")
	}
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"@acme%2fpkg", "reader"),
		200,
		"unchanged-signature",
	)
}

func TestAccRemoteInvalidBytesNeverPublish(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDoc(t, "pkg", "1.0.0")
	f.up.Set(
		"/pkg/-/pkg-1.0.0.tgz",
		fakeupstream.Resource{
			Body:   []byte("corrupt bytes"),
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
		},
	)
	requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 502)
	if _, err := f.db.Items().
		Get(t.Context(), "npm-remote", true, "pkg/-/pkg-1.0.0.tgz"); err == nil {
		t.Fatal("invalid tarball published")
	}
}

func TestAccRemoteMetadataRefreshTagsNegativeAndOutage(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDoc(t, "pkg", "1.0.0")
	requireRemote(t, f.do("GET", remoteRegistry+"pkg", "reader"), 200, "1.0.0")
	f.packageDoc(t, "pkg", "1.0.0", "2.0.0")
	f.clock.Advance(6 * time.Second)
	requireRemote(t, f.do("GET", remoteRegistry+"pkg/latest", "reader"), 200, `"version":"2.0.0"`)
	requireRemote(t, f.do("GET", remoteRegistry+"absent", "reader"), 404)
	requireRemote(t, f.do("GET", remoteRegistry+"absent", "reader"), 404)
	if f.up.Count("GET", "/absent") > 1 || f.up.Count("HEAD", "/absent") > 1 {
		t.Fatal("negative cache ignored")
	}
	f.up.Close()
	f.clock.Advance(6 * time.Second)
	requireRemote(t, f.do("GET", remoteRegistry+"pkg/latest", "reader"), 200, `"version":"2.0.0"`)
}

func TestAccRemoteSearchFiltersAndKeepsUpstreamFields(t *testing.T) {
	f := newRemoteFixture(t)
	f.setJSON(
		"/-/v1/search",
		map[string]any{
			"objects": []any{
				map[string]any{
					"package": map[string]string{"name": "mixed", "version": "1.0.0"},
					"score":   map[string]int{"final": 1},
				},
				map[string]any{"package": map[string]string{"name": "hidden", "version": "2.0.0"}},
			},
			"total": 999,
		},
	)
	res := f.do("GET", remoteRegistry+"-/v1/search?text=mixed&size=20", "partial")
	requireRemote(t, res, 200, "mixed", "score")
	if strings.Contains(res.Body.String(), "hidden") || strings.Contains(res.Body.String(), "999") {
		t.Fatal("search discloses inaccessible package")
	}
	requireRemote(t, f.do("POST", remoteRegistry+"-/npm/v1/security/audits/quick", "reader"), 405)
}

func TestAccRemoteExternalURLCredentialsAndDeniedDestinations(t *testing.T) {
	f := newRemoteFixture(t)
	target := fakeupstream.New(t, fakeupstream.Options{})
	doc := f.packageDoc(t, "pkg", "1.0.0")
	data := tarball(`{"name":"pkg","version":"1.0.0"}`)
	target.Set(
		"/pkg.tgz",
		fakeupstream.Resource{
			Body:   data,
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
		},
	)
	dist := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
	dist["tarball"] = target.URL + "/pkg.tgz"
	f.setJSON("/pkg", doc)
	requireRemote(t, f.do("GET", remoteRegistry+"pkg", "reader"), 502)
	if len(target.Requests()) != 0 {
		t.Fatal("disabled URL contacted")
	}
	c, _ := f.db.Repos().Get(t.Context(), "npm-remote")
	yes := true
	c.ExternalDependenciesEnabled = &yes
	c.ExternalDependenciesPatterns = []string{target.URL + "/**"}
	c.Username = "upstream-user"
	c.Password = "upstream-secret"
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{
			RevisionKey:           []byte(strings.Repeat("k", 32)),
			AllowedPrivateOrigins: []string{target.URL},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.policy.Options = resolver.Options
	requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 200)
	for _, request := range target.Requests() {
		if request.Header.Get("Authorization") != "" {
			t.Fatal("origin credential leaked")
		}
	}
	// Even a broad administrator allow pattern cannot authorize cloud metadata.
	c.ExternalDependenciesPatterns = []string{"**"}
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	dist["tarball"] = "http://169.254.169.254/latest/meta-data/"
	f.setJSON("/pkg", doc)
	requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 502)
}

func TestAccRemotePackageMutationsRemainReadOnly(t *testing.T) {
	f := newRemoteFixture(t)
	for _, tc := range []struct{ method, path string }{{"PUT", "pkg"}, {"DELETE", "pkg"}, {"PUT", "-/package/pkg/dist-tags/latest"}, {"DELETE", "-/package/pkg/dist-tags/latest"}} {
		requireRemote(t, f.do(tc.method, remoteRegistry+tc.path, "admin"), 405)
	}
	if len(f.up.Requests()) != 0 {
		t.Fatal("write attempted upstream traffic")
	}
}

func TestAccRemoteMetadataAndSearchFenceRevokedVersion(t *testing.T) {
	for _, path := range []string{"mixed", "-/v1/search?text=mixed"} {
		t.Run(path, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.packageDoc(t, "mixed", "1.0.0", "2.0.0")
			f.setJSON(
				"/-/v1/search",
				map[string]any{
					"objects": []any{
						map[string]any{
							"package": map[string]string{"name": "mixed", "version": "1.0.0"},
						},
						map[string]any{
							"package": map[string]string{"name": "mixed", "version": "2.0.0"},
						},
					},
				},
			)
			f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				// The metadata folder remains readable through version 2. Revocation of
				// version 1 must nevertheless fence the previously filtered response.
				if q.Resource.Kind == source.ResourceKindArtifact &&
					q.Resource.Path == "mixed/-/mixed-1.0.0.tgz" {
					return source.ErrDenied
				}
				return nil
			}
			requireRemote(t, f.do("GET", remoteRegistry+path, "reader"), 403)
		})
	}
}

func TestAccRemoteNoStoreAndMalformedMetadata(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDoc(t, "pkg", "1.0.0")
	c, _ := f.db.Repos().Get(t.Context(), "npm-remote")
	off := false
	c.StoreArtifactsLocally = &off
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 200)
	if _, err := f.db.Items().
		Get(t.Context(), "npm-remote", true, "pkg/-/pkg-1.0.0.tgz"); err == nil {
		t.Fatal("no-store published item")
	}
	for _, raw := range []string{`null`, `{"name":"different","versions":{}}`, `{"name":"pkg","versions":null}`, `{"name":"pkg","versions":{}}`} {
		f.up.Set(
			"/pkg",
			fakeupstream.Resource{
				Body:   []byte(raw),
				Header: http.Header{"Content-Type": []string{"application/json"}},
			},
		)
		res := f.do("GET", remoteRegistry+"pkg", "reader")
		if res.Code != 502 && res.Code != 404 {
			t.Fatalf("malformed metadata status %d %s", res.Code, res.Body)
		}
	}
}

func TestAccRemoteInvalidMetadataCanBeCorrectedWithoutCacheExpiry(t *testing.T) {
	for _, invalid := range []string{
		`{"name":"wrong","versions":{},"dist-tags":{}}`,
		`{"name":"pkg","versions":{"invalid":{"name":"pkg","version":"invalid","dist":{"tarball":"pkg.tgz"}}},"dist-tags":{}}`,
		`{"name":"pkg","versions":{},"dist-tags":{"latest":"missing"}}`,
		`{"name":"pkg","versions":{},"dist-tags":{}} {}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.up.Set(
				"/pkg",
				fakeupstream.Resource{
					Body:   []byte(invalid),
					Header: http.Header{"Content-Type": []string{"application/json"}},
				},
			)
			requireRemote(t, f.do("GET", remoteRegistry+"pkg", "reader"), 502)
			f.packageDoc(t, "pkg", "1.0.0")
			requireRemote(t, f.do("GET", remoteRegistry+"pkg", "reader"), 200, "1.0.0")
		})
	}
	for _, invalid := range []string{`{"objects":null}`, `{"objects":[{"package":{"name":"../escape","version":"1.0.0"}}]}`, `{"objects":[{"package":{"name":"pkg","version":"bad"}}]}`} {
		t.Run("search="+invalid, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.up.Set(
				"/-/v1/search",
				fakeupstream.Resource{
					Body:   []byte(invalid),
					Header: http.Header{"Content-Type": []string{"application/json"}},
				},
			)
			requireRemote(t, f.do("GET", remoteRegistry+"-/v1/search", "reader"), 502)
			f.setJSON(
				"/-/v1/search",
				map[string]any{
					"objects": []any{
						map[string]any{
							"package": map[string]string{"name": "pkg", "version": "1.0.0"},
						},
					},
				},
			)
			requireRemote(t, f.do("GET", remoteRegistry+"-/v1/search", "reader"), 200, "pkg")
		})
	}
}

func TestAccRemoteAdmissionValidatesHiddenVersions(t *testing.T) {
	f := newRemoteFixture(t)
	doc := f.packageDoc(t, "mixed", "1.0.0", "2.0.0")
	versions := doc["versions"].(map[string]any)
	hidden := versions["2.0.0"].(map[string]any)
	hidden["version"] = "wrong"
	f.setJSON("/mixed", doc)
	requireRemote(t, f.do("GET", remoteRegistry+"mixed", "partial"), 502)
	hidden["version"] = "2.0.0"
	f.setJSON("/mixed", doc)
	requireRemote(t, f.do("GET", remoteRegistry+"mixed", "partial"), 200, "1.0.0")
	requireRemote(t, f.do("GET", remoteRegistry+"mixed", "reader"), 200, "2.0.0")
}

func TestAccRemoteSHA384ValidationOfflineAndNoStore(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprintf("no-store=%v", noStore), func(t *testing.T) {
			f := newRemoteFixture(t)
			doc := f.packageDoc(t, "pkg", "1.0.0")
			data := tarball(`{"name":"pkg","version":"1.0.0"}`)
			sum := sha512.Sum384(data)
			dist := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
			dist["integrity"] = "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
			f.setJSON("/pkg", doc)
			c, _ := f.db.Repos().Get(t.Context(), "npm-remote")
			store := !noStore
			c.StoreArtifactsLocally = &store
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			result := f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader")
			requireRemote(t, result, 200)
			if result.Body.String() != string(data) {
				t.Fatal("SRI bytes changed")
			}
			if noStore {
				if _, err := f.db.Items().
					Get(t.Context(), "npm-remote", true, "pkg/-/pkg-1.0.0.tgz"); err == nil {
					t.Fatal("no-store item published")
				}
				f.up.Set(
					"/pkg/-/pkg-1.0.0.tgz",
					fakeupstream.Resource{
						Body:   []byte("tampered"),
						Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
					},
				)
				requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 502)
				return
			}
			offline := true
			c.Offline = &offline
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 200)
			offline = false
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			changed := sha512.Sum384([]byte("replacement"))
			dist["integrity"] = "sha384-" + base64.StdEncoding.EncodeToString(changed[:])
			f.setJSON("/pkg", doc)
			f.clock.Advance(6 * time.Second)
			requireRemote(t, f.do("GET", remoteRegistry+"pkg", "reader"), 200)
			offline = true
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			requireRemote(t, f.do("GET", remoteRegistry+"pkg/-/pkg-1.0.0.tgz", "reader"), 502)
		})
	}
}
