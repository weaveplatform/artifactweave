package pypi_test

import (
	"context"
	"crypto/sha256"
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
	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/pypi"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteRegistry = "/artifactory/api/pypi/pypi-remote/"

type remoteFixture struct {
	*fixture
	up     *fakeupstream.Server
	policy *remote.Policy
	svc    *source.Service
	clock  *fakeupstream.Clock
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newFixture(t, pypifmt.Settings{SimpleJSON: true, CoreMetadata: true})
	up := fakeupstream.New(t, fakeupstream.Options{})
	c, err := f.db.Repos().Get(t.Context(), "pypi-remote")
	if err != nil {
		t.Fatal(err)
	}
	c.URL = up.URL
	c.PyPIRegistryURL = up.URL
	c.PyPIRepositorySuffix = "custom/simple"
	ttl := 5
	c.RetrievalCachePeriodSecs = &ttl
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for user, include := range map[string]string{"reader": "**", "partial": "demo/1.0/**"} {
		err = f.db.Permissions().
			Create(t.Context(), types.Permission{Name: "remote-" + user, Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{user: types.ActionRead}}, Targets: map[string]types.Target{"pypi-remote": {Include: []string{include}}}}}})
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
	r.Use(principal(t, f.db))
	pkgroute.Mount(
		r,
		f.opts,
		pypi.Protocol(
			pypi.Options{
				RemoteSources: f.svc,
				Format:        pypifmt.Settings{SimpleJSON: true, CoreMetadata: true},
			},
		),
	)
	f.h = r
}

func (f *remoteFixture) project(t *testing.T, name string, versions ...string) map[string]any {
	t.Helper()
	files := []map[string]any{}
	for _, v := range versions {
		filename := strings.ReplaceAll(name, "-", "_") + "-" + v + "-py3-none-any.whl"
		meta := metadata(name, v, "Requires-Python: >=3.8\n")
		body := wheel(t, name, v, meta)
		sum := sha256.Sum256(body)
		msum := sha256.Sum256([]byte(meta))
		href := "/files/" + filename
		files = append(
			files,
			map[string]any{
				"filename":        filename,
				"url":             href,
				"hashes":          map[string]string{"sha256": fmt.Sprintf("%x", sum)},
				"requires-python": ">=3.8",
				"yanked":          false,
				"core-metadata":   map[string]string{"sha256": fmt.Sprintf("%x", msum)},
				"future-field":    "retained",
			},
		)
		f.up.Set(
			href,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
			},
		)
		f.up.Set(
			href+".metadata",
			fakeupstream.Resource{
				Body:   []byte(meta),
				Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
			},
		)
	}
	doc := map[string]any{
		"meta":     map[string]string{"api-version": "1.1"},
		"name":     name,
		"files":    files,
		"versions": versions,
	}
	f.setJSON("/custom/simple/"+name+"/", doc)
	return doc
}

func (f *remoteFixture) setJSON(path string, v any) {
	b, _ := json.Marshal(v)
	f.up.Set(
		path,
		fakeupstream.Resource{
			Body:   b,
			Header: http.Header{"Content-Type": []string{"application/vnd.pypi.simple.v1+json"}},
		},
	)
}

func requireRemote(t *testing.T, r *httptest.ResponseRecorder, status int, contains ...string) {
	t.Helper()
	wantStatus(t, r, status)
	for _, s := range contains {
		if !strings.Contains(r.Body.String(), s) {
			t.Fatalf("missing %q in %s", s, r.Body)
		}
	}
}

func TestRemoteIndexDownloadAndOffline(t *testing.T) {
	f := newRemoteFixture(t)
	f.project(t, "demo", "1.0", "2.0")
	requireRemote(t, f.do("GET", remoteRegistry+"simple/DEMO/", "reader"), 302)
	json := f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept)
	requireRemote(
		t,
		json,
		200,
		"future-field",
		"https://pypi.example/artifactory/api/pypi/pypi-remote/demo/1.0/demo-1.0-py3-none-any.whl",
		"core-metadata",
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "partial", "Accept", pipAccept),
		200,
		"demo-1.0",
	)
	if strings.Contains(
		f.do("GET", remoteRegistry+"simple/demo/", "partial", "Accept", pipAccept).Body.String(),
		"demo-2.0",
	) {
		t.Fatal("forbidden version leaked")
	}
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader"),
		200,
		"data-requires-python=\"&gt;=3.8\"",
		"data-core-metadata",
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl.metadata", "partial"),
		200,
		"Name: demo",
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "partial"),
		200,
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/2.0/demo-2.0-py3-none-any.whl", "partial"),
		403,
	)
	requireRemote(t, f.do("POST", remoteRegistry, "admin"), 405)
	requireRemote(t, f.do("PUT", remoteRegistry+"file.whl", "admin"), 405)
	c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
	offline := true
	c.Offline = &offline
	_ = f.db.Repos().Update(t.Context(), c)
	f.up.Close()
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
		200,
		"demo-2.0",
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "partial"),
		200,
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl.metadata", "partial"),
		200,
		"Name: demo",
	)
}

