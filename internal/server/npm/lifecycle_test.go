package npm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// grantLifecycle gives the fixture the users the dist-tag, deprecate and
// unpublish tests act as:
//   - tagger reads and annotates all of npm-local;
//   - blind annotates all of npm-local and reads nothing;
//   - tagone reads and annotates only rich's 1.x tarballs;
//   - pruner reads and deletes only rich's 1.x tarballs.
func grantLifecycle(grant func(user, include string, actions types.Actions)) {
	read, annotate := types.ActionRead, types.ActionAnnotate
	grant("tagger", "**", read|annotate)
	grant("blind", "**", annotate)
	grant("tagone", "rich/-/rich-1.*", read|annotate)
	grant("pruner", "rich/-/rich-1.*", read|types.ActionDelete)
}

const tags = registry + "-/package/"

// tagsOf is the dist-tags the packument shows reader.
func (f *fixture) tagsOf(t *testing.T, name string) any {
	t.Helper()
	return decode(t, f.do("GET", registry+name, "reader"))["dist-tags"]
}

// npm dist-tag ls, add and rm (R25 2.2): each answers the dist-tags the
// caller then sees, a tag set on one version is taken from the version that
// had it, and latest moves as the npm format's rules move it.
func TestAccDistTags(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	f.deploy(t, "@acme/tool", "1.0.0", `{"name":"@acme/tool","version":"1.0.0"}`, nil)
	initial := map[string]any{"latest": "1.1.0", "beta": "2.0.0-beta.1"}
	rec := f.do("GET", tags+"rich/dist-tags", "reader")
	wantStatus(t, rec, http.StatusOK)
	if diff := cmp.Diff(initial, decode(t, rec)); diff != "" {
		t.Fatalf("ls (-want +got):\n%s", diff)
	}
	for _, p := range []string{"@acme%2ftool/dist-tags", "@acme/tool/dist-tags"} {
		rec := f.do("GET", tags+p, "reader")
		wantStatus(t, rec, http.StatusOK)
		if diff := cmp.Diff(map[string]any{"latest": "1.0.0"}, decode(t, rec)); diff != "" {
			t.Fatalf("%s (-want +got):\n%s", p, diff)
		}
	}
	steps := []struct {
		method, tag, body string
		expected          map[string]any
	}{
		{
			"PUT", "stable", `"1.0.0"`,
			map[string]any{"latest": "1.1.0", "beta": "2.0.0-beta.1", "stable": "1.0.0"},
		},
		// npm sends the version as typed; it cleans as npm's semver does.
		{
			"PUT", "beta", `"v1.1.0"`,
			map[string]any{"latest": "1.1.0", "beta": "1.1.0", "stable": "1.0.0"},
		},
		// Already there: nothing changes.
		{
			"PUT", "beta", `"1.1.0"`,
			map[string]any{"latest": "1.1.0", "beta": "1.1.0", "stable": "1.0.0"},
		},
		{
			"PUT", "latest", `"1.0.0"`,
			map[string]any{"latest": "1.0.0", "beta": "1.1.0", "stable": "1.0.0"},
		},
		// latest taken off goes to the version that outranks the others.
		{
			"DELETE",
			"latest",
			"",
			map[string]any{"latest": "1.1.0", "beta": "1.1.0", "stable": "1.0.0"},
		},
		{"DELETE", "stable", "", map[string]any{"latest": "1.1.0", "beta": "1.1.0"}},
	}
	for _, s := range steps {
		rec := f.send(s.method, tags+"rich/dist-tags/"+s.tag, "tagger", strings.NewReader(s.body))
		wantStatus(t, rec, http.StatusOK)
		if diff := cmp.Diff(s.expected, decode(t, rec)); diff != "" {
			t.Fatalf("%s %s (-want +got):\n%s", s.method, s.tag, diff)
		}
		if diff := cmp.Diff(s.expected, f.tagsOf(t, "rich")); diff != "" {
			t.Fatalf("%s %s: packument (-want +got):\n%s", s.method, s.tag, diff)
		}
	}
	if got := f.props(t, npmfmt.CanonicalPath("rich", "2.0.0-beta.1"))[npmfmt.PropDistTag]; got != nil {
		t.Fatalf("2.0.0-beta.1 kept %v", got)
	}
	// A caller who may annotate but read nothing is told the tags it sees:
	// none.
	rec = f.send("PUT", tags+"rich/dist-tags/blind", "blind", strings.NewReader(`"1.0.0"`))
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("blind sees %s", rec.Body)
	}
}

