package bowerhttp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

func TestAccBowerRewriteGitSourcesAndTransitiveArchives(t *testing.T) {
	for _, value := range []string{"art://team/dep#2.0.0", "team/dep#2.0.0", "{upstream}/git/team/dep.git#master", "art://team/dep#^2.0.0", "team/dep"} {
		t.Run(value, func(t *testing.T) {
			f, base := dependencyFixture(t)
			c, err := f.db.Repos().Get(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			manifest := fmt.Sprintf(
				`{"name":"hello","version":"1.0.0","dependencies":{"dep":%q,"ordinary":"^1.0.0"}}`,
				strings.ReplaceAll(value, "{upstream}", f.up.URL),
			)
			f.deploy(c, "hello", "1.0.0", "bower.json", manifest)
			archive := bowerArchive(
				t,
				[]archiveEntry{
					{
						"dep/bower.json",
						`{"name":"dep","dependencies":{"child":"` + f.up.URL + `/child.tgz"}}`,
						0,
					},
				},
			)
			f.up.Set(
				"/git/team/dep/archive/refs/tags/2.0.0.tar.gz",
				fakeupstream.Resource{Body: archive},
			)
			f.up.Set(
				"/git/team/dep/archive/refs/heads/master.tar.gz",
				fakeupstream.Resource{Body: archive},
			)
			f.up.Set(
				"/child.tgz",
				fakeupstream.Resource{
					Body: bowerArchive(
						t,
						[]archiveEntry{{"child/bower.json", `{"name":"child"}`, 0}},
					),
				},
			)
			rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
			if rec.Code != 200 {
				t.Fatalf("rewrite: %d %s", rec.Code, rec.Body)
			}
			files, err := dependencyFiles(rec.Body.Bytes(), "hello")
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if err := json.Unmarshal(files["bower.json"], &m); err != nil {
				t.Fatal(err)
			}
			if m.Dependencies["ordinary"] != "^1.0.0" ||
				!strings.HasPrefix(m.Dependencies["dep"], "art://") {
				t.Fatalf("manifest: %s", files["bower.json"])
			}
			if source, tag, ok := strings.Cut(
				m.Dependencies["dep"],
				"#",
			); ok &&
				strings.HasPrefix(tag, dependencyTargetPrefix) {
				rec = f.get(
					base+"binaries/"+strings.TrimPrefix(
						source,
						"art://",
					)+".git/"+url.PathEscape(
						tag,
					),
					"admin",
				)
				if rec.Code != 200 {
					t.Fatalf("transitive: %d %s", rec.Code, rec.Body)
				}
				files, err := dependencyFiles(rec.Body.Bytes(), "dep")
				if err != nil ||
					!bytes.Contains(files["bower.json"], []byte("art://external/child")) {
					t.Fatalf("transitive manifest: %s %v", files["bower.json"], err)
				}
			}
		})
	}
}

func TestAccBowerRewriteRejectsUnsafeSourceAndCacheConfiguration(t *testing.T) {
	for _, value := range []string{"file:///etc/passwd", "https://user:secret@example.org/dep.zip", "art://team/nested/dep#1.0.0", "../dep#1.0.0", "https://other.invalid/team/dep.git#1.0.0", "{upstream}/external/dep.tgz#unknown", "art://team/dep#../main", "art://team/dep#^1.0.0/secret"} {
		t.Run(value, func(t *testing.T) {
			f, base := dependencyFixture(t)
			c, err := f.db.Repos().Get(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			f.deploy(
				c,
				"hello",
				"1.0.0",
				"bower.json",
				fmt.Sprintf(
					`{"name":"hello","version":"1.0.0","dependencies":{"dep":%q}}`,
					strings.ReplaceAll(value, "{upstream}", f.up.URL),
				),
			)
			rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
			if rec.Code == 200 {
				t.Fatal("unsafe dependency delivered")
			}
		})
	}
	for _, change := range []func(*repo.Config){
		func(c *repo.Config) {
			c.ExternalDependenciesRemoteRepo = "missing"
			c.Repositories = append(c.Repositories, "missing")
		},
		func(c *repo.Config) { c.ExternalDependenciesRemoteRepo = "local" },
		func(c *repo.Config) { c.ExternalDependenciesPatterns = nil },
	} {
		f, base := dependencyFixture(t)
		c, err := f.db.Repos().Get(t.Context(), "virtual")
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin"); rec.Code == 200 {
			t.Fatal("unsafe rewrite configuration delivered")
		}
	}
	f, base := dependencyFixture(t)
	f.configure(func(c *repo.Config) { c.AllowAnyHostAuth = new(true) })
	if rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin"); rec.Code == 200 {
		t.Fatal("lenient credentials admitted")
	}
	for _, path := range []string{"binaries/wrong/dep.git/" + url.PathEscape(dependencyTarget(f.up.URL+"/external/dep.tgz")), "binaries/external/dep.git/" + url.PathEscape(dependencyTargetPrefix+"bad.tar.gz"), "refs/team/../dep", "refs/team/dep?unexpected=true"} {
		if rec := f.get(base+path, "admin"); rec.Code == 200 {
			t.Fatalf("unsafe endpoint admitted: %s", path)
		}
	}
}