func TestRemoteInvalidIndexDoesNotPoisonCache(t *testing.T) {
	for _, bad := range []string{`{"meta":{"api-version":"1.1"},"name":"other","files":[]}`, `{"meta":{"api-version":"1.1"},"name":"demo","files":[]} trailing`, `<!DOCTYPE html><html><a href="file:///secret.whl">secret.whl</a></html>`, `<html><a href="http://user:secret@example.com/demo-1.0-py3-none-any.whl">demo-1.0-py3-none-any.whl</a></html>`} {
		f := newRemoteFixture(t)
		f.up.Set("/custom/simple/demo/", fakeupstream.Resource{Body: []byte(bad)})
		requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 502)
		f.project(t, "demo", "1.0")
		requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 200, "demo-1.0")
	}
}

func TestRemoteHashMismatchAndTruncation(t *testing.T) {
	for _, fault := range []bool{false, true} {
		f := newRemoteFixture(t)
		f.project(t, "demo", "1.0")
		resource := fakeupstream.Resource{
			Body:   []byte("corrupted bytes"),
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
		}
		if fault {
			resource.Fault = fakeupstream.Fault{TruncateAfter: 3}
		}
		f.up.Set("/files/demo-1.0-py3-none-any.whl", resource)
		requireRemote(
			t,
			f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "reader"),
			502,
		)
		if _, err := f.db.Items().
			Get(t.Context(), "pypi-remote", true, "demo/1.0/demo-1.0-py3-none-any.whl"); err == nil {
			t.Fatal("bad item published")
		}
		f.project(t, "demo", "1.0")
		requireRemote(
			t,
			f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "reader"),
			200,
		)
	}
}

func TestRemoteWarehouseAndList(t *testing.T) {
	f := newRemoteFixture(t)
	doc := f.project(t, "demo", "1.0", "2.0")
	files := doc["files"].([]map[string]any)
	entries := []map[string]any{}
	releases := map[string]any{}
	for i, file := range files {
		e := map[string]any{
			"filename":        file["filename"],
			"url":             file["url"],
			"digests":         file["hashes"],
			"requires_python": ">=3.8",
			"yanked":          false,
			"core-metadata":   file["core-metadata"],
			"custom":          "retained",
		}
		entries = append(entries, e)
		releases[fmt.Sprintf("%d.0", i+1)] = []any{e}
	}
	warehouse := map[string]any{
		"info":     map[string]string{"name": "Demo", "version": "1.0"},
		"urls":     entries[:1],
		"releases": releases,
	}
	f.setJSON("/pypi/demo/json", warehouse)
	f.setJSON("/pypi/demo/1.0/json", warehouse)
	for _, p := range []string{"pypi/Demo/json", "pypi/demo/1.0/json"} {
		res := f.do("GET", remoteRegistry+p, "partial")
		requireRemote(t, res, 200, "custom", "/demo/1.0/demo-1.0-py3-none-any.whl")
		if strings.Contains(res.Body.String(), "demo-2.0") {
			t.Fatal("hidden release leaked")
		}
	}
	f.setJSON(
		"/custom/simple/",
		map[string]any{
			"meta":     map[string]string{"api-version": "1.0"},
			"projects": []any{map[string]string{"name": "Demo"}},
		},
	)
	requireRemote(t, f.do("GET", remoteRegistry+"simple/", "reader"), 200, "demo/")
	requireRemote(t, f.do("GET", remoteRegistry+"simple/", "partial"), 200, "demo/")
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/", "reader", "Accept", pipAccept),
		200,
		`"name":"demo"`,
	)
}