// A dist-tag change needs Annotate on the package's folder before anything
// is read, then on each tarball that gains or loses the tag; a tag npm
// would read as a version range, an unknown package, version or tag, and
// paths no operation serves are refused.
func TestAccDistTagRefusals(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	for _, c := range []struct {
		method, path, user, body string
		expected                 int
		msg                      string
	}{
		{"PUT", "rich/dist-tags/qa", "writer", `"1.0.0"`, http.StatusForbidden, ""},
		{"PUT", "rich/dist-tags/qa", "reader", `"1.0.0"`, http.StatusForbidden, ""},
		{"PUT", "rich/dist-tags/qa", auth.AnonymousUser, `"1.0.0"`, http.StatusUnauthorized, ""},
		{"DELETE", "rich/dist-tags/beta", "nobody", "", http.StatusForbidden, ""},
		// tagone may annotate 1.x, but beta is on 2.0.0-beta.1.
		{"PUT", "rich/dist-tags/beta", "tagone", `"1.0.0"`, http.StatusForbidden, ""},
		{"DELETE", "rich/dist-tags/beta", "tagone", "", http.StatusForbidden, ""},
		{
			"PUT", "rich/dist-tags/beta", "tagger", `"2.0.0"`, http.StatusNotFound,
			"version not found: rich@2.0.0",
		},
		{"PUT", "rich/dist-tags/beta", "tagger", `"next"`, http.StatusNotFound, "version not found"},
		{
			"PUT", "absent/dist-tags/beta", "tagger", `"1.0.0"`, http.StatusNotFound,
			"package not found: absent",
		},
		{
			"DELETE", "rich/dist-tags/nope", "tagger", "", http.StatusNotFound,
			"nope is not a dist-tag on rich",
		},
		{"PUT", "rich/dist-tags/beta", "tagger", `1.0.0`, http.StatusBadRequest, "JSON string"},
		{
			"PUT", "rich/dist-tags/1.x", "tagger", `"1.0.0"`, http.StatusBadRequest,
			"Tag name must not be a valid SemVer range: 1.x",
		},
		{"PUT", "rich/dist-tags/%5E1.0.0", "tagger", `"1.0.0"`, http.StatusBadRequest, "SemVer range"},
		{"DELETE", "rich/dist-tags/v2", "tagger", "", http.StatusBadRequest, "SemVer range"},
		{"PUT", "rich/dist-tags", "tagger", `{}`, http.StatusMethodNotAllowed, "method not allowed"},
		{"GET", "rich/dist-tags/beta", "reader", "", http.StatusNotFound, "not found"},
		{"GET", "rich", "reader", "", http.StatusNotFound, "not found"},
		{"GET", "rich/other", "reader", "", http.StatusNotFound, "not found"},
		{"GET", "", "reader", "", http.StatusNotFound, "not found"},
		{"GET", "rich;=v/dist-tags", "reader", "", http.StatusBadRequest, ""},
		{"PUT", "rich;=v/dist-tags/qa", "tagger", `"1.0.0"`, http.StatusBadRequest, ""},
		{"GET", "absent/dist-tags", "reader", "", http.StatusNotFound, "package not found"},
	} {
		rec := f.send(c.method, tags+c.path, c.user, strings.NewReader(c.body))
		if c.msg == "" {
			wantStatus(t, rec, c.expected)
			continue
		}
		wantError(t, rec, c.expected, c.msg)
	}
	if diff := cmp.Diff(map[string]any{"latest": "1.1.0", "beta": "2.0.0-beta.1"},
		f.tagsOf(t, "rich")); diff != "" {
		t.Fatalf("refusals changed the tags (-want +got):\n%s", diff)
	}
	// tagone may add a tag no other version holds to a 1.x version.
	wantStatus(t, f.send("PUT", tags+"rich/dist-tags/mine", "tagone", strings.NewReader(`"1.0.0"`)),
		http.StatusOK)
}

