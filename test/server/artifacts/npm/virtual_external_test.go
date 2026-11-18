package npm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func externalNPMArchive(t *testing.T, name, version, marker string, deps map[string]any) []byte {
	t.Helper()
	fields := map[string]any{"name": name, "version": version, "description": marker}
	for key, v := range deps {
		fields[key] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"package/package.json", raw}, {"package/marker.txt", []byte(marker)}} {
		if err = tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func externalNPMWorld(t *testing.T, versioned bool, mutate ...func(*config.Config)) *harness.RemoteWorld {
	t.Helper()
	w := virtualNPMWorld(t, npmfmt.Settings{ExternalDependenciesVersioning: versioned}, mutate...)
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) {
		c.ExternalDependenciesEnabled = new(true)
		c.ExternalDependenciesRemoteRepo = "remote"
		c.ExternalDependenciesPatterns = []string{w.Upstream.URL + "/**", "https://github.com/**"}
	})
	return w
}

func externalNPMParent(t *testing.T, w *harness.RemoteWorld, parent, version, target string) {
	t.Helper()
	data := externalNPMArchive(
		t,
		parent,
		version,
		"parent",
		map[string]any{
			"dependencies":         map[string]string{"dep": target},
			"devDependencies":      map[string]string{"dev": target},
			"optionalDependencies": map[string]string{"opt": target},
			"peerDependencies":     map[string]string{"peer": target},
		},
	)
	harness.WantStatus(t, w.Request("PUT", "/artifactory/first/"+npmfmt.CanonicalPath(parent, version), "admin", string(data)), 201)
}

func externalNPMTag(t *testing.T, w *harness.RemoteWorld, parent, version string) string {
	t.Helper()
	rec := w.Request("GET", npmBase+"virtual/"+parent+"/"+version, "admin", "")
	harness.WantStatus(t, rec, 200)
	var doc struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Dependencies["dep"]
}

func externalNPMTarball(t *testing.T, w *harness.RemoteWorld, spec string) (string, []byte) {
	t.Helper()
	rec := w.Request("GET", npmBase+"virtual/dep/"+spec, "admin", "")
	harness.WantStatus(t, rec, 200)
	var doc struct {
		Dist struct {
			Tarball   string `json:"tarball"`
			Shasum    string `json:"shasum"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(doc.Dist.Tarball)
	if err != nil || doc.Dist.Shasum == "" || !strings.HasPrefix(doc.Dist.Integrity, "sha512-") {
		t.Fatal(doc, err)
	}
	path := u.RequestURI()
	rec = w.Request("GET", path, "admin", "")
	harness.WantStatus(t, rec, 200)
	return path, rec.Body.Bytes()
}

func TestAccNPMVirtualExternalRewriteAndCache(t *testing.T) {
	w := externalNPMWorld(t, false)
	target := w.Upstream.URL + "/dep.tgz"
	data := externalNPMArchive(t, "dep", "2.0.0", "external", nil)
	w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Body: data})
	externalNPMParent(t, w, "parent", "1.0.0", target)
	for _, short := range []bool{false, true} {
		accept := "application/json"
		if short {
			accept = "application/vnd.npm.install-v1+json"
		}
		rec := w.Request("GET", npmBase+"virtual/parent", "admin", "", "Accept", accept)
		harness.WantStatus(t, rec, 200)
		if strings.Count(rec.Body.String(), "dep-dep.tgz-art-external") != 1 || strings.Contains(rec.Body.String(), `"dependencies":{"dep":"`+target) {
			t.Fatal(rec.Body.String())
		}
		for _, field := range []string{"devDependencies", "optionalDependencies"} {
			if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
				t.Fatal(field, rec.Body.String())
			}
		}
	}
	spec := externalNPMTag(t, w, "parent", "1.0.0")
	if spec != "dep-dep.tgz-art-external" {
		t.Fatal(spec)
	}
	first := w.Request("GET", npmBase+"virtual/parent", "admin", "")
	conditional := w.Request("GET", npmBase+"virtual/parent", "admin", "", "If-None-Match", first.Header().Get("ETag"))
	harness.WantStatus(t, conditional, 304)
	virtualNPMHead(t, w, npmBase+"virtual/parent")
	path, got := externalNPMTarball(t, w, spec)
	if !bytes.Equal(got, data) {
		t.Fatal("wrong external bytes")
	}
	if _, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, "_external/dep-dep.tgz"); err != nil {
		t.Fatal("external cache", err)
	}
	before := w.Upstream.Count("GET", "/dep.tgz")
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ArtifactoryRequestsCanRetrieveRemoteArtifacts = new(false) })
	origin := w.Request("GET", path, "admin", "", "X-Artifactory-Originated", "another-node")
	harness.WantStatus(t, origin, 200)
	if !bytes.Equal(origin.Body.Bytes(), data) || w.Upstream.Count("GET", "/dep.tgz") != before {
		t.Fatal("warm origin restriction bypassed cache")
	}
	_, got = externalNPMTarball(t, w, spec)
	if !bytes.Equal(got, data) || w.Upstream.Count("GET", "/dep.tgz") != before {
		t.Fatal("warm external origin contact")
	}
	w.Repo.Offline = new(true)
	w.Update()
	rec := w.Request("GET", path, "reader", "")
	harness.WantStatus(t, rec, 200)
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatal("offline bytes")
	}
	if w.Upstream.Count("GET", "/dep.tgz") != before {
		t.Fatal("offline contact")
	}
}