func TestRemoteHTMLSemanticsAndBaseRewriting(t *testing.T) {
	f := newRemoteFixture(t)
	f.project(t, "demo", "1.0")
	b := wheel(t, "demo", "1.0", metadata("demo", "1.0", "Requires-Python: >=3.8\n"))
	hash := sha256Hex(b)
	html := `<!DOCTYPE html><html><body><a href="../../../files/demo-1.0-py3-none-any.whl#sha256=` + hash + `" data-requires-python="&gt;=3.8" data-yanked="broken" data-dist-info-metadata="true">demo-1.0-py3-none-any.whl</a></body></html>`
	f.up.Set(
		"/custom/simple/demo/",
		fakeupstream.Resource{
			Body:   []byte(html),
			Header: http.Header{"Content-Type": []string{"text/html"}},
		},
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
		200,
		`"yanked":"broken"`,
		`"requires-python":">=3.8"`,
		`"core-metadata":true`,
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "reader"),
		200,
	)
	f.mount(t, "https://second.example/custom")
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader"),
		200,
		"https://second.example/custom/artifactory/api/pypi/pypi-remote/",
	)
}

func TestRemoteRefreshNegativeAndNoStore(t *testing.T) {
	f := newRemoteFixture(t)
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 404)
	f.project(t, "demo", "1.0")
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 404)
	f.clock.Advance(31 * time.Minute)
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 200, "demo-1.0")
	f.project(t, "demo", "1.0", "2.0")
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 200, "demo-1.0")
	f.clock.Advance(6 * time.Second)
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader"), 200, "demo-2.0")
	c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
	store := false
	c.StoreArtifactsLocally = &store
	_ = f.db.Repos().Update(t.Context(), c)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "reader"),
		200,
	)
	if _, err := f.db.Items().
		Get(t.Context(), "pypi-remote", true, "demo/1.0/demo-1.0-py3-none-any.whl"); err == nil {
		t.Fatal("transient distribution published")
	}
}

func TestRemoteDisclosuresUseCurrentPermission(t *testing.T) {
	for _, path := range []string{"simple/demo/", "demo/1.0/demo-1.0-py3-none-any.whl.metadata"} {
		f := newRemoteFixture(t)
		f.project(t, "demo", "1.0", "2.0")
		f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
			if q.Resource.Kind == source.Artifact &&
				q.Resource.Path == "demo/1.0/demo-1.0-py3-none-any.whl" {
				return source.ErrDenied
			}
			return nil
		}
		requireRemote(t, f.do("GET", remoteRegistry+path, "reader"), 403)
	}
}

func TestRemoteExternalLinksKeepCredentialsAtOrigin(t *testing.T) {
	f := newRemoteFixture(t)
	doc := f.project(t, "demo", "1.0")
	target := fakeupstream.New(t, fakeupstream.Options{})
	body := wheel(t, "demo", "1.0", metadata("demo", "1.0", "Requires-Python: >=3.8\n"))
	target.Set("/demo-1.0-py3-none-any.whl", fakeupstream.Resource{Body: body})
	doc["files"].([]map[string]any)[0]["url"] = target.URL + "/demo-1.0-py3-none-any.whl"
	f.setJSON("/custom/simple/demo/", doc)
	c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
	c.Username = "source-user"
	c.Password = "source-secret"
	_ = f.db.Repos().Update(t.Context(), c)
	// An unconfigured private origin is refused before a connection, even when discovered in a valid index.
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "reader"),
		502,
	)
	if len(target.Requests()) != 0 {
		t.Fatal("untrusted private target contacted")
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
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl", "reader"),
		200,
	)
	for _, q := range target.Requests() {
		if q.Header.Get("Authorization") != "" {
			t.Fatal("origin credential leaked")
		}
	}
}

func TestRemoteNegotiatedMIMERefreshAndOffline(t *testing.T) {
	f := newRemoteFixture(t)
	f.project(t, "demo", "1.0")
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
		200,
		"demo-1.0",
	)
	hash := sha256Hex(wheel(t, "demo", "1.0", metadata("demo", "1.0", "Requires-Python: >=3.8\n")))
	body := []byte(
		`<html><a href="/files/demo-1.0-py3-none-any.whl#sha256=` + hash + `" data-yanked="temporarily broken">demo-1.0-py3-none-any.whl</a></html>`,
	)
	f.clock.Advance(6 * time.Second)
	f.up.Set(
		"/custom/simple/demo/",
		fakeupstream.Resource{
			Body:   body,
			Header: http.Header{"Content-Type": []string{"application/json"}},
		},
	)
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept), 502)
	f.up.Set(
		"/custom/simple/demo/",
		fakeupstream.Resource{
			Body:   body,
			Header: http.Header{"Content-Type": []string{"application/vnd.pypi.simple.v1+html"}},
		},
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
		200,
		`"yanked":"temporarily broken"`,
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader"),
		200,
		`data-yanked="temporarily broken"`,
	)
	c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
	offline := true
	c.Offline = &offline
	_ = f.db.Repos().Update(t.Context(), c)
	f.up.Close()
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
		200,
		"temporarily broken",
	)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader"),
		200,
		"temporarily broken",
	)
}