func TestUnitValidRange(t *testing.T) {
	for tag, want := range map[string]bool{
		"latest": false, "beta": false, "next": false, "v": false, "rc-1": false, "1.0": true,
		"1": true, "x": true, "*": true, "v1": true, "=1.2.3": true, "^1.0.0": true,
		"~1.2": true, "~>1.2": true, ">= 1.2.3": true, "<2.0.0-beta.1+b": true, "1.x || 2.x": true,
		"1.0.0 - 2.0.0": true, ">1 <3": true, ">1 beta": false, "||": true, "1.2.3.4": false,
	} {
		if got := npm.ValidRange(tag); got != want {
			t.Errorf("%q: %v", tag, got)
		}
	}
}

// writeDoc is the full packument reader reads with ?write=true, as npm
// deprecate and npm unpublish read it.
func (f *fixture) writeDoc(t *testing.T, name string) map[string]any {
	t.Helper()
	return decode(t, f.do("GET", registry+name+"?write=true", "reader"))
}

func versionsOf(doc map[string]any) map[string]any {
	v, _ := doc["versions"].(map[string]any)
	return v
}

// npm deprecate sends the packument back without attachments: each
// version's deprecated becomes its tarball's npm.deprecated, an empty
// message or one that is no string removes it, and the versions the body
// leaves out, or the index lacks, are left alone.
func TestAccDeprecate(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	doc := f.writeDoc(t, "rich")
	versionsOf(doc)["1.1.0"].(map[string]any)["deprecated"] = "use 2"
	versionsOf(doc)["1.0.0"].(map[string]any)["deprecated"] = ""
	versionsOf(doc)["9.9.9"] = map[string]any{"deprecated": "no such version"}
	versionsOf(doc)["not-a-version"] = map[string]any{"deprecated": "x"}
	rec := f.send("PUT", registry+"rich", "tagger", bytes.NewReader(encodeDoc(t, doc)))
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("body %s", rec.Body)
	}
	deprecations := func() map[string]any {
		out := map[string]any{}
		for v, m := range versionsOf(f.writeDoc(t, "rich")) {
			out[v] = m.(map[string]any)["deprecated"]
		}
		return out
	}
	want := map[string]any{"1.0.0": nil, "1.1.0": "use 2", "2.0.0-beta.1": nil}
	if diff := cmp.Diff(want, deprecations()); diff != "" {
		t.Fatalf("deprecations (-want +got):\n%s", diff)
	}
	if got := f.props(t, npmfmt.CanonicalPath("rich", "1.1.0"))[npmfmt.PropDeprecated]; !slices.Equal(
		got,
		[]string{"use 2"},
	) {
		t.Fatalf("npm.deprecated %v", got)
	}
	// Versions the body leaves out stay as they are; a deprecated that is
	// no string removes the deprecation.
	for _, body := range []string{
		`{"name":"rich","versions":{"2.0.0-beta.1":{"deprecated":"try 1.1.0"}}}`,
		`{"name":"rich"}`,
		`{"name":"rich","versions":{"v2.0.0-beta.1":{"deprecated":"try 1.1.0"}}}`,
	} {
		wantStatus(t, f.send("PUT", registry+"rich", "tagger", strings.NewReader(body)),
			http.StatusOK)
	}
	want["2.0.0-beta.1"] = "try 1.1.0"
	if diff := cmp.Diff(want, deprecations()); diff != "" {
		t.Fatalf("after a partial body (-want +got):\n%s", diff)
	}
	wantStatus(t, f.send("PUT", registry+"rich", "tagger",
		strings.NewReader(`{"versions":{"1.1.0":{"deprecated":false}}}`)), http.StatusOK)
	want["1.1.0"] = nil
	if diff := cmp.Diff(want, deprecations()); diff != "" {
		t.Fatalf("after a boolean (-want +got):\n%s", diff)
	}
}