func TestAccBowerRewriteRechecksCurrentVirtualAuthority(t *testing.T) {
	for _, stage := range []string{"before read", "after read", "delivery"} {
		t.Run(stage, func(t *testing.T) {
			f, base := dependencyFixture(t)
			calls := 0
			f.sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if q.Repo.Key != "virtual" {
					return nil
				}
				calls++
				limit := map[string]int{"before read": 1, "after read": 2, "delivery": 3}[stage]
				if calls >= limit {
					return source.ErrDenied
				}
				return nil
			}
			rec := f.get(
				base+"binaries/external/dep.git/"+url.PathEscape(
					dependencyTarget(f.up.URL+"/external/dep.tgz"),
				),
				"admin",
			)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s revocation = %d (%d checks): %s", stage, rec.Code, calls, rec.Body)
			}
		})
	}
	f, base := dependencyFixture(t)
	f.sources.Revalidate = func(ctx context.Context, q source.Request, _ source.Permit) error {
		if q.Repo.Key != "virtual" {
			return nil
		}
		c, err := f.db.Repos().Get(ctx, "virtual")
		if err != nil {
			return err
		}
		if err := f.db.Repos().Delete(ctx, c.Key); err != nil {
			return err
		}
		return f.db.Repos().Create(ctx, c)
	}
	if rec := f.get(
		base+"binaries/external/dep.git/"+url.PathEscape(
			dependencyTarget(f.up.URL+"/external/dep.tgz"),
		),
		"admin",
	); rec.Code == 200 {
		t.Fatal("recreated virtual inherited in-flight authority")
	}
}

