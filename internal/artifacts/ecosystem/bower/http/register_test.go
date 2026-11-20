package bowerhttp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccBowerRegistrationRechecksOriginalAndSelectedWriteAuthority(t *testing.T) {
	for _, denied := range []string{"", "bower-virtual", "bower-remote"} {
		t.Run("denied="+denied, func(t *testing.T) {
			f := newRemoteFixture(t, true)
			f.configure(func(c *repo.Config) {
				c.URL, c.VCSGitProvider = f.up.URL+"/stash", repo.Stash
			})
			f.repo("bower-virtual", repo.Virtual, "bower-remote")
			current := false
			f.currentAccess = func(ctx context.Context) (kernel.Principal, api.Decider, error) {
				if !f.db.InTx(ctx) {
					t.Fatal("current Bower authority is outside publication")
				}
				current = true
				return kernel.Principal{Name: "admin"}, &accessapitest.Decider{AllowsFolderFunc: func(_ api.ResourceType, r api.Repo, _ string, want api.Actions) bool {
					return r.Key != denied && want == api.ActionWrite
				}}, nil
			}
			f.mount(f.sources)
			u, _ := url.Parse(f.up.URL)
			r := f.registerPackage("bower-virtual", "hello", "ssh://git@"+u.Hostname()+":7999/team/hello.git", "admin")
			want := http.StatusCreated
			if denied != "" {
				want = http.StatusForbidden
			}
			if !current || r.Code != want {
				t.Fatalf("current=%t registration=%d body=%s, want %d", current, r.Code, r.Body.String(), want)
			}
			_, err := f.db.BowerRegistrations().Get(t.Context(), "bower-remote", "hello")
			if denied == "" && err != nil || denied != "" && !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("registration effect escaped selected current authority", err)
			}
		})
	}
}

func (f *fixture) registerPackage(key, name, raw, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(
		http.MethodPost,
		"/artifactory/api/bower/"+key+"/packages",
		strings.NewReader(url.Values{"name": {name}, "url": {raw}}.Encode()),
	)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func TestAccBowerPrivateStashRegistrationAndOfflineResolution(t *testing.T) {
	f := newRemoteFixture(t, true)
	f.configure(func(c *repo.Config) {
		c.URL = f.up.URL + "/stash"
		c.VCSGitProvider = repo.Stash
		c.BowerRegistryURL = "https://unused.invalid"
	})
	u, _ := url.Parse(f.up.URL)
	locator := "ssh://git@" + u.Hostname() + ":7999/team/hello.git"
	f.up.Set(
		"/stash/scm/team/hello.git/info/refs",
		fakeupstream.Resource{
			Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v1.0.0\n")),
		},
	)
	archive := bowerArchive(
		t,
		[]archiveEntry{
			{"hello-v1.0.0/bower.json", `{"name":"hello"}`, 0},
			{"hello-v1.0.0/index.js", "private", 0},
		},
	)
	f.up.Set(
		"/stash/rest/api/1.0/projects/team/repos/hello/archive",
		fakeupstream.Resource{Body: archive},
	)
	for _, key := range []string{"bower-remote", "bower-virtual"} {
		if key == "bower-virtual" {
			f.repo(key, repo.Virtual, "bower-remote")
		}
		rec := f.registerPackage(key, "hello", locator, "guest")
		if rec.Code != 403 {
			t.Fatalf("guest registration: %d %s", rec.Code, rec.Body)
		}
	}
	rec := f.registerPackage("bower-virtual", "hello", locator, "admin")
	if rec.Code != 201 {
		t.Fatalf("registration: %d %s", rec.Code, rec.Body)
	}
	rec = f.registerPackage("bower-remote", "hello", locator, "admin")
	if rec.Code != 403 {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	for _, key := range []string{"bower-remote", "bower-virtual"} {
		base := "/artifactory/api/bower/" + key + "/"
		for _, path := range []string{"packages", "packages/search/hello", "packages/hello", "refs/" + key + "/hello", "binaries/" + key + "/hello.git/v1.0.0"} {
			rec := f.get(base+path, "admin")
			if rec.Code != 200 {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
			}
		}
	}
	_, err := f.db.RemoteCache().Invalidate(t.Context(), "bower-remote", "catalog", true)
	if err != nil {
		t.Fatal(err)
	}
	if rec = f.get("/artifactory/api/bower/bower-remote/packages/hello", "admin"); rec.Code != 200 {
		t.Fatalf("registration lost after zap: %d", rec.Code)
	}
	f.configure(func(c *repo.Config) { c.Offline = new(true) })
	f.up.Close()
	rec = f.get(
		"/artifactory/api/bower/bower-virtual/binaries/bower-virtual/hello.git/v1.0.0",
		"admin",
	)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), archive) {
		t.Fatalf("offline: %d %s", rec.Code, rec.Body)
	}
	f.configure(func(c *repo.Config) { c.URL = "https://other.invalid" })
	if rec = f.get("/artifactory/api/bower/bower-remote/packages/hello", "admin"); rec.Code == 200 {
		t.Fatal("registration escaped changed provider origin")
	}
}

func TestUnitBowerSSHLocatorRejectsForeignOrAmbiguousCoordinates(t *testing.T) {
	c := repo.Config{URL: "https://stash.example/context", VCSGitProvider: repo.Stash}
	for _, raw := range []string{"ssh://git@other.example/team/repo.git", "ssh://git:secret@stash.example/team/repo.git", "ssh://user@stash.example/team/repo.git", "ssh://git@stash.example/team/../repo.git", "ssh://git@stash.example/team/repo%2egit", "ssh://git@stash.example/team/repo.git?secret=x", "ssh://git@stash.example/team/repo.git#tag", "ssh://git@stash.example:0/team/repo.git", "ssh://git@stash.example:65536/team/repo.git", "ssh://git@stash.example/team/repo"} {
		if _, err := gitPointerURL(c, "hello", raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	p, err := gitPointerURL(c, "hello", "ssh://git@stash.example:7999/team/repo.git")
	if err != nil || p.url.String() != "https://stash.example/context/scm/team/repo.git" {
		t.Fatalf("locator: %v, %v", p, err)
	}
	f := newRemoteFixture(t, true)
	f.repo("local", repo.Local)
	if rec := f.registerPackage(
		"local",
		"hello",
		"https://git.example/a/b.git",
		"admin",
	); rec.Code != 405 {
		t.Fatalf("local registration: %d", rec.Code)
	}
	if rec := f.registerPackage(
		"bower-remote",
		"hello",
		"ssh://git@other.example/a/b.git",
		"admin",
	); rec.Code != 400 {
		t.Fatalf("foreign registration: %d", rec.Code)
	}
	if rec := f.registerPackage(
		"bower-remote",
		"../escape",
		f.up.URL+"/git/team/hello.git",
		"admin",
	); rec.Code != 400 {
		t.Fatalf("invalid registration name: %d", rec.Code)
	}
}