// A packument update needs Annotate on each tarball it changes, checked
// before anything changes, and a body npm would not send is refused.
func TestAccDeprecateRefusals(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	deprecateAll := `{"versions":{"1.0.0":{"deprecated":"x"},"2.0.0-beta.1":{"deprecated":"x"}}}`
	for _, c := range []struct {
		user, path, body string
		expected         int
		msg              string
	}{
		// writer may deploy, which lets the body be read, but not annotate.
		{"writer", "rich", deprecateAll, http.StatusForbidden, ""},
		{"reader", "rich", deprecateAll, http.StatusForbidden, ""},
		{auth.AnonymousUser, "rich", deprecateAll, http.StatusUnauthorized, ""},
		// tagone may annotate 1.0.0 but not 2.0.0-beta.1.
		{"tagone", "rich", deprecateAll, http.StatusForbidden, ""},
		{"tagger", "absent", deprecateAll, http.StatusNotFound, "package not found: absent"},
		{"tagger", "rich", `{"versions":[]}`, http.StatusBadRequest, "versions is not an object"},
		{"tagger", "rich", `{"versions":null}`, http.StatusBadRequest, "versions is not an object"},
		{
			"tagger", "rich", `{"versions":{"1.0.0":"x"}}`, http.StatusBadRequest,
			"the version 1.0.0 is not an object",
		},
	} {
		rec := f.send("PUT", registry+c.path, c.user, strings.NewReader(c.body))
		if c.msg == "" {
			wantStatus(t, rec, c.expected)
			continue
		}
		wantError(t, rec, c.expected, c.msg)
	}
	if got := f.props(t, npmfmt.CanonicalPath("rich", "1.0.0"))[npmfmt.PropDeprecated]; !slices.Equal(
		got,
		[]string{"use 1.1.0"},
	) {
		t.Fatalf("a refused update changed 1.0.0: %v", got)
	}
}

// npm unpublish <name>@<version>: the PUT of the packument without the
// version removes it through the trash can, latest moves on as the npm
// format settles it, and the tarball DELETE that follows finds it gone,
// which npm takes as done. _rev is compared with nothing.
func TestAccUnpublishVersion(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	doc := f.writeDoc(t, "rich")
	delete(versionsOf(doc), "1.1.0")
	doc["dist-tags"] = map[string]any{"latest": "2.0.0-beta.1"}
	rec := f.send(
		"PUT",
		registry+"rich/-rev/stale-rev",
		"writer",
		bytes.NewReader(encodeDoc(t, doc)),
	)
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("body %s", rec.Body)
	}
	// The body's dist-tags are ignored: 1.0.0 outranks the pre-release.
	if diff := cmp.Diff(map[string]any{"latest": "1.0.0", "beta": "2.0.0-beta.1"},
		f.tagsOf(t, "rich")); diff != "" {
		t.Fatalf("dist-tags (-want +got):\n%s", diff)
	}
	path := npmfmt.CanonicalPath("rich", "1.1.0")
	trashed, err := f.db.Trash().Find(context.Background(), "npm-local", path)
	if err != nil || len(trashed) != 1 {
		t.Fatalf("trash %v %v", trashed, err)
	}
	wantStatus(t, f.do("DELETE", registry+path+"/-rev/1-0", "writer"), http.StatusNotFound)
	// The tarball DELETE alone removes a version too.
	wantStatus(t, f.do("DELETE", registry+npmfmt.CanonicalPath("rich", "1.0.0")+"/-rev/1-0",
		"writer"), http.StatusOK)
	if diff := cmp.Diff(map[string]any{"latest": "2.0.0-beta.1", "beta": "2.0.0-beta.1"},
		f.tagsOf(t, "rich")); diff != "" {
		t.Fatalf("after the tarball DELETE (-want +got):\n%s", diff)
	}
}