func TestUnitBowerDependencyArchiveFaults(t *testing.T) {
	for _, entries := range [][]archiveEntry{
		{{"dep/bower.json", `{"name":"dep"}`, 0}, {"dep/link", "", tar.TypeSymlink}},
		{{"first/bower.json", `{"name":"dep"}`, 0}, {"second/bower.json", `{"name":"dep"}`, 0}},
		{{"dep/index.js", "no manifest", 0}},
		{{"dep/", "", tar.TypeDir}, {"dep/bower.json", `{"name":"dep"}`, 0}},
	} {
		_, err := dependencyFiles(bowerArchive(t, entries), "dep")
		if entries[0].kind != tar.TypeDir && err == nil {
			t.Fatal("unsafe archive admitted")
		}
	}
	for _, data := range [][]byte{nil, []byte("not gzip"), []byte("PK\x03\x04bad"), bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"dep"}`, 0}})[:15]} {
		if _, err := dependencyFiles(data, "dep"); err == nil {
			t.Fatal("corrupt archive admitted")
		}
	}
	var output bytes.Buffer
	zw := zip.NewWriter(&output)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(0o777 | 1<<27)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dependencyFiles(output.Bytes(), "dep"); err == nil {
		t.Fatal("ZIP link accepted")
	}
}

func TestAccBowerRegistrationBodies(t *testing.T) {
	f := newRemoteFixture(t, true)
	for _, body := range []string{"name=hello&url=x&extra=yes", "name=hello&name=other&url=x", "bad%zz", "name=x&url=" + strings.Repeat("a", 8193)} {
		request := httptest.NewRequest(
			http.MethodPost,
			"/artifactory/api/bower/bower-remote/packages",
			strings.NewReader(body),
		)
		request.Header.Set("X-User", "admin")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, request)
		if rec.Code != 400 {
			t.Fatalf("registration body: %d", rec.Code)
		}
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/artifactory/api/bower/bower-remote/packages",
		strings.NewReader(`{"name":"hello"}`),
	)
	request.Header.Set("X-User", "admin")
	request.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, request)
	if rec.Code != 400 {
		t.Fatalf("registration content type: %d", rec.Code)
	}
}

func TestAccBowerRewrittenLocalFilesRetainCurrentMemberPermissions(t *testing.T) {
	f, base := dependencyFixture(t)
	c, err := f.db.Repos().Get(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	f.deploy(c, "hello", "1.0.0", "index.js", "protected parent bytes")
	checks := 0
	f.sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if q.Repo.Key == "local" && q.Resource.Path == "hello/1.0.0/index.js" {
			checks++
			return source.ErrDenied
		}
		return nil
	}
	rec := f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
	if rec.Code != http.StatusForbidden || checks != 1 {
		t.Fatalf("member file revocation: %d, %d checks: %s", rec.Code, checks, rec.Body)
	}
}

func TestAccBowerExternalRedirectsNeverBorrowOriginCredentials(t *testing.T) {
	f, base := dependencyFixture(t)
	target := fakeupstream.New(t, fakeupstream.Options{})
	archive := bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"dep"}`, 0}})
	target.Set("/dep.tgz", fakeupstream.Resource{Body: archive, ETag: `"upstream-only"`})
	f.up.Set("/redirect.tgz", fakeupstream.Resource{Redirect: target.URL + "/dep.tgz"})
	f.configure(func(c *repo.Config) { c.Username = "origin-user"; c.Password = "origin-secret" })
	virtual, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	virtual.ExternalDependenciesPatterns = append(
		virtual.ExternalDependenciesPatterns,
		target.URL+"/**",
	)
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	resolver, err := upstream.NewResolver(
		f.db,
		nil,
		upstream.ResolverOptions{
			RevisionKey:           []byte(strings.Repeat("k", 32)),
			AllowedPrivateOrigins: []string{target.URL},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.policy.Options = resolver.Options
	for _, raw := range []string{target.URL + "/dep.tgz", f.up.URL + "/redirect.tgz"} {
		rec := f.get(
			base+"binaries/external/dep.git/"+url.PathEscape(dependencyTarget(raw)),
			"admin",
		)
		if rec.Code != 200 {
			t.Fatalf("external redirect: %d %s", rec.Code, rec.Body)
		}
		if rec.Header().Get("ETag") != "" || rec.Header().Get("X-Checksum-Sha256") != "" {
			t.Fatal("rewritten bytes retained upstream validators")
		}
	}
	for _, request := range target.Requests() {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatal("origin credentials leaked to dependency target")
		}
	}
	virtual.ExternalDependenciesPatterns = []string{f.up.URL + "/**"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	f.up.Set("/disallowed-redirect.tgz", fakeupstream.Resource{Redirect: target.URL + "/dep.tgz"})
	before := len(target.Requests())
	rec := f.get(
		base+"binaries/external/dep.git/"+url.PathEscape(
			dependencyTarget(f.up.URL+"/disallowed-redirect.tgz"),
		),
		"admin",
	)
	if rec.Code == 200 || len(target.Requests()) != before {
		t.Fatal("redirect escaped virtual allowlist")
	}
	virtual.ExternalDependenciesPatterns = []string{"**"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	rec = f.get(
		base+"binaries/external/dep.git/"+url.PathEscape(
			dependencyTarget("http://169.254.169.254/latest/meta-data/"),
		),
		"admin",
	)
	if rec.Code == 200 {
		t.Fatal("broad dependency pattern bypassed link-local destination policy")
	}
}
