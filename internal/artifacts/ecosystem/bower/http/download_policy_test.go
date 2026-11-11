package bowerhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The binary archive reads its files' blobs itself, so it applies the
// download policy that the kit's Item applies (decision 0032): a
// blacked-out repository refuses the version by its manifest, an excluded
// file refuses the archive that would hold it, administrators included,
// and a virtual repository passes over a member that refuses the version.
func TestUnitBowerBinaryDownloadPolicy(t *testing.T) {
	f := newFixture(t)
	l := f.repo("bower-local", repo.Local)
	fed := f.repo("bower-fed", repo.Federated)
	_ = f.repo("bower-virtual", repo.Virtual, l.Key, fed.Key)
	for _, c := range []repo.Config{l, fed} {
		f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
		f.deploy(c, "hello", "1.0.0", "lib/index.js", "from-"+c.Key)
	}
	binary := func(key string) string {
		return "/artifactory/api/bower/" + key + "/binaries/" + key + "/hello.git/v1.0.0"
	}
	patch := func(key string, change func(*repo.Config)) {
		t.Helper()
		c, err := f.db.Repos().Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	served := func(key, from string) {
		t.Helper()
		rec := f.get(binary(key), "admin")
		if rec.Code != http.StatusOK || archivedIndex(t, rec) != from {
			t.Fatalf("GET %s: %d, want the archive from %s", binary(key), rec.Code, from)
		}
	}
	refused := func(key, message string) {
		t.Helper()
		rec := f.get(binary(key), "admin")
		if rec.Code != http.StatusNotFound || bowerMessage(t, rec) != message {
			t.Fatalf("GET %s: %d %s, want 404 %q", binary(key), rec.Code, rec.Body, message)
		}
	}
	served(l.Key, "from-bower-local")
	served("bower-virtual", "from-bower-local")

	patch(l.Key, func(c *repo.Config) { c.BlackedOut = ptr(true) })
	refused(l.Key, "The repository 'bower-local' is blacked out and cannot serve artifact "+
		"'bower-local:hello/1.0.0/bower.json'.")
	if rec := f.get(binary(l.Key), "guest"); rec.Code != http.StatusForbidden {
		t.Fatalf("a caller who may not read is told how the repository is set: %d %s",
			rec.Code, rec.Body)
	}
	served("bower-virtual", "from-bower-fed")

	patch(
		l.Key,
		func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("**/*.js") },
	)
	refused(l.Key, "The repository 'bower-local' rejected the artifact "+
		"'bower-local:hello/1.0.0/lib/index.js' due to its include/exclude pattern settings.")
	patch(l.Key, func(c *repo.Config) { c.ExcludesPattern = ptr("hello/**") })
	served("bower-virtual", "from-bower-fed")

	patch("bower-virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("hello/**") })
	refused("bower-virtual",
		"No repository found to serve the request for bower-virtual:hello/1.0.0/bower.json")
	patch("bower-virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("") })
	patch(l.Key, func(c *repo.Config) { c.ExcludesPattern = ptr("") })
	served(l.Key, "from-bower-local")
}

// The package lookup and refs a client resolves from refuse a blacked-out
// repository, naming the request's path, after read permission (R2a); the
// package list and search keep their answers (R2b); a version whose file
// the patterns reject stays listed (R3); and a binary is refused before its
// version is looked up. A virtual repository resolves without a
// blacked-out member, passes over a member whose patterns reject any file
// of the archive, and its own patterns refuse a binary whether the version
// exists or not (R5).
func TestAccBowerDownloadPolicyLocalAndVirtual(t *testing.T) {
	f := newFixture(t)
	l := f.repo("bower-local", repo.Local)
	fed := f.repo("bower-fed", repo.Federated)
	_ = f.repo("bower-virtual", repo.Virtual, l.Key, fed.Key)
	for _, c := range []repo.Config{l, fed} {
		f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
		f.deploy(c, "hello", "1.0.0", "lib/index.js", "from-"+c.Key)
	}
	f.deploy(l, "solo", "1.0.0", "bower.json", `{"name":"solo","version":"1.0.0"}`)
	a := policyAnswers{t: t, get: f.get}
	binary := func(key, version string) string {
		return key + "/binaries/" + key + "/hello.git/v" + version
	}

	f.patch(l.Key, func(c *repo.Config) { c.BlackedOut = ptr(true) })
	a.refused("bower-local/packages/hello", blackedOut(l.Key, "packages/hello"))
	a.refused("bower-local/refs/bower-local/hello", blackedOut(l.Key, "refs/bower-local/hello"))
	if rec := f.get("/artifactory/api/bower/bower-local/packages/hello", "guest"); rec.Code !=
		http.StatusForbidden {
		t.Fatalf("a caller who may not read is told how the repository is set: %d %s",
			rec.Code, rec.Body)
	}
	a.answered("bower-local/packages", `"name":"hello"`, `"name":"solo"`)
	a.answered("bower-local/packages/search/sol", `"name":"solo"`)
	a.refused(binary(l.Key, "9.9.9"), blackedOut(l.Key, "hello/9.9.9/bower.json"))
	a.refused("bower-virtual/packages/solo", "Bower package not found")
	a.refused("bower-virtual/refs/bower-virtual/solo", "Bower package not found")
	a.answered("bower-virtual/packages", `"name":"hello"`, `"name":"solo"`)
	a.answered("bower-virtual/refs/bower-virtual/hello", "refs/tags/v1.0.0")
	a.archived(binary("bower-virtual", "1.0.0"), "from-bower-fed")

	f.patch(l.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("**/*.js")
	})
	a.answered("bower-local/packages/hello", `"name":"hello"`)
	a.answered("bower-local/refs/bower-local/hello", "refs/tags/v1.0.0")
	a.refused(binary(l.Key, "1.0.0"), rejected(l.Key, "hello/1.0.0/lib/index.js"))
	a.archived(binary("bower-virtual", "1.0.0"), "from-bower-fed")

	f.patch(l.Key, func(c *repo.Config) { c.ExcludesPattern = ptr("") })
	f.patch("bower-virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("hello/9.9.9/**") })
	a.refused(binary("bower-virtual", "9.9.9"),
		noMember("bower-virtual", "hello/9.9.9/bower.json"))
	a.archived(binary("bower-virtual", "1.0.0"), "from-bower-local")
	f.patch("bower-virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.js") })
	a.refused(binary("bower-virtual", "1.0.0"),
		noMember("bower-virtual", "hello/1.0.0/lib/index.js"))
	a.answered("bower-virtual/refs/bower-virtual/hello", "refs/tags/v1.0.0")
}

// A blacked-out remote refuses the package lookup and refs naming the
// request's path, and a binary naming its archive's cache path, before its
// upstream is asked; its package list keeps its answer (R2b). Its
// metadata reads check the blackout alone, so a version whose manifest or
// folder the patterns reject stays listed and only its archive is refused
// (R3, R4). A virtual repository passes over the remote member that
// refuses the archive (R5).
func TestAccBowerDownloadPolicyRemote(t *testing.T) {
	f := newRemoteFixture(t, false)
	l := f.repo("bower-local", repo.Local)
	f.deploy(l, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(l, "hello", "1.0.0", "lib/index.js", "from-bower-local")
	_ = f.repo("bower-virtual", repo.Virtual, "bower-remote", l.Key)
	f.up.Set("/api/bower/source/packages",
		fakeupstream.Resource{Body: []byte(`[{"name":"hello","url":"art://source/hello"}]`)})
	refs := "/api/bower/source/refs/bower-remote/hello"
	f.up.Set(refs, fakeupstream.Resource{
		Body: []byte(strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"),
	})
	archive := "/api/bower/source/binaries/bower-remote/hello.git/v1.0.0"
	f.up.Set(archive, fakeupstream.Resource{Body: bowerArchive(t, []archiveEntry{
		{"hello-1.0.0/bower.json", `{"name":"hello","version":"1.0.0"}`, 0},
		{"hello-1.0.0/lib/index.js", "from-bower-remote", 0},
	})})
	a := policyAnswers{t: t, get: f.get}
	binary := "bower-remote/binaries/bower-remote/hello.git/v1.0.0"
	cached := remoteArchivePath("hello", "1.0.0")

	f.configure(func(c *repo.Config) { c.BlackedOut = ptr(true) })
	a.refused("bower-remote/packages/hello", blackedOut("bower-remote", "packages/hello"))
	a.refused("bower-remote/refs/bower-remote/hello",
		blackedOut("bower-remote", "refs/bower-remote/hello"))
	a.refused(binary, blackedOut("bower-remote", cached))
	a.refused("bower-remote/packages", blackedOut("bower-remote", ""))
	if n := f.up.Count(http.MethodGet, refs) + f.up.Count(http.MethodGet, archive); n != 0 {
		t.Fatalf("a blacked-out remote asked its upstream %d times", n)
	}
	a.answered("bower-virtual/packages", `"name":"hello"`)
	a.answered("bower-virtual/refs/bower-virtual/hello", "refs/tags/v1.0.0")
	a.archived("bower-virtual/binaries/bower-virtual/hello.git/v1.0.0", "from-bower-local")

	f.configure(func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("hello/1.0.0/bower.json")
	})
	a.answered("bower-remote/packages", `"name":"hello"`)
	a.answered("bower-remote/packages/hello", `"name":"hello"`)
	a.answered("bower-remote/refs/bower-remote/hello", "refs/tags/v1.0.0")
	a.archived(binary, "from-bower-remote")

	f.configure(func(c *repo.Config) { c.ExcludesPattern = ptr("hello/**") })
	a.answered("bower-remote/packages", `"name":"hello"`)
	a.answered("bower-remote/packages/search/hel", `"name":"hello"`)
	a.answered("bower-remote/packages/hello", `"name":"hello"`)
	a.answered("bower-remote/refs/bower-remote/hello", "refs/tags/v1.0.0")
	a.refused(binary, rejected("bower-remote", cached))
	a.archived("bower-virtual/binaries/bower-virtual/hello.git/v1.0.0", "from-bower-local")
}

// A Git remote's lookups check its blackout alone too, and its catalog
// keeps a package whose folder the patterns reject.
func TestAccBowerDownloadPolicyGitRemote(t *testing.T) {
	f := newRemoteFixture(t, true)
	pointer := `{"name":"hello","url":"` + f.up.URL + `/git/team/hello.git"}`
	f.up.Set("/registry/packages", fakeupstream.Resource{Body: []byte("[" + pointer + "]")})
	f.up.Set("/registry/packages/hello", fakeupstream.Resource{Body: []byte(pointer)})
	f.up.Set("/git/team/hello.git/info/refs", fakeupstream.Resource{Body: []byte(
		gitPkt("# service=git-upload-pack\n") + "0000" +
			gitPkt(strings.Repeat("a", 40)+" refs/tags/v1.0.0\n") + "0000",
	)})
	archive := "/git/team/hello/archive/refs/tags/v1.0.0.tar.gz"
	f.up.Set(archive, fakeupstream.Resource{
		Body: bowerArchive(t, []archiveEntry{{"hello-1.0.0/bower.json", `{"name":"hello"}`, 0}}),
	})
	a := policyAnswers{t: t, get: f.get}
	binary := "bower-remote/binaries/bower-remote/hello.git/v1.0.0"
	cached := remoteArchivePath("hello", "1.0.0")

	f.configure(func(c *repo.Config) { c.ExcludesPattern = ptr("hello/**") })
	a.answered("bower-remote/packages", `"name":"hello"`)
	a.answered("bower-remote/packages/hello", `"name":"hello"`)
	a.answered("bower-remote/refs/bower-remote/hello", "refs/tags/v1.0.0")
	a.refused(binary, rejected("bower-remote", cached))

	f.configure(func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = ptr(true), ptr("") })
	a.refused("bower-remote/packages/hello", blackedOut("bower-remote", "packages/hello"))
	a.refused(binary, blackedOut("bower-remote", cached))
	if n := f.up.Count(http.MethodGet, archive); n != 0 {
		t.Fatalf("a refused archive was fetched %d times", n)
	}
}

// The external dependency archive a client downloads through a virtual
// repository is a file it serves, which its own patterns refuse (R5),
// while the parent whose rewrite caches it is served. A dependency's refs
// keep a tag whose manifest the cache member's patterns reject (R3).
func TestAccBowerDependencyDownloadPolicy(t *testing.T) {
	f, base := dependencyFixture(t)
	f.up.Set("/git/team/dep.git/info/refs", fakeupstream.Resource{
		Body: []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v2.0.0\n")),
	})
	f.configure(func(c *repo.Config) { c.ExcludesPattern = ptr("dep/2.0.0/bower.json") })
	rec := f.get(base+"refs/team/dep", "admin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "refs/tags/v2.0.0") {
		t.Fatalf("shorthand refs: %d %s", rec.Code, rec.Body)
	}
	f.configure(func(c *repo.Config) { c.ExcludesPattern = ptr("") })

	f.patch("virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("dep/**") })
	rec = f.get(base+"binaries/virtual/hello.git/v1.0.0", "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("rewritten parent: %d %s", rec.Code, rec.Body)
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
	_, tag, _ := strings.Cut(m.Dependencies["dep"], "#")
	rec = f.get(base+"binaries/external/dep.git/"+url.PathEscape(tag), "admin")
	want := "No repository found to serve the request for virtual:dep/0.0.0/external/"
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(bowerMessage(t, rec), want) {
		t.Fatalf("dependency the virtual repository's patterns reject: %d %s", rec.Code, rec.Body)
	}
}

// A remote's refs lookup refused by the download policy is the repository's
// answer, not a missing package, though both are 404: the blackout can be
// set between the route's own check and the lookup.
func TestUnitBowerMissingPackageKeepsPolicyRefusal(t *testing.T) {
	refusal := pkgroute.SourceError(fmt.Errorf(
		"bower remote refs: %w",
		format.Rejectf(
			http.StatusNotFound,
			"%s",
			blackedOut("bower-remote", "packages/hello"),
		),
	), false)
	for _, tc := range []struct {
		err     error
		missing bool
	}{
		{fmt.Errorf("lookup: %w", refusal), false},
		{fmt.Errorf("lookup: %w", kernel.ErrNotFound), true},
		{apierror.New(http.StatusNotFound, "Not Found"), true},
		{apierror.New(http.StatusBadGateway, "Bad Gateway"), false},
	} {
		if got := missingPackage(tc.err); got != tc.missing {
			t.Fatalf("missingPackage(%v) = %t, want %t", tc.err, got, tc.missing)
		}
	}
}

// policyAnswers checks the answers of routes under /artifactory/api/bower/,
// asked by an administrator, whom the download policy refuses too.
type policyAnswers struct {
	t   *testing.T
	get func(path, user string) *httptest.ResponseRecorder
}

func (a policyAnswers) refused(path, message string) {
	a.t.Helper()
	rec := a.get("/artifactory/api/bower/"+path, "admin")
	if rec.Code != http.StatusNotFound || bowerMessage(a.t, rec) != message {
		a.t.Fatalf("GET %s: %d %s, want 404 %q", path, rec.Code, rec.Body, message)
	}
}

// answered checks a 200 whose body holds each of want.
func (a policyAnswers) answered(path string, want ...string) {
	a.t.Helper()
	rec := a.get("/artifactory/api/bower/"+path, "admin")
	if rec.Code != http.StatusOK {
		a.t.Fatalf("GET %s: %d %s, want 200", path, rec.Code, rec.Body)
	}
	for _, w := range want {
		if !strings.Contains(rec.Body.String(), w) {
			a.t.Fatalf("GET %s: %s, want %s in it", path, rec.Body, w)
		}
	}
}

// archived checks a 200 whose archive's lib/index.js is from.
func (a policyAnswers) archived(path, from string) {
	a.t.Helper()
	rec := a.get("/artifactory/api/bower/"+path, "admin")
	if rec.Code != http.StatusOK || archivedIndex(a.t, rec) != from {
		a.t.Fatalf("GET %s: %d %s, want the archive from %s", path, rec.Code, rec.Body, from)
	}
}

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func blackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
		key + ":" + path + "'."
}

func rejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

func noMember(key, path string) string {
	return "No repository found to serve the request for " + key + ":" + path
}

func ptr[T any](v T) *T { return &v }

// archivedIndex is lib/index.js in the archive rec answered.
func archivedIndex(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("archive: %v: %s", err, rec.Body)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(h.Name, "/lib/index.js") {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
}

// bowerMessage is the message of the Artifactory error envelope rec holds.
func bowerMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("not one error: %v: %s", err, rec.Body)
	}
	return body.Errors[0].Message
}