func TestAccNPMVirtualExternalDisabledAndInvalidCache(t *testing.T) {
	for _, tc := range []string{"disabled", "unset", "absent", "local", "nested", "patterns", "credentials"} {
		t.Run(tc, func(t *testing.T) {
			w := externalNPMWorld(t, false)
			target := w.Upstream.URL + "/dep.tgz"
			externalNPMParent(t, w, "parent", "1.0.0", target)
			virtualNPMConfig(t, w, "virtual", func(c *repo.Config) {
				switch tc {
				case "disabled":
					c.ExternalDependenciesEnabled = new(false)
				case "unset":
					c.ExternalDependenciesRemoteRepo = ""
				case "absent":
					c.ExternalDependenciesRemoteRepo = "absent"
					c.Repositories = append(c.Repositories, "absent")
				case "local":
					c.ExternalDependenciesRemoteRepo = "first"
				case "nested":
					c.ExternalDependenciesRemoteRepo = "inner"
				case "patterns":
					c.ExternalDependenciesPatterns = nil
				case "credentials":
				}
			})
			if tc == "credentials" {
				w.Repo.AllowAnyHostAuth = new(true)
				w.Update()
			}
			if got := externalNPMTag(t, w, "parent", "1.0.0"); got != target {
				t.Fatal(got, target)
			}
			if w.Upstream.Count("GET", "/dep.tgz") != 0 {
				t.Fatal("disabled external fetched")
			}
		})
	}
}