// npm unpublish --force removes every version of the package, scoped or
// not, however the scope's slash arrives.
func TestAccUnpublishPackage(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	f.deploy(t, "@acme/tool", "1.0.0", `{"name":"@acme/tool","version":"1.0.0"}`, nil)
	f.deploy(t, "@acme/tool", "2.0.0", `{"name":"@acme/tool","version":"2.0.0"}`, nil)
	wantStatus(t, f.do("DELETE", registry+"rich/-rev/1-0", "writer"), http.StatusOK)
	wantStatus(t, f.do("GET", registry+"rich", "reader"), http.StatusNotFound)
	if _, err := f.db.Packages().Find(context.Background(), "npm-local", "rich"); err == nil {
		t.Fatal("rich is still indexed")
	}
	wantStatus(t, f.do("DELETE", registry+"@acme/tool/-/@acme/tool-2.0.0.tgz/-rev/1-0", "writer"),
		http.StatusOK)
	wantStatus(t, f.do("DELETE", registry+"@acme%2ftool/-rev/1-0", "writer"), http.StatusOK)
	wantStatus(t, f.do("GET", registry+"@acme%2ftool", "reader"), http.StatusNotFound)
	wantStatus(t, f.do("DELETE", registry+"rich/-rev/1-0", "writer"), http.StatusNotFound)
}