func TestRemoteAdvertisedSignatureColdAndOffline(t *testing.T) {
	for _, representation := range []string{"html", "json"} {
		t.Run(representation, func(t *testing.T) {
			f := newRemoteFixture(t)
			doc := f.project(t, "demo", "1.0")
			key, err := terraformtest.NewKey()
			if err != nil {
				t.Fatal(err)
			}
			sig := key.Sign([]byte("signed distribution"))
			index := `<html><a href="/files/demo-1.0-py3-none-any.whl" data-gpg-sig="true">demo-1.0-py3-none-any.whl</a></html>`
			f.up.Set(
				"/custom/simple/demo/",
				fakeupstream.Resource{
					Body:   []byte(index),
					Header: http.Header{"Content-Type": []string{"text/html"}},
				},
			)
			if representation == "json" {
				doc["files"].([]map[string]any)[0]["gpg-sig"] = true
				f.setJSON("/custom/simple/demo/", doc)
			}

			f.up.Set(
				"/files/demo-1.0-py3-none-any.whl.asc",
				fakeupstream.Resource{Body: []byte("malformed signature")},
			)
			requireRemote(
				t,
				f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl.asc", "reader"),
				502,
			)
			f.up.Set("/files/demo-1.0-py3-none-any.whl.asc", fakeupstream.Resource{Body: sig})
			result := f.do(
				"GET",
				remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl.asc",
				"partial",
			)
			requireRemote(t, result, 200)
			if result.Body.String() != string(sig) {
				t.Fatal("signature bytes changed")
			}
			c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
			offline := true
			c.Offline = &offline
			_ = f.db.Repos().Update(t.Context(), c)
			f.up.Close()
			result = f.do("GET", remoteRegistry+"demo/1.0/demo-1.0-py3-none-any.whl.asc", "partial")
			requireRemote(t, result, 200)
			if result.Body.String() != string(sig) {
				t.Fatal("offline signature changed")
			}
		})
	}
}

func TestRemoteRedirectBaseAdmissionDownloadAndOffline(t *testing.T) {
	for _, representation := range []string{"json", "html"} {
		t.Run(representation, func(t *testing.T) {
			f := newRemoteFixture(t)
			doc := f.project(t, "demo", "1.0")
			file := "demo-1.0-py3-none-any.whl"
			dist := wheel(t, "demo", "1.0", metadata("demo", "1.0", "Requires-Python: >=3.8\n"))
			f.up.Set(
				"/redirected/files/"+file,
				fakeupstream.Resource{
					Body:   dist,
					Header: http.Header{"Content-Type": {"application/octet-stream"}},
				},
			)
			doc["files"].([]map[string]any)[0]["url"] = "../../files/" + file
			doc["files"].([]map[string]any)[0]["hashes"] = map[string]string{}
			f.up.Set(
				"/custom/simple/demo/",
				fakeupstream.Resource{
					Redirect: "/redirected/simple/demo/?signed=redacted",
				},
			)
			path := "/redirected/simple/demo/"
			if representation == "json" {
				f.setJSON(path, doc)
			} else {
				f.up.Set(
					path,
					fakeupstream.Resource{
						Body: []byte(
							`<html><head><base href="../links/"></head><a href="../../files/` + file + `">` + file + `</a></html>`,
						),
						Header: http.Header{"Content-Type": {"text/html"}},
					},
				)
			}
			index := f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept)
			requireRemote(t, index, 200, file)
			for key, values := range index.Header() {
				if strings.Contains(strings.Join(values, " "), f.up.URL) ||
					strings.Contains(strings.Join(values, " "), "redacted") {
					t.Fatalf("response base exposed in %s", key)
				}
			}
			if strings.Contains(index.Body.String(), "redacted") ||
				strings.Contains(index.Body.String(), f.up.URL) {
				t.Fatal("upstream base exposed")
			}
			requireRemote(t, f.do("GET", remoteRegistry+"demo/1.0/"+file, "reader"), 200)
			if f.up.Count("GET", "/redirected/files/"+file) != 1 {
				t.Fatal("distribution did not use final response base", f.up.Requests())
			}
			c, err := f.db.Repos().Get(t.Context(), "pypi-remote")
			if err != nil {
				t.Fatal(err)
			}
			offline := true
			c.Offline = &offline
			if err = f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			before := len(f.up.Requests())
			requireRemote(
				t,
				f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
				200,
				file,
			)
			requireRemote(t, f.do("GET", remoteRegistry+"demo/1.0/"+file, "reader"), 200)
			if len(f.up.Requests()) != before {
				t.Fatal("offline redirect read fetched upstream")
			}
		})
	}
}