func TestAccNPMVirtualExternalURLCollision(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		t.Run(string(rune('0'+boolInt(versioned))), func(t *testing.T) {
			w := externalNPMWorld(t, versioned)
			first := externalNPMArchive(t, "dep", "1.0.0", "first", nil)
			second := externalNPMArchive(t, "dep", "2.0.0", "second", nil)
			w.Upstream.Set("/1.0.0/dep.tgz", fakeupstream.Resource{Body: first})
			w.Upstream.Set("/2.0.0/dep.tgz", fakeupstream.Resource{Body: second})
			externalNPMParent(t, w, "parent-one", "1.0.0", w.Upstream.URL+"/1.0.0/dep.tgz")
			one := externalNPMTag(t, w, "parent-one", "1.0.0")
			pathOne, got := externalNPMTarball(t, w, one)
			if !bytes.Equal(got, first) {
				t.Fatal("first bytes")
			}
			externalNPMParent(t, w, "parent-two", "1.0.0", w.Upstream.URL+"/2.0.0/dep.tgz")
			two := externalNPMTag(t, w, "parent-two", "1.0.0")
			if one == two {
				t.Fatal("two URLs share one tag", one)
			}
			pathTwo, got := externalNPMTarball(t, w, two)
			if !bytes.Equal(got, second) {
				t.Fatal("second bytes")
			}
			// Previously selected tarball URLs remain bound when another render/cache fill occurs.
			rec := w.Request("GET", pathOne, "admin", "")
			harness.WantStatus(t, rec, 200)
			if !bytes.Equal(rec.Body.Bytes(), first) {
				t.Fatal("old URL replaced by other source")
			}
			rec = w.Request("GET", pathTwo, "admin", "")
			harness.WantStatus(t, rec, 200)
			if !bytes.Equal(rec.Body.Bytes(), second) {
				t.Fatal("new URL replaced by other source")
			}
			if versioned && !strings.Contains(pathOne, "1.0.0") {
				t.Fatal("versioned cache filename", pathOne)
			}
			w.Repo.Offline = new(true)
			w.Update()
			rec = w.Request("GET", pathOne, "admin", "")
			if !versioned && rec.Code == 200 {
				t.Fatal("offline collision served another source", rec.Body.String())
			}
			if versioned {
				harness.WantStatus(t, rec, 200)
			}
		})
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestAccNPMVirtualExternalInvalidArchive(t *testing.T) {
	for _, tc := range []string{"missing", "name"} {
		t.Run(tc, func(t *testing.T) {
			w := externalNPMWorld(t, false)
			data := []byte("not an archive")
			if tc == "name" {
				data = externalNPMArchive(t, "other", "1.0.0", "wrong", nil)
			}
			w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Body: data})
			externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
			_ = externalNPMTag(t, w, "parent", "1.0.0")
			rec := w.Request(http.MethodGet, npmBase+"virtual/dep", "admin", "")
			harness.WantStatus(t, rec, 502)
		})
	}
}

func TestAccNPMVirtualExternalMemberWinnerAndSourcePolicy(t *testing.T) {
	for _, mode := range []string{"member-winner", "offline-cold", "blackout", "remote-pattern", "cache-only", "no-store", "read-only-cold", "denied", "late-virtual"} {
		t.Run(mode, func(t *testing.T) {
			w := externalNPMWorld(t, false)
			data := externalNPMArchive(t, "dep", "2.0.0", "external", nil)
			w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Body: data})
			externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
			spec := externalNPMTag(t, w, "parent", "1.0.0")
			user := "admin"
			want := 200
			switch mode {
			case "member-winner":
				virtualNPMLocal(t, w, "first", "dep", "2.0.0", "member-winner")
			case "offline-cold":
				w.Repo.Offline = new(true)
				w.Update()
				want = 404
			case "blackout":
				w.Repo.BlackedOut = new(true)
				w.Update()
				want = 404
			case "remote-pattern":
				w.Repo.ExcludesPattern = new("_external/**")
				w.Update()
				want = 404
			case "cache-only":
				virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ArtifactoryRequestsCanRetrieveRemoteArtifacts = new(false) })
				want = 404
			case "no-store":
				w.Repo.StoreArtifactsLocally = new(false)
				w.Update()
			case "read-only-cold":
				if err := w.Runtime.Store.Permissions().
					Update(t.Context(), accesstest.Permission{Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}}, Targets: map[string]accesstest.Target{"remote": {}}}}}); err != nil {
					t.Fatal(err)
				}
				user = "reader"
				want = 404
			case "denied":
				user = "denied"
				want = 403
			case "late-virtual":
				svc := w.Runtime.Artifacts.RemoteSources()
				original := svc.HTTP
				svc.HTTP = externalAfterRead{Backend: original, after: func(q source.Request) {
					if q.Repo.Key == "remote" && strings.HasPrefix(q.Resource.Path, "_external/") {
						virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExternalDependenciesEnabled = new(false) })
					}
				}}
				want = 503
			}
			headers := []string{}
			if mode == "cache-only" {
				headers = []string{"X-Artifactory-Originated", "another-node"}
			}
			rec := w.Request("GET", npmBase+"virtual/dep/"+spec, user, "", headers...)
			harness.WantStatus(t, rec, want)
			if want != 200 && strings.Contains(rec.Body.String(), "external-bytes") {
				t.Fatal("refused external disclosure")
			}
			if mode == "member-winner" && (!strings.Contains(rec.Body.String(), "member-winner") || strings.Contains(rec.Body.String(), "_external/")) {
				t.Fatal("member version lost", rec.Body.String())
			}
			if mode == "no-store" {
				if _, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, "_external/dep-dep.tgz"); err == nil {
					t.Fatal("no-store external persisted")
				}
			}
		})
	}
}

