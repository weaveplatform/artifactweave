package npm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // npm's dist.shasum is SHA-1 by definition
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	registry = "/artifactory/api/npm/npm-local/"
	base     = "https://registry.example"
	// abbreviatedAccept is what pacote sends when it installs.
	abbreviatedAccept = "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*"
)

// fixture is the npm protocol mounted as the server mounts it, over a
// store whose npm-local holds what each test deploys. A request names its
// principal in X-Test-User: none, anonymous, admin, or a user, whose
// grants the fixture gives:
//   - reader reads all of npm-local;
//   - partial reads only the 1.x tarballs of package mixed;
//   - writer reads, deploys and deletes all of npm-local;
//   - narrow reads and deploys only the 1.x tarballs of package pkg;
//   - nobody holds nothing.
type fixture struct {
	h      http.Handler
	db     *inmem.Store
	items  *item.Service
	tick   time.Time
	faults []error
	// opts and principal are what the router is mounted with.
	opts      pkgroute.Options
	principal func(http.Handler) http.Handler
}

func newFixture(t *testing.T, wrap ...func(store.Store) store.Store) *fixture {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		{Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM, URL: "https://up.example"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(user, include string, actions types.Actions) {
		t.Helper()
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: user,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: actions}},
				Targets: map[string]types.Target{"npm-local": {Include: []string{include}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read, write, del := types.ActionRead, types.ActionWrite, types.ActionDelete
	grant("reader", "**", read)
	grant("partial", "mixed/-/mixed-1.*", read)
	grant("writer", "**", read|write|del)
	grant("narrow", "pkg/-/pkg-1.*", read|write)
	grantLifecycle(grant)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, tick: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	f.items = &item.Service{Store: db, Blobs: blobs, Now: f.now}
	var backend store.Store = db
	for _, w := range wrap {
		backend = w(backend)
	}
	o := pkgroute.Options{
		Store: backend,
		Items: func() *item.Service { return f.items },
		ServeItem: func(w http.ResponseWriter, _ *http.Request, c repo.Config, it types.Item) {
			_, _ = io.WriteString(w, "served "+c.Key+"/"+it.FullPath())
		},
		ItemError: func(_ context.Context, _ string, err error) error { return err },
		Fault:     func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
	f.opts = o
	f.principal = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Test-User")
			if name == "" {
				next.ServeHTTP(w, r)
				return
			}
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == auth.AnonymousUser {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
	f.remount(t, npm.Options{})
	return f
}

// remount mounts the npm protocol afresh, with o.
func (f *fixture) remount(t *testing.T, o npm.Options) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(base))
	r.Use(f.principal)
	pkgroute.Mount(r, f.opts, npm.Protocol(o))
	f.h = r
}

// now is a clock a minute later at each deploy, so each version has its
// own time.
func (f *fixture) now() time.Time {
	f.tick = f.tick.Add(time.Minute)
	return f.tick
}

// deploy stores a tarball whose package.json is manifest at the canonical
// path of name at version, with props.
func (f *fixture) deploy(
	t *testing.T,
	name, version, manifest string,
	props types.Properties,
) []byte {
	t.Helper()
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	body := tarball(manifest)
	if _, err := f.items.Deploy(ctx, item.Deploy{
		Target:     item.Target{Repo: c, Path: npmfmt.CanonicalPath(name, version)},
		Body:       bytes.NewReader(body),
		Properties: props,
	}); err != nil {
		t.Fatal(err)
	}
	return body
}

func (f *fixture) do(method, path, user string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// tarball is a package tarball holding package.json as manifest.
func tarball(manifest string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{
		Name: "package/package.json", Mode: 0o644, Size: int64(len(manifest)),
		Typeflag: tar.TypeReg,
	})
	_, _ = io.WriteString(tw, manifest)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// dist is the dist object npm must get for a tarball of body at path: the
// three fields of 7.x's (RTFACT-30472), without npm's own registry's
// fileCount and unpackedSize.
func dist(body []byte, path string) map[string]any {
	sha1sum := sha1.Sum(body) //nolint:gosec // npm's dist.shasum is SHA-1
	sha512sum := sha512.Sum512(body)
	return map[string]any{
		"shasum":    hex.EncodeToString(sha1sum[:]),
		"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sha512sum[:]),
		"tarball":   base + registry + path,
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body, err)
	}
	return m
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

const (
	rich100 = `{"name":"rich","version":"1.0.0","description":"First <b>rich</b>",` +
		`"dependencies":{"dep":"^1.0.0"},"bin":{"rich":"cli.js"},"engines":{"node":">=18"},` +
		`"scripts":{"install":"node build.js"},"deprecated":"the author's own","_id":"bogus",` +
		`"author":"First Author","license":"ISC"}`
	// The author wrote the version with a v, which the index cleans.
	rich110 = `{"name":"rich","version":"v1.1.0","description":"Second","license":"MIT",` +
		`"author":{"name":"Ann","email":"ann@example.com"}}`
	rich200 = `{"name":"rich","version":"2.0.0-beta.1","description":"Third","os":["linux"]}`
)

// deployRich deploys three versions of rich: 1.0.0 deprecated, 1.1.0, and
// 2.0.0-beta.1 tagged beta. It returns each tarball by version.
func (f *fixture) deployRich(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"1.0.0": f.deploy(t, "rich", "1.0.0", rich100,
			types.Properties{npmfmt.PropDeprecated: {"use 1.1.0"}}),
		"1.1.0": f.deploy(t, "rich", "1.1.0", rich110, nil),
		"2.0.0-beta.1": f.deploy(t, "rich", "2.0.0-beta.1", rich200,
			types.Properties{npmfmt.PropDistTag: {"beta"}}),
	}
}

// annAuthor is rich 1.1.0's author.
var annAuthor = map[string]any{"name": "Ann", "email": "ann@example.com"}

// The full packument holds each version's package.json with the index's
// cleaned version, its _id, its dist with the tarball's URL under the
// request's base URL, and the deprecation its property holds, whatever
// the author wrote; latest is the highest version that is no pre-release.
// Around them are the fields of 7.x's (RTFACT-30472): _rev 1-0, users,
// time.unpublished, and the latest version's author and license.
func TestFullPackument(t *testing.T) {
	f := newFixture(t)
	tgz := f.deployRich(t)
	rec := f.do("GET", registry+"rich", "reader")
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	got := decode(t, rec)
	want := map[string]any{
		"_id": "rich", "_rev": "1-0", "name": "rich", "description": "Second",
		"dist-tags": map[string]any{"latest": "1.1.0", "beta": "2.0.0-beta.1"},
		"versions": map[string]any{
			"1.0.0": map[string]any{
				"_id": "rich@1.0.0", "name": "rich", "version": "1.0.0",
				"description":  "First <b>rich</b>",
				"dependencies": map[string]any{"dep": "^1.0.0"},
				"bin":          map[string]any{"rich": "cli.js"},
				"engines":      map[string]any{"node": ">=18"},
				"scripts":      map[string]any{"install": "node build.js"},
				"deprecated":   "use 1.1.0",
				"author":       "First Author", "license": "ISC",
				"dist": dist(tgz["1.0.0"], "rich/-/rich-1.0.0.tgz"),
			},
			"1.1.0": map[string]any{
				"_id": "rich@1.1.0", "name": "rich", "version": "1.1.0",
				"description": "Second", "license": "MIT", "author": annAuthor,
				"dist": dist(tgz["1.1.0"], "rich/-/rich-1.1.0.tgz"),
			},
			"2.0.0-beta.1": map[string]any{
				"_id": "rich@2.0.0-beta.1", "name": "rich", "version": "2.0.0-beta.1",
				"description": "Third", "os": []any{"linux"},
				"dist": dist(tgz["2.0.0-beta.1"], "rich/-/rich-2.0.0-beta.1.tgz"),
			},
		},
		"author": annAuthor,
		"time": map[string]any{
			"unpublished":  nil,
			"created":      "2026-10-01T09:01:00.000Z",
			"modified":     "2026-10-01T09:03:00.000Z",
			"1.0.0":        "2026-10-01T09:01:00.000Z",
			"1.1.0":        "2026-10-01T09:02:00.000Z",
			"2.0.0-beta.1": "2026-10-01T09:03:00.000Z",
		},
		"users":   map[string]any{},
		"license": "MIT",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("packument (-want +got):\n%s", diff)
	}
	if !strings.Contains(rec.Body.String(), "<b>") {
		t.Errorf("HTML characters escaped: %s", rec.Body)
	}
}

// The latest version's description, author and license reach the top of
// the full packument as its package.json has them, empty ones included:
// 7.x sends "description" : "" for an empty description (RTFACT-30472's
// second packument). Only a field package.json lacks is left out, and a
// description that is not a string.
func TestPackumentKeepsEmptyFields(t *testing.T) {
	f := newFixture(t)
	f.deploy(t, "blank", "1.0.0",
		`{"name":"blank","version":"1.0.0","description":"","author":"","license":""}`, nil)
	f.deploy(t, "bare", "1.0.0", `{"name":"bare","version":"1.0.0"}`, nil)
	f.deploy(t, "odd", "1.0.0", `{"name":"odd","version":"1.0.0","description":["a list"]}`, nil)
	for name, want := range map[string]map[string]any{
		"blank": {"description": "", "author": "", "license": ""},
		"bare":  {},
		"odd":   {},
	} {
		rec := f.do("GET", registry+name, "reader")
		wantStatus(t, rec, http.StatusOK)
		got := map[string]any{}
		for k, v := range decode(t, rec) {
			if k == "description" || k == "author" || k == "license" {
				got[k] = v
			}
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", name, diff)
		}
	}
	// The version keeps the empty description its package.json has.
	versions, _ := decode(t, f.do("GET", registry+"blank", "reader"))["versions"].(map[string]any)
	if v, _ := versions["1.0.0"].(map[string]any); v["description"] != "" {
		t.Fatalf("%v", versions)
	}
}

// The abbreviated packument keeps per version only what installing needs,
// with hasInstallScript, and is what pacote's Accept asks for.
func TestAbbreviatedPackument(t *testing.T) {
	f := newFixture(t)
	tgz := f.deployRich(t)
	rec := f.do("GET", registry+"rich", "reader", "Accept", abbreviatedAccept)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.npm.install-v1+json" {
		t.Errorf("Content-Type %q", ct)
	}
	want := map[string]any{
		"name": "rich", "modified": "2026-10-01T09:03:00.000Z",
		"dist-tags": map[string]any{"latest": "1.1.0", "beta": "2.0.0-beta.1"},
		"versions": map[string]any{
			"1.0.0": map[string]any{
				"name": "rich", "version": "1.0.0",
				"dependencies":     map[string]any{"dep": "^1.0.0"},
				"bin":              map[string]any{"rich": "cli.js"},
				"engines":          map[string]any{"node": ">=18"},
				"deprecated":       "use 1.1.0",
				"hasInstallScript": true,
				"dist":             dist(tgz["1.0.0"], "rich/-/rich-1.0.0.tgz"),
			},
			"1.1.0": map[string]any{
				"name": "rich", "version": "1.1.0",
				"dist": dist(tgz["1.1.0"], "rich/-/rich-1.1.0.tgz"),
			},
			"2.0.0-beta.1": map[string]any{
				"name": "rich", "version": "2.0.0-beta.1", "os": []any{"linux"},
				"dist": dist(tgz["2.0.0-beta.1"], "rich/-/rich-2.0.0-beta.1.tgz"),
			},
		},
	}
	if diff := cmp.Diff(want, decode(t, rec)); diff != "" {
		t.Errorf("packument (-want +got):\n%s", diff)
	}
}

// The abbreviated form only when its type comes first, and never for a
// read that will write the packument back.
func TestPackumentForm(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	for _, c := range []struct {
		path, accept string
		abbreviated  bool
	}{
		{"rich", abbreviatedAccept, true},
		{"rich", "Application/VND.npm.install-v1+json", true},
		{"rich", "application/json, application/vnd.npm.install-v1+json", false},
		{"rich", "", false},
		{"rich?write=true", abbreviatedAccept, false},
	} {
		rec := f.do("GET", registry+c.path, "reader", "Accept", c.accept)
		wantStatus(t, rec, http.StatusOK)
		_, full := decode(t, rec)["_rev"]
		if full == c.abbreviated {
			t.Errorf("%s with Accept %q: full %v", c.path, c.accept, full)
		}
	}
}

// latest is served as the npm format stores it (npm.disttag=latest): set
// on a version, it stays there through a tagged deploy of a higher one and
// moves to an untagged one that outranks it; among pre-releases alone the
// highest has it.
func TestLatest(t *testing.T) {
	f := newFixture(t)
	f.deploy(t, "pinned", "1.0.0", `{"name":"pinned","version":"1.0.0"}`, nil)
	f.deploy(t, "pinned", "1.1.0", `{"name":"pinned","version":"1.1.0"}`, nil)
	f.deploy(t, "pinned", "1.0.1", `{"name":"pinned","version":"1.0.1"}`,
		types.Properties{npmfmt.PropDistTag: {"latest"}})
	f.deploy(t, "pinned", "2.0.0", `{"name":"pinned","version":"2.0.0"}`,
		types.Properties{npmfmt.PropDistTag: {"next"}})
	f.deploy(t, "moved", "1.0.0", `{"name":"moved","version":"1.0.0"}`,
		types.Properties{npmfmt.PropDistTag: {"latest"}})
	f.deploy(t, "moved", "2.0.0", `{"name":"moved","version":"2.0.0"}`, nil)
	f.deploy(t, "pre", "1.0.0-alpha", `{"name":"pre","version":"1.0.0-alpha"}`, nil)
	f.deploy(t, "pre", "1.0.0-beta", `{"name":"pre","version":"1.0.0-beta"}`, nil)
	for name, want := range map[string]map[string]any{
		"pinned": {"latest": "1.0.1", "next": "2.0.0"},
		"moved":  {"latest": "2.0.0"},
		"pre":    {"latest": "1.0.0-beta"},
	} {
		tags := decode(t, f.do("GET", registry+name, "reader"))["dist-tags"]
		if diff := cmp.Diff(want, tags); diff != "" {
			t.Errorf("%s dist-tags (-want +got):\n%s", name, diff)
		}
	}
}

// A version whose tarball the caller may not read is left out, with the
// tags that name it, and latest resolves among what is left; _rev is 1-0
// whatever the caller sees.
func TestPackumentShowsReadableVersions(t *testing.T) {
	f := newFixture(t)
	f.deploy(t, "mixed", "1.0.0", `{"name":"mixed","version":"1.0.0"}`, nil)
	f.deploy(t, "mixed", "2.0.0", `{"name":"mixed","version":"2.0.0"}`,
		types.Properties{npmfmt.PropDistTag: {"next", "latest"}})
	all := decode(t, f.do("GET", registry+"mixed", "reader"))
	some := decode(t, f.do("GET", registry+"mixed", "partial"))
	if diff := cmp.Diff(
		map[string]any{"latest": "2.0.0", "next": "2.0.0"}, all["dist-tags"],
	); diff != "" {
		t.Errorf("reader's dist-tags (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]any{"latest": "1.0.0"}, some["dist-tags"]); diff != "" {
		t.Errorf("partial's dist-tags (-want +got):\n%s", diff)
	}
	versions, _ := some["versions"].(map[string]any)
	if _, ok := versions["2.0.0"]; ok || len(versions) != 1 {
		t.Errorf("partial sees %v", versions)
	}
	if some["_rev"] != "1-0" || all["_rev"] != "1-0" {
		t.Errorf("partial's _rev %q, reader's %q", some["_rev"], all["_rev"])
	}
	// A latest version without an author or a license gives the packument
	// neither.
	for _, field := range []string{"author", "license"} {
		if v, ok := all[field]; ok {
			t.Errorf("%s %v", field, v)
		}
	}
	wantStatus(t, f.do("GET", registry+"mixed/2.0.0", "partial"), http.StatusNotFound)
	wantStatus(t, f.do("GET", registry+"mixed/next", "partial"), http.StatusNotFound)
	wantStatus(t, f.do("GET", registry+"mixed/1.0.0", "partial"), http.StatusOK)
}

// _rev is 1-0 as 7.x answers it, whatever changes: a tag, a deprecation,
// or a publish.
func TestRev(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	rev := func() any { return decode(t, f.do("GET", registry+"rich", "reader"))["_rev"] }
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	it, err := f.db.Items().Get(ctx, "npm-local", false, npmfmt.CanonicalPath("rich", "1.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := rev(); got != "1-0" {
		t.Errorf("_rev %v", got)
	}
	if err := f.items.Annotate(ctx, c, it, types.Properties{
		npmfmt.PropDistTag: {"stable"}, npmfmt.PropDeprecated: {"old"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	f.deploy(t, "rich", "3.0.0", `{"name":"rich","version":"3.0.0"}`, nil)
	if got := rev(); got != "1-0" {
		t.Errorf("_rev after changes %v", got)
	}
}

// Each form of a packument carries X-Checksum-Sha1 and an ETag equal to
// it, the SHA-1 of its body, unquoted as 7.x sends it; If-None-Match
// naming it, however a client or proxy wrote it, answers 304 with both
// headers and no body. Anything the body shows changing changes the ETag.
func TestPackumentETag(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	etag := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		sum := sha1.Sum(rec.Body.Bytes()) //nolint:gosec // the ETag 7.x computes
		want := hex.EncodeToString(sum[:])
		if got := rec.Header().Get("ETag"); got != want ||
			rec.Header().Get("X-Checksum-Sha1") != want {
			t.Fatalf("ETag %q, X-Checksum-Sha1 %q, want %s",
				got, rec.Header().Get("X-Checksum-Sha1"), want)
		}
		return want
	}
	full := f.do("GET", registry+"rich", "reader")
	wantStatus(t, full, http.StatusOK)
	tag := etag(full)
	short := f.do("GET", registry+"rich", "reader", "Accept", abbreviatedAccept)
	shortTag := etag(short)
	if shortTag == tag {
		t.Fatal("both forms have one ETag")
	}
	if head := f.do("HEAD", registry+"rich", "reader"); head.Header().Get("ETag") != tag {
		t.Errorf("HEAD's ETag %q, GET's %q", head.Header().Get("ETag"), tag)
	}
	for _, match := range []string{
		tag, `"` + tag + `"`, "W/" + tag, `W/"` + tag + `"`, "*",
		`"other", ` + tag, " other ," + tag + " ",
	} {
		rec := f.do("GET", registry+"rich", "reader", "If-None-Match", match)
		wantStatus(t, rec, http.StatusNotModified)
		if rec.Body.Len() != 0 || rec.Header().Get("ETag") != tag ||
			rec.Header().Get("X-Checksum-Sha1") != tag {
			t.Errorf("If-None-Match %q: %v %q", match, rec.Header(), rec.Body)
		}
	}
	for _, match := range []string{"other", tag[1:], shortTag} {
		wantStatus(t, f.do("GET", registry+"rich", "reader", "If-None-Match", match),
			http.StatusOK)
	}
	// The abbreviated form's own ETag answers 304 to its own request.
	wantStatus(t, f.do("GET", registry+"rich", "reader", "Accept", abbreviatedAccept,
		"If-None-Match", shortTag), http.StatusNotModified)
	// What a caller may read is part of the body, and so of the ETag.
	f.deploy(t, "mixed", "1.0.0", `{"name":"mixed","version":"1.0.0"}`, nil)
	f.deploy(t, "mixed", "2.0.0", `{"name":"mixed","version":"2.0.0"}`, nil)
	if etag(f.do("GET", registry+"mixed", "partial")) ==
		etag(f.do("GET", registry+"mixed", "reader")) {
		t.Error("callers who see different versions share an ETag")
	}
	f.deploy(t, "rich", "3.0.0", `{"name":"rich","version":"3.0.0"}`, nil)
	wantStatus(t, f.do("GET", registry+"rich", "reader", "If-None-Match", tag), http.StatusOK)
	// A version manifest carries no ETag.
	if got := f.do("GET", registry+"rich/1.1.0", "reader").Header().Get("ETag"); got != "" {
		t.Errorf("manifest ETag %q", got)
	}
}

// A version is read by its number, as npm's semver.clean cleans it, or by
// a dist-tag, and answers the version's document in the full form.
func TestVersionManifest(t *testing.T) {
	f := newFixture(t)
	tgz := f.deployRich(t)
	want := map[string]any{
		"_id": "rich@1.1.0", "name": "rich", "version": "1.1.0",
		"description": "Second", "license": "MIT", "author": annAuthor,
		"dist": dist(tgz["1.1.0"], "rich/-/rich-1.1.0.tgz"),
	}
	for _, spec := range []string{"1.1.0", "v1.1.0", "=1.1.0", "latest"} {
		rec := f.do("GET", registry+"rich/"+spec, "reader")
		wantStatus(t, rec, http.StatusOK)
		if diff := cmp.Diff(want, decode(t, rec)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", spec, diff)
		}
	}
	got := decode(t, f.do("GET", registry+"rich/beta", "reader"))
	if got["version"] != "2.0.0-beta.1" {
		t.Errorf("beta is %v", got["version"])
	}
	for _, spec := range []string{"9.9.9", "nope", "1.1"} {
		rec := f.do("GET", registry+"rich/"+spec, "reader")
		wantStatus(t, rec, http.StatusNotFound)
		if msg := decode(t, rec)["error"]; msg != "version not found: rich@"+spec {
			t.Errorf("%s: %v", spec, msg)
		}
	}
}

// A scoped package is addressed with its slash encoded, as npm sends it,
// or not; its tarball URL keeps the scope in the file name.
func TestScopedPackage(t *testing.T) {
	const manifest = `{"name":"@acme/tool","version":"1.0.0"}`
	f := newFixture(t)
	body := f.deploy(t, "@acme/tool", "1.0.0", manifest, nil)
	for _, p := range []string{"@acme%2ftool", "@acme%2Ftool", "@acme/tool"} {
		rec := f.do("GET", registry+p, "reader")
		wantStatus(t, rec, http.StatusOK)
		versions, _ := decode(t, rec)["versions"].(map[string]any)
		v, _ := versions["1.0.0"].(map[string]any)
		want := dist(body, "@acme/tool/-/@acme/tool-1.0.0.tgz")
		if diff := cmp.Diff(want, v["dist"]); diff != "" {
			t.Errorf("%s dist (-want +got):\n%s", p, diff)
		}
		wantStatus(t, f.do("GET", registry+p+"/1.0.0", "reader"), http.StatusOK)
	}
	wantStatus(t, f.do("GET", registry+"@acme", "reader"), http.StatusNotFound)
}

// A path with a "-" segment is a tarball, downloaded through the item
// routes' download as the file at that path.
func TestTarball(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	f.deploy(t, "@acme/tool", "1.0.0", `{"name":"@acme/tool","version":"1.0.0"}`, nil)
	for p, want := range map[string]string{
		"rich/-/rich-1.0.0.tgz":                   "served npm-local/rich/-/rich-1.0.0.tgz",
		"@acme%2ftool/-/@acme%2ftool-1.0.0.tgz":   "served npm-local/@acme/tool/-/@acme/tool-1.0.0.tgz",
		"@acme/tool/-/@acme/tool-1.0.0.tgz":       "served npm-local/@acme/tool/-/@acme/tool-1.0.0.tgz",
		"@acme%2ftool/-/@acme/tool-1.0.0.tgz;k=v": "served npm-local/@acme/tool/-/@acme/tool-1.0.0.tgz",
	} {
		rec := f.do("GET", registry+p, "reader")
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != want {
			t.Errorf("%s: %s", p, rec.Body)
		}
	}
	wantStatus(t, f.do("HEAD", registry+"rich/-/rich-1.0.0.tgz", "reader"), http.StatusOK)
	wantStatus(t, f.do("GET", registry+"rich/-/absent.tgz", "reader"), http.StatusNotFound)
	wantStatus(t, f.do("GET", registry+"rich/-/rich-1.0.0.tgz", "nobody"), http.StatusForbidden)
}

// A caller that may not read a package's folder is refused whether or not
// the package exists; one that may gets 404 for a package with nothing it
// can read.
func TestPackumentRefusals(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	f.deploy(t, "mixed", "2.0.0", `{"name":"mixed","version":"2.0.0"}`, nil)
	for _, c := range []struct {
		path, user string
		want       int
	}{
		{"rich", "nobody", http.StatusForbidden},
		{"absent", "nobody", http.StatusForbidden},
		{"rich", auth.AnonymousUser, http.StatusUnauthorized},
		{"rich", "", http.StatusUnauthorized},
		{"absent", "reader", http.StatusNotFound},
		// partial sees the folder through mixed's 1.x pattern, and there is
		// no 1.x.
		{"mixed", "partial", http.StatusNotFound},
		{"rich", "partial", http.StatusForbidden},
		{"rich/1.0.0", "nobody", http.StatusForbidden},
	} {
		rec := f.do("GET", registry+c.path, c.user)
		wantStatus(t, rec, c.want)
		if decode(t, rec)["error"] == nil {
			t.Errorf("%s as %q: not npm's envelope: %s", c.path, c.user, rec.Body)
		}
	}
}

// Paths that name no package, or more than a version under one, answer
// 404 in npm's envelope; remote repositories wait for P9.
func TestPathsNotServed(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	for _, p := range []string{"", "-/all", "rich/1.0.0/extra"} {
		rec := f.do("GET", registry+p, "reader")
		wantStatus(t, rec, http.StatusNotFound)
		if msg, _ := decode(t, rec)["error"].(string); !strings.HasPrefix(msg, "not found: GET ") {
			t.Errorf("%s: %s", p, rec.Body)
		}
	}
	// A malformed matrix parameter.
	wantStatus(t, f.do("GET", registry+"rich;=v", "reader"), http.StatusBadRequest)
	rec := f.do("GET", "/artifactory/api/npm/npm-remote/rich", "reader")
	wantStatus(t, rec, http.StatusNotImplemented)
	// The JFrog operations beside the registry keep their paths.
	wantStatus(t, f.do("PUT", "/artifactory/api/npm/auth", "reader"), http.StatusMethodNotAllowed)
	wantStatus(t, f.do("PUT", registry+"auth/acme", "reader"), http.StatusMethodNotAllowed)
}

func TestPingAndWhoami(t *testing.T) {
	f := newFixture(t)
	for _, user := range []string{"reader", "nobody", auth.AnonymousUser} {
		rec := f.do("GET", registry+"-/ping?write=true", user)
		wantStatus(t, rec, http.StatusOK)
		if strings.TrimSpace(rec.Body.String()) != "{}" {
			t.Errorf("ping as %s: %s", user, rec.Body)
		}
	}
	rec := f.do("GET", registry+"-/whoami", "reader")
	wantStatus(t, rec, http.StatusOK)
	if diff := cmp.Diff(map[string]any{"username": "reader"}, decode(t, rec)); diff != "" {
		t.Errorf("whoami (-want +got):\n%s", diff)
	}
	for _, user := range []string{auth.AnonymousUser, ""} {
		rec := f.do("GET", registry+"-/whoami", user)
		wantStatus(t, rec, http.StatusUnauthorized)
		if decode(t, rec)["error"] == nil {
			t.Errorf("whoami as %q: %s", user, rec.Body)
		}
	}
}

// A version document the index cannot decode is a fault: 500, and the
// cause goes to the access log.
func TestBrokenIndexIsAFault(t *testing.T) {
	f := newFixture(t)
	f.deploy(t, "broken", "1.0.0", `{"name":"broken","version":"1.0.0"}`, nil)
	ctx := context.Background()
	p, err := f.db.Packages().Find(ctx, "npm-local", "broken")
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Packages().Version(ctx, p.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range []string{`not json`, `{"manifest":[1],"dist":{}}`} {
		v.Metadata = []byte(meta)
		if _, err := f.db.Packages().PutVersion(ctx, v); err != nil {
			t.Fatal(err)
		}
		f.faults = nil
		for _, path := range []string{"broken", "broken/1.0.0"} {
			for _, accept := range []string{"", abbreviatedAccept} {
				rec := f.do("GET", registry+path, "reader", "Accept", accept)
				wantStatus(t, rec, http.StatusInternalServerError)
			}
		}
		if len(f.faults) == 0 {
			t.Errorf("metadata %s: no fault logged", meta)
		}
	}
}

// A store that fails is a fault, not a missing package.
func TestStoreFailureIsAFault(t *testing.T) {
	for _, failing := range []string{"find", "versions"} {
		f := newFixture(t, func(st store.Store) store.Store {
			return failingStore{Store: st, failing: failing}
		})
		f.deployRich(t)
		rec := f.do("GET", registry+"rich", "reader")
		wantStatus(t, rec, http.StatusInternalServerError)
		if len(f.faults) != 1 || !errors.Is(f.faults[0], errBroken) {
			t.Errorf("%s: faults %v", failing, f.faults)
		}
	}
}

var errBroken = errors.New("broken store")

// failingStore fails one of its package index's reads.
type failingStore struct {
	store.Store
	failing string
}

func (s failingStore) Packages() store.PackageStore {
	return failingPackages{PackageStore: s.Store.Packages(), failing: s.failing}
}

type failingPackages struct {
	store.PackageStore
	failing string
}

func (p failingPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.failing == "find" {
		return types.Package{}, errBroken
	}
	return p.PackageStore.Find(ctx, key, name) //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if p.failing == "versions" {
		return nil, errBroken
	}
	return p.PackageStore.Versions(ctx, id) //nolint:wrapcheck // a test's pass-through
}