// Each removal needs Delete on the tarball, checked for every tarball
// before any goes; a version the caller cannot see is never removed for
// being absent from its body; and requests npm would not send are refused.
func TestAccUnpublishRefusals(t *testing.T) {
	f := newFixture(t)
	f.deployRich(t)
	all := f.writeDoc(t, "rich")
	for _, c := range []struct {
		method, path, user, body string
		expected                 int
		msg                      string
	}{
		{"DELETE", "rich/-rev/1-0", "reader", "", http.StatusForbidden, ""},
		{"DELETE", "rich/-rev/1-0", auth.AnonymousUser, "", http.StatusUnauthorized, ""},
		{"DELETE", "rich/-rev/1-0", "narrow", "", http.StatusForbidden, ""},
		// pruner may delete 1.x, not 2.0.0-beta.1.
		{"DELETE", "rich/-rev/1-0", "pruner", "", http.StatusForbidden, ""},
		{"DELETE", "rich/-/rich-2.0.0-beta.1.tgz/-rev/1-0", "pruner", "", http.StatusForbidden, ""},
		{"DELETE", "rich/-/-rev/1-0", "writer", "", http.StatusNotFound, "Could not find resource"},
		{"DELETE", "rich", "writer", "", http.StatusMethodNotAllowed, "method not allowed"},
		{"DELETE", "rich/-rev", "writer", "", http.StatusMethodNotAllowed, "method not allowed"},
		{"DELETE", "rich/x/-rev/1-0", "writer", "", http.StatusNotFound, "not found"},
		{"DELETE", "-/-rev/1-0", "writer", "", http.StatusNotFound, "not found"},
		{"DELETE", "rich;=v/-rev/1-0", "writer", "", http.StatusBadRequest, ""},
		{"DELETE", "absent/-rev/1-0", "writer", "", http.StatusNotFound, "package not found: absent"},
		{"PUT", "rich/-rev/1-0", "reader", "{}", http.StatusForbidden, ""},
		{
			"PUT", "rich/-rev/1-0", "writer", `{"name":"rich"}`, http.StatusBadRequest,
			"names no versions",
		},
		{
			"PUT", "rich/-rev/1-0", "writer", `{"versions":7}`, http.StatusBadRequest,
			"versions is not an object",
		},
		{
			"PUT", "rich/-rev/1-0", "writer", `{"versions":{}, "_attachments":{"a.tgz":{"data":"eA=="}}}`,
			http.StatusBadRequest, "an unpublish carries no tarball",
		},
		{"PUT", "rich/-rev/1-0", "writer", `{"versions":`, http.StatusBadRequest, "malformed"},
		{
			"PUT", "absent/-rev/1-0", "writer", `{"versions":{}}`, http.StatusNotFound,
			"package not found",
		},
		{"PUT", "rich/-rev/1-0", "tagone", `{"versions":{}}`, http.StatusForbidden, ""},
	} {
		rec := f.send(c.method, registry+c.path, c.user, strings.NewReader(c.body))
		if c.msg == "" {
			wantStatus(t, rec, c.expected)
			continue
		}
		wantError(t, rec, c.expected, c.msg)
	}
	if diff := cmp.Diff(versionsOf(all), versionsOf(f.writeDoc(t, "rich"))); diff != "" {
		t.Fatalf("a refused unpublish removed versions (-before +after):\n%s", diff)
	}
	// pruner sees rich's 1.x versions only: keeping 1.0.0 removes 1.1.0,
	// and leaves 2.0.0-beta.1, which its body could not name.
	wantStatus(t, f.send("PUT", registry+"rich/-rev/1-0", "pruner",
		strings.NewReader(`{"versions":{"1.0.0":{}}}`)), http.StatusOK)
	got := slices.Sorted(func(yield func(string) bool) {
		for v := range versionsOf(f.writeDoc(t, "rich")) {
			if !yield(v) {
				return
			}
		}
	})
	if !slices.Equal(got, []string{"1.0.0", "2.0.0-beta.1"}) {
		t.Fatalf("versions left %v", got)
	}
	// An unpublish is bounded as a publish is.
	f.remount(t, npm.Options{MaxPublishSize: 8})
	rec := f.send("PUT", registry+"rich/-rev/1-0", "writer",
		strings.NewReader(`{"versions":{"1.0.0":{}}}`))
	wantStatus(t, rec, http.StatusRequestEntityTooLarge)
}

// A store that fails while a lifecycle request reads the index is a
// fault.
func TestAccLifecycleStoreFaults(t *testing.T) {
	for _, c := range []struct {
		failing, method, path, body string
	}{
		{"find", "PUT", tags + "rich/dist-tags/qa", `"1.0.0"`},
		{"versions", "DELETE", registry + "rich/-rev/1-0", ""},
		{"versions", "PUT", registry + "rich", `{"versions":{}}`},
	} {
		f := newFixture(t, func(st store.Store) store.Store {
			return failingStore{Store: st, failing: c.failing}
		})
		f.deployRich(t)
		wantStatus(t, f.send(c.method, c.path, "admin", strings.NewReader(c.body)),
			http.StatusInternalServerError)
	}
	// A version the index names whose tarball is gone is a fault too.
	f := newFixture(t)
	f.deployRich(t)
	ctx := context.Background()
	if _, err := f.db.Items().Delete(ctx, "npm-local", false,
		npmfmt.CanonicalPath("rich", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.send("PUT", tags+"rich/dist-tags/qa", "admin", strings.NewReader(`"1.0.0"`)),
		http.StatusInternalServerError)
	var doc map[string]any
	_ = json.Unmarshal([]byte(`{"versions":{"1.0.0":{"deprecated":"x"}}}`), &doc)
	wantStatus(t, f.send("PUT", registry+"rich", "admin", bytes.NewReader(encodeDoc(t, doc))),
		http.StatusInternalServerError)
}