func TestAccNPMVirtualExternalMappingsFaultIs500(t *testing.T) {
	w := externalNPMWorld(t, false)
	externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
	if _, err := w.Runtime.Store.Exec(t.Context(), "inject npm external mapping statement fault", "ALTER TABLE npm_externals RENAME TO npm_externals_hidden"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := w.Runtime.Store.Exec(t.Context(), "restore npm external mapping table", "ALTER TABLE npm_externals_hidden RENAME TO npm_externals"); err != nil {
			t.Error(err)
		}
	}()
	rec := w.Request("GET", npmBase+"virtual/parent/1.0.0", "admin", "")
	harness.WantStatus(t, rec, 500)
	if w.Upstream.Count("GET", "/dep.tgz") != 0 {
		t.Fatal("fault became external fetch")
	}
}

type externalAfterRead struct {
	source.Backend
	after func(source.Request)
}

func (b externalAfterRead) Read(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
	result, err := b.Backend.Read(ctx, q, p)
	if err == nil {
		b.after(q)
	}
	return result, err
}

func TestAccNPMVirtualExternalPreservesWinningSignaturesAndR8(t *testing.T) {
	w := externalNPMWorld(t, false)
	target := w.Upstream.URL + "/external.tgz"
	virtualNPMRemote(t, w, "signed-parent", map[string]string{"1.0.0": "keep-signature"}, map[string]string{"latest": "1.0.0"}, func(doc map[string]any) {
		version := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)
		version["dependencies"] = map[string]string{"dep": target}
	})
	for _, path := range []string{"signed-parent", "signed-parent/1.0.0"} {
		for _, accept := range []string{"application/json", "application/vnd.npm.install-v1+json"} {
			rec := w.Request("GET", npmBase+"virtual/"+path, "admin", "", "Accept", accept)
			harness.WantStatus(t, rec, 200)
			if !strings.Contains(rec.Body.String(), `"sig":"keep-signature"`) || !strings.Contains(rec.Body.String(), "dep-external.tgz-art-external") {
				t.Fatal(rec.Body.String())
			}
		}
	}
	direct := w.Request("GET", npmBase+"remote/signed-parent/1.0.0", "admin", "")
	harness.WantStatus(t, direct, 200)
	if !strings.Contains(direct.Body.String(), target) || strings.Contains(direct.Body.String(), "art-external") {
		t.Fatal("cached member mutated", direct.Body.String())
	}
	externalNPMParent(t, w, "local-parent", "1.0.0", target)
	first := externalNPMTag(t, w, "local-parent", "1.0.0")
	externalNPMParent(t, w, "local-parent", "1.0.0", w.Upstream.URL+"/replacement.tgz")
	if got := externalNPMTag(t, w, "local-parent", "1.0.0"); got != first {
		t.Fatal("registration/write invalidated R8 model", got, first)
	}
}