func TestRemoteRedirectedMalformedIndexCanRetryImmediately(t *testing.T) {
	f := newRemoteFixture(t)
	doc := f.project(t, "demo", "1.0")
	f.up.Set("/custom/simple/demo/", fakeupstream.Resource{Redirect: "/new/simple/demo/"})
	doc["files"].([]map[string]any)[0]["url"] = "file:///demo-1.0-py3-none-any.whl"
	f.setJSON("/new/simple/demo/", doc)
	requireRemote(t, f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept), 502)
	doc["files"].([]map[string]any)[0]["url"] = "/files/demo-1.0-py3-none-any.whl"
	f.setJSON("/new/simple/demo/", doc)
	requireRemote(
		t,
		f.do("GET", remoteRegistry+"simple/demo/", "reader", "Accept", pipAccept),
		200,
		"demo-1.0",
	)
}

func TestRemoteListRequiresActualReadableDistribution(t *testing.T) {
	for _, target := range []types.Target{{Include: []string{"demo/.project"}}, {Include: []string{"demo/**"}, Exclude: []string{"demo/1.0/**", "demo/2.0/**"}}} {
		f := newRemoteFixture(t)
		f.project(t, "demo", "1.0", "2.0")
		f.setJSON(
			"/custom/simple/",
			map[string]any{
				"meta":     map[string]string{"api-version": "1.1"},
				"projects": []map[string]string{{"name": "demo"}},
			},
		)
		if err := f.db.Permissions().Delete(t.Context(), "remote-partial"); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Permissions().
			Create(t.Context(), types.Permission{Name: "restricted", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"partial": types.ActionRead}}, Targets: map[string]types.Target{"pypi-remote": target}}}}); err != nil {
			t.Fatal(err)
		}
		requireRemote(
			t,
			f.do("GET", remoteRegistry+"simple/demo/", "partial", "Accept", pipAccept),
			404,
		)
		for _, accept := range []string{pipAccept, "text/html"} {
			res := f.do("GET", remoteRegistry+"simple/", "partial", "Accept", accept)
			requireRemote(t, res, 200)
			if strings.Contains(res.Body.String(), "demo") {
				t.Fatal("unreadable project disclosed", res.Body)
			}
		}
	}
	f := newRemoteFixture(t)
	f.project(t, "demo", "1.0", "2.0")
	f.setJSON(
		"/custom/simple/",
		map[string]any{
			"meta":     map[string]string{"api-version": "1.1"},
			"projects": []map[string]string{{"name": "demo"}},
		},
	)
	f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if q.Resource.Kind == source.Artifact &&
			q.Resource.Path == "demo/1.0/demo-1.0-py3-none-any.whl" {
			return source.ErrDenied
		}
		return nil
	}
	requireRemote(t, f.do("GET", remoteRegistry+"simple/", "partial"), 403)
}