func TestAccNPMVirtualExternalRestartRetainsTagsAndCache(t *testing.T) {
	cfg := harness.AppConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	if dsn := os.Getenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN"); dsn != "" {
		cfg.Database = config.Database{Driver: "postgres", DSN: sqltest.ScratchDSN(t, dsn)}
	}
	open := func() *app.Runtime {
		t.Helper()
		rt, err := app.OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), harness.AppOptions())
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	rt := open()
	defer func() {
		if rt != nil {
			_ = rt.Close(t.Context())
		}
	}()
	up := fakeupstream.New(t, fakeupstream.Options{})
	w := &harness.RemoteWorld{T: t, Runtime: rt, Upstream: up, Repo: *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.NPM, URL: up.URL})}
	for _, c := range []repo.Config{w.Repo, *repo.Defaults(&repo.Config{Key: "first", Class: repo.Local, PackageType: repo.NPM}), *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"first", "remote"}, ExternalDependenciesEnabled: new(true), ExternalDependenciesRemoteRepo: "remote", ExternalDependenciesPatterns: []string{up.URL + "/**"}})} {
		if err := rt.Store.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	mount := func() {
		w.H = harness.Router(
			harness.Config{
				Store:         rt.Store,
				Blobs:         rt.Blobs,
				Auth:          rt.Auth,
				Tokens:        rt.Tokens,
				Keys:          rt.Keys,
				RemoteSources: rt.Artifacts.RemoteSources(),
				RemoteHTTP:    rt.Artifacts.RemoteHTTP(),
				VirtualCache:  rt.Artifacts.VirtualCache(),
			},
		)
	}
	mount()
	data := externalNPMArchive(t, "dep", "2.0.0", "survives restart", nil)
	up.Set("/dep.tgz", fakeupstream.Resource{Body: data})
	externalNPMParent(t, w, "parent", "1.0.0", up.URL+"/dep.tgz")
	spec := externalNPMTag(t, w, "parent", "1.0.0")
	path, got := externalNPMTarball(t, w, spec)
	if !bytes.Equal(got, data) {
		t.Fatal("initial bytes")
	}
	w.Repo.Offline = new(true)
	w.Update()
	before := up.Count("GET", "/dep.tgz")
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	rt = nil
	rt = open()
	w.Runtime = rt
	mount()
	// No parent render rebuilds mappings after restart.
	rec := w.Request("GET", npmBase+"virtual/dep/"+spec, "admin", "")
	harness.WantStatus(t, rec, 200)
	rec = w.Request("GET", path, "admin", "")
	harness.WantStatus(t, rec, 200)
	if !bytes.Equal(rec.Body.Bytes(), data) || up.Count("GET", "/dep.tgz") != before {
		t.Fatal("restart lost identity or fetched offline")
	}
}

func TestAccNPMVirtualExternalLateSourceAndServedFaults(t *testing.T) {
	for _, mode := range []string{"revision", "served-body", "admission-body", "virtual-before-publication", "permission"} {
		t.Run(mode, func(t *testing.T) {
			w := externalNPMWorld(t, false)
			data := externalNPMArchive(t, "dep", "2.0.0", "no disclosure", nil)
			w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Body: data})
			externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
			spec := externalNPMTag(t, w, "parent", "1.0.0")
			if mode == "served-body" {
				w.Repo.StoreArtifactsLocally = new(false)
				w.Update()
			}
			svc := w.Runtime.Artifacts.RemoteSources()
			original := svc.HTTP
			svc.HTTP = externalBackendFunc(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				if mode == "virtual-before-publication" && strings.HasPrefix(q.Resource.Path, "_external/") {
					validate := q.Resource.Validate
					q.Resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
						virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExternalDependenciesEnabled = new(false) })
						return validate(ctx, body, size)
					}
				}
				if mode == "admission-body" && strings.HasPrefix(q.Resource.Path, "_external/") {
					validate := q.Resource.Validate
					q.Resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
						err := validate(ctx, externalBrokenSeeker{body}, size)
						t.Logf("admission callback error=%T %v", err, err)
						return err
					}
				}
				result, err := original.Read(ctx, q, p)
				if mode == "admission-body" {
					t.Logf("backend %s error=%T %v", q.Resource.Path, err, err)
				}
				if err == nil && strings.HasPrefix(q.Resource.Path, "_external/") {
					switch mode {
					case "revision":
						w.Repo.URL += "/new-source"
						w.Update()
					case "served-body":
						if result.Stream == nil {
							t.Fatal("no-store has no spool")
						}
						result.Stream.Body = externalBrokenBody{result.Stream.Body}
					case "permission":
						if err := w.Runtime.Store.Permissions().
							Update(ctx, accesstest.Permission{Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionWrite}}, Targets: map[string]accesstest.Target{"remote": {}}}}}); err != nil {
							t.Fatal(err)
						}
					}
				}
				return result, err
			})
			user := "admin"
			want := 503
			if mode == "served-body" || mode == "admission-body" {
				want = 500
			}
			if mode == "permission" {
				user = "reader"
				want = 403
			}
			rec := w.Request("GET", npmBase+"virtual/dep/"+spec, user, "")
			harness.WantStatus(t, rec, want)
			if mode == "virtual-before-publication" {
				if _, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, "_external/dep-dep.tgz"); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("superseded virtual published external archive", err)
				}
			}
			if strings.Contains(rec.Body.String(), "no disclosure") {
				t.Fatal("late fence leaked manifest", rec.Body.String())
			}
		})
	}
}

type externalBackendFunc func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f externalBackendFunc) Read(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
	return f(ctx, q, p)
}

type externalBrokenBody struct{ io.ReadCloser }

func (externalBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type externalBrokenSeeker struct{ io.ReadSeeker }

func (externalBrokenSeeker) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAccNPMVirtualExternalVersionlessURLFallback(t *testing.T) {
	w := externalNPMWorld(t, true)
	data := externalNPMArchive(t, "dep", "2.0.0", "URL has no version hint", nil)
	w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Body: data})
	externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
	spec := externalNPMTag(t, w, "parent", "1.0.0")
	if spec != "dep-dep.tgz-art-external" {
		t.Fatal("versionless naming fallback", spec)
	}
	_, got := externalNPMTarball(t, w, spec)
	if !bytes.Equal(got, data) {
		t.Fatal("versionless external bytes")
	}
	if _, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, "_external/dep-dep.tgz"); err != nil {
		t.Fatal(err)
	}
}

func TestAccNPMVirtualExternalReadPrecedesVirtualFilePolicy(t *testing.T) {
	w := externalNPMWorld(t, false)
	data := externalNPMArchive(t, "dep", "2.0.0", "read-order", nil)
	w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Body: data})
	externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
	spec := externalNPMTag(t, w, "parent", "1.0.0")
	path, _ := externalNPMTarball(t, w, spec)
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("_external/**") })
	before := w.Upstream.Count("GET", "/dep.tgz")
	harness.WantStatus(t, w.Request("GET", path, "denied", ""), 403)
	harness.WantStatus(t, w.Request("GET", path, "admin", ""), 404)
	if w.Upstream.Count("GET", "/dep.tgz") != before {
		t.Fatal("refused file contacted origin")
	}
}

func TestAccNPMVirtualExternalRedirectAuthority(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "allowed"}[allowed], func(t *testing.T) {
			cdn := fakeupstream.New(t, fakeupstream.Options{})
			defer cdn.Close()
			w := externalNPMWorld(t, false, func(c *config.Config) { c.Remote.AllowedPrivateOrigins = []string{cdn.URL} })
			data := externalNPMArchive(t, "dep", "2.0.0", "redirected", nil)
			cdn.Set("/archive.tgz", fakeupstream.Resource{Body: data})
			w.Upstream.Set("/dep.tgz", fakeupstream.Resource{Redirect: cdn.URL + "/archive.tgz"})
			w.Repo.Username = "cache-user"
			w.Repo.Password = "cache-secret"
			w.Repo.CustomHTTPHeaders = []repo.CustomHTTPHeader{{Name: "X-External-Secret", Value: "cache-header"}}
			w.Update()
			if allowed {
				virtualNPMConfig(t, w, "virtual", func(c *repo.Config) {
					c.ExternalDependenciesPatterns = append(c.ExternalDependenciesPatterns, cdn.URL+"/**")
				})
			}
			externalNPMParent(t, w, "parent", "1.0.0", w.Upstream.URL+"/dep.tgz")
			spec := externalNPMTag(t, w, "parent", "1.0.0")
			rec := w.Request("GET", npmBase+"virtual/dep/"+spec, "admin", "")
			if !allowed {
				harness.WantStatus(t, rec, 502)
				if len(cdn.Requests()) != 0 {
					t.Fatal("refused redirect contacted destination")
				}
				return
			}
			harness.WantStatus(t, rec, 200)
			for _, request := range cdn.Requests() {
				for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-External-Secret"} {
					if request.Header.Get(header) != "" {
						t.Fatalf("redirect leaked %s", header)
					}
				}
			}
			_, got := externalNPMTarball(t, w, spec)
			if !bytes.Equal(got, data) {
				t.Fatal("redirected bytes changed")
			}
		})
	}
}