func TestRemoteArchiveAndHTMLRejectionDoNotPoisonCache(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run(fmt.Sprintf("store=%v", store), func(t *testing.T) {
			f := newRemoteFixture(t)
			c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
			c.StoreArtifactsLocally = &store
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			filename := "demo-1.0.tar.gz"
			good := sdist(t, "demo", "1.0", metadata("demo", "1.0", ""))
			bad := append([]byte(nil), good...)
			bad[len(bad)-8] ^= 0xff
			// The malformed archive's advertised hash matches: archive validation must reject it.
			f.setJSON(
				"/custom/simple/demo/",
				map[string]any{
					"meta": map[string]string{"api-version": "1.1"},
					"name": "demo",
					"files": []map[string]any{
						{
							"filename": filename,
							"url":      "/files/" + filename,
							"hashes":   map[string]string{"sha256": sha256Hex(bad)},
						},
					},
				},
			)
			f.up.Set("/files/"+filename, fakeupstream.Resource{Body: bad})
			requireRemote(t, f.do("GET", remoteRegistry+"demo/1.0/"+filename, "reader"), 502)
			if _, err := f.db.Items().
				Get(t.Context(), "pypi-remote", true, "demo/1.0/"+filename); err == nil {
				t.Fatal("corrupt archive published")
			}
			// Metadata without a digest lets the corrected bytes prove immediate artifact retry.
			f.project(t, "clean", "1.0")
			f.up.Set(
				"/custom/simple/clean/",
				fakeupstream.Resource{
					Body: []byte(
						`<html><meta name="pypi:repository-version" content="2.0"></html>`,
					),
					Header: http.Header{"Content-Type": []string{"text/html"}},
				},
			)
			requireRemote(t, f.do("GET", remoteRegistry+"simple/clean/", "reader"), 502)
			f.project(t, "clean", "1.0")
			requireRemote(
				t,
				f.do("GET", remoteRegistry+"simple/clean/", "reader"),
				200,
				"clean-1.0",
			)
			// Use a distinct uncached project: keep its valid metadata unchanged between retries.
			clean := sdist(t, "retry", "1.0", metadata("retry", "1.0", ""))
			corrupt := append([]byte(nil), clean...)
			corrupt[len(corrupt)-8] ^= 0xff
			f.setJSON(
				"/custom/simple/retry/",
				map[string]any{
					"meta": map[string]string{"api-version": "1.1"},
					"name": "retry",
					"files": []map[string]any{
						{
							"filename": "retry-1.0.tar.gz",
							"url":      "/files/retry-1.0.tar.gz",
							"hashes":   map[string]string{},
						},
					},
				},
			)
			f.up.Set("/files/retry-1.0.tar.gz", fakeupstream.Resource{Body: corrupt})
			requireRemote(
				t,
				f.do("GET", remoteRegistry+"retry/1.0/retry-1.0.tar.gz", "reader"),
				502,
			)
			f.up.Set("/files/retry-1.0.tar.gz", fakeupstream.Resource{Body: clean})
			requireRemote(
				t,
				f.do("GET", remoteRegistry+"retry/1.0/retry-1.0.tar.gz", "reader"),
				200,
			)
		})
	}
}

func TestRemoteWarehouseBinarySourceArchive(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprintf("stored=%v", stored), func(t *testing.T) {
			f := newRemoteFixture(t)
			c, _ := f.db.Repos().Get(t.Context(), "pypi-remote")
			c.StoreArtifactsLocally = &stored
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			good := sdist(t, "demo", "1.0", metadata("demo", "1.0", ""))
			bad := append([]byte(nil), good...)
			bad[len(bad)-8] ^= 0xff
			f.setJSON(
				"/custom/simple/demo/",
				map[string]any{
					"meta": map[string]string{"api-version": "1.1"},
					"name": "demo",
					"files": []map[string]any{
						{
							"filename": "demo-1.0.tar.gz",
							"url":      "/files/demo-1.0.tar.gz",
							"hashes":   map[string]string{},
						},
					},
				},
			)
			set := func(body []byte) {
				f.up.Set(
					"/files/demo-1.0.tar.gz",
					fakeupstream.Resource{
						Body:   body,
						Header: http.Header{"Content-Type": {"binary/octet-stream"}},
					},
				)
			}
			set(bad)
			target := remoteRegistry + "demo/1.0/demo-1.0.tar.gz"
			requireRemote(t, f.do("GET", target, "reader"), 502)
			if _, err := f.db.Items().
				Get(t.Context(), "pypi-remote", true, "demo/1.0/demo-1.0.tar.gz"); err == nil {
				t.Fatal("corrupt archive cached")
			}
			set(good)
			result := f.do("GET", target, "reader")
			requireRemote(t, result, 200)
			if result.Body.String() != string(good) {
				t.Fatal("original archive changed")
			}
			if !stored {
				return
			}
			offline := true
			c.Offline = &offline
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			f.up.Close()
			result = f.do("GET", target, "reader")
			requireRemote(t, result, 200)
			if result.Body.String() != string(good) {
				t.Fatal("offline original archive changed")
			}
		})
	}
}
