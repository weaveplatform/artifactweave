package npm_test

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // npm's shasum is SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// world is an npm local repository with the item service over it and a
// clock that moves a minute on every write.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	items *item.Service
	c     repo.Config
	now   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return newWorldOn(t, inmem.New())
}

func newWorldOn(t *testing.T, st store.Store) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: st,
		now: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC),
	}
	w.items = &item.Service{Store: st, Blobs: blobs, Now: func() time.Time { return w.now }}
	w.c = repo.Config{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM}
	repo.Defaults(&w.c)
	if err := st.Repos().Create(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	return w
}

// pkg is a tarball of package.json with fields after its name and version.
func pkg(name, version, fields string) []byte {
	if fields != "" {
		fields = "," + fields
	}
	return tgz(
		file{
			name: "package/package.json",
			body: fmt.Sprintf(`{"name":%q,"version":%q%s}`, name, version, fields),
		},
		file{name: "package/index.js", body: "module.exports = 1"},
	)
}

func (w *world) deploy(p string, body []byte, props types.Properties) types.Item {
	w.t.Helper()
	it, err := w.tryDeploy(p, body, props)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) tryDeploy(p string, body []byte, props types.Properties) (types.Item, error) {
	w.now = w.now.Add(time.Minute)
	return w.items.Deploy(w.ctx, item.Deploy{ //nolint:wrapcheck // test helper
		Target: item.Target{Repo: w.c, Path: p}, Body: bytes.NewReader(body),
		Properties: props, By: "tester",
	})
}

func (w *world) annotate(p string, set types.Properties, remove ...string) {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.items.Annotate(w.ctx, w.c, it, set, remove); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) props(p string) types.Properties {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	props, err := w.st.Items().Properties(w.ctx, it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return props
}

// propsTime is when the properties of the item at p last changed.
func (w *world) propsTime(p string) time.Time {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	return it.PropertiesModified
}

// index is the repository's index: each package's versions with their
// Metadata.
func (w *world) index() map[string]map[string]npm.Metadata {
	w.t.Helper()
	out := map[string]map[string]npm.Metadata{}
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		vs, err := w.st.Packages().Versions(w.ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		out[p.Name] = map[string]npm.Metadata{}
		for _, v := range vs {
			m, err := npm.DecodeMetadata(v.Metadata)
			if err != nil {
				w.t.Fatal(err)
			}
			out[p.Name][v.Version] = m
		}
	}
	return out
}

// tags is each indexed version's dist-tags.
func (w *world) tags(name string) map[string][]string {
	w.t.Helper()
	out := map[string][]string{}
	for v, m := range w.index()[name] {
		out[v] = m.Tags
	}
	return out
}

// A tarball at its canonical path is a version whose Metadata holds its
// package.json, its dist facts from the stored checksums and its deploy
// time, and it gets the npm.* properties package.json gives. The first
// version of a package is its latest, stored as 7.x stores it, as
// npm.disttag=latest.
func TestDeployIndexes(t *testing.T) {
	w := newWorld(t)
	fields := `"description":"pads <left>","keywords":["pad","left"],"scripts":{"install":"make"},` +
		`"license":"Python-2.0"`
	body := pkg("@acme/left-pad", "v1.3.0", fields)
	p := "@acme/left-pad/-/@acme/left-pad-1.3.0.tgz"
	w.deploy(p, body, nil)
	manifest := `{"name":"@acme/left-pad","version":"v1.3.0",` + fields + `}`
	sha1Sum, sha512Sum := sha1.Sum(body), sha512.Sum512(body) //nolint:gosec // npm's shasum
	want := map[string]map[string]npm.Metadata{"@acme/left-pad": {"1.3.0": {
		Manifest: []byte(manifest),
		Dist: npm.Dist{
			Shasum:    hex.EncodeToString(sha1Sum[:]),
			Integrity: "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:]),
		},
		HasInstallScript: true,
		Tags:             []string{"latest"},
		Published:        w.now,
	}}}
	if diff := cmp.Diff(want, w.index()); diff != "" {
		t.Fatalf("index (-want +got):\n%s", diff)
	}
	// artifactory.licenses as RTFACT-30834's 7.x log writes it beside the
	// npm.* properties.
	if diff := cmp.Diff(types.Properties{
		npm.PropName: {"@acme/left-pad"}, npm.PropVersion: {"1.3.0"},
		npm.PropDescription: {"pads <left>"}, npm.PropKeywords: {`["pad","left"]`},
		npm.PropLicenses: {"Python-2.0"}, npm.PropDistTag: {"latest"},
	}, w.props(p)); diff != "" {
		t.Fatalf("properties (-want +got):\n%s", diff)
	}
	// The properties package.json gives are part of the deploy.
	if got := w.propsTime(p); !got.Equal(w.now) {
		t.Fatalf("properties time %v, want the deploy's %v", got, w.now)
	}
	// A redeploy of the same content changes nothing but the time.
	w.deploy(p, body, nil)
	if got := w.index()["@acme/left-pad"]["1.3.0"].Published; !got.Equal(w.now) {
		t.Fatalf("published %v, want %v", got, w.now)
	}
}

// Values taken from package.json are cut as the store cuts every property
// value (types.PropertyValueMax), and keywords that do not fit are left out
// of npm.keywords so it stays JSON; Metadata keeps all of them.
func TestLongPropertyValues(t *testing.T) {
	w := newWorld(t)
	long := strings.Repeat("é", types.PropertyValueMax+1000)
	many := make([]string, 0, 400)
	for i := range 400 {
		many = append(many, fmt.Sprintf("keyword-%03d", i))
	}
	kw, _ := json.Marshal(many)
	p := "x/-/x-1.0.0.tgz"
	w.deploy(p, pkg("x", "1.0.0", `"description":"`+long+`","keywords":`+string(kw)), nil)
	props := w.props(p)
	if d := props[npm.PropDescription][0]; d != long[:2*types.PropertyValueMax] {
		t.Errorf("description of %d bytes", len(d))
	}
	var got []string
	if err := json.Unmarshal([]byte(props[npm.PropKeywords][0]), &got); err != nil ||
		len(got) == 0 || len(got) == len(many) || !slices.Equal(got, many[:len(got)]) ||
		len(props[npm.PropKeywords][0]) > types.PropertyValueMax {
		t.Errorf("keywords %d %v", len(got), err)
	}
	w.deploy(
		p,
		pkg("x", "1.0.0", `"keywords":["`+strings.Repeat("k", types.PropertyValueMax)+`"]`),
		nil,
	)
	if _, ok := w.props(p)[npm.PropKeywords]; ok {
		t.Error("kept a keyword that cannot fit")
	}
}

// artifactory.licenses follows package.json's license when it has one,
// and is otherwise left as it is: other tools write it too, so a value the
// format did not write is not removed.
func TestLicenses(t *testing.T) {
	w := newWorld(t)
	p := "left-pad/-/left-pad-1.0.0.tgz"
	w.deploy(p, pkg("left-pad", "1.0.0", `"license":"MIT"`), nil)
	w.deploy(p, pkg("left-pad", "1.0.0", `"license":"ISC"`), nil)
	if got := w.props(p)[npm.PropLicenses]; !slices.Equal(got, []string{"ISC"}) {
		t.Fatalf("after a new license: %v", got)
	}
	w.deploy(p, pkg("left-pad", "1.0.0", ""), nil)
	if got := w.props(p)[npm.PropLicenses]; !slices.Equal(got, []string{"ISC"}) {
		t.Fatalf("after no license: %v", got)
	}
	other := "other/-/other-1.0.0.tgz"
	w.deploy(other, pkg("other", "1.0.0", ""), types.Properties{npm.PropLicenses: {"Apache-2.0"}})
	if got := w.props(other)[npm.PropLicenses]; !slices.Equal(got, []string{"Apache-2.0"}) {
		t.Fatalf("a deploy's own license: %v", got)
	}
	long := strings.Repeat("L", types.PropertyValueMax+10)
	w.deploy(other, pkg("other", "1.0.0", `"license":"`+long+`"`), nil)
	if got := w.props(other)[npm.PropLicenses]; len(got) != 1 ||
		got[0] != long[:types.PropertyValueMax] {
		t.Fatalf("a long license: %d values", len(got))
	}
}

// Strict layout: a tarball anywhere but its canonical path, or one the
// format cannot read, is stored but not indexed and gets no properties.
func TestDeployOffCanonicalPath(t *testing.T) {
	w := newWorld(t)
	body := pkg("left-pad", "1.3.0", "")
	for _, p := range []string{
		"left-pad/-/left-pad-9.9.9.tgz", "left-pad/left-pad-1.3.0.tgz", "left-pad-1.3.0.tgz",
		"left-pad/-/left-pad-1.3.0.tar.gz",
	} {
		w.deploy(p, body, nil)
		if props := w.props(p); len(props) > 0 {
			t.Errorf("%s: %v", p, props)
		}
	}
	w.deploy("x/-/x-1.0.0.tgz", []byte("not a tarball"), nil)
	if idx := w.index(); len(idx) > 0 {
		t.Fatalf("indexed %v", idx)
	}
}

// Overwriting a tarball re-reads it: content that is another version's, or
// none, drops the version, and its package with its last version; a
// description and keywords package.json no longer has are removed.
func TestOverwrite(t *testing.T) {
	w := newWorld(t)
	p1, p2 := "left-pad/-/left-pad-1.0.0.tgz", "left-pad/-/left-pad-2.0.0.tgz"
	w.deploy(p1, pkg("left-pad", "1.0.0", `"description":"d","keywords":"a, b"`), nil)
	w.deploy(p2, pkg("left-pad", "2.0.0", ""), nil)
	if got := w.props(p1)[npm.PropKeywords]; !cmp.Equal(got, []string{`["a","b"]`}) {
		t.Fatalf("keywords %v", got)
	}
	w.deploy(p1, pkg("left-pad", "1.0.0", ""), nil)
	if got := w.props(p1); len(got) != 2 || got[npm.PropDescription] != nil {
		t.Fatalf("properties %v", got)
	}
	w.deploy(p1, pkg("left-pad", "3.0.0", ""), nil)
	if idx := w.index(); len(idx["left-pad"]) != 1 {
		t.Fatalf("index %v", idx)
	}
	w.deploy(p2, []byte("garbage"), nil)
	if idx := w.index(); len(idx) > 0 {
		t.Fatalf("index %v", idx)
	}
}

// A dist-tag names one version: a tarball that gains one, by a deploy's
// matrix parameters (jf npm publish --tag) or any property write, takes it
// from the package's other versions, and both versions' Metadata follow.
// The package's first version takes latest too, tagged or not, and a
// tagged deploy leaves it there.
func TestDistTagsMove(t *testing.T) {
	w := newWorld(t)
	p1, p2 := "left-pad/-/left-pad-1.0.0.tgz", "left-pad/-/left-pad-2.0.0.tgz"
	w.deploy(p1, pkg("left-pad", "1.0.0", ""), types.Properties{npm.PropDistTag: {"beta", "rc"}})
	w.deploy(p2, pkg("left-pad", "2.0.0", ""), types.Properties{npm.PropDistTag: {"beta"}})
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"latest", "rc"}, "2.0.0": {"beta"}},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("after deploy (-want +got):\n%s", diff)
	}
	w.now = w.now.Add(time.Hour)
	w.annotate(p2, types.Properties{npm.PropDistTag: {"beta", "rc", "next", ""}})
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"latest"}, "2.0.0": {"beta", "next", "rc"}},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("after annotate (-want +got):\n%s", diff)
	}
	if got := w.props(p1)[npm.PropDistTag]; !slices.Equal(got, []string{"latest"}) {
		t.Fatalf("1.0.0 kept %v", got)
	}
	// The tag taken from 1.0.0 changed its properties as part of the same
	// write.
	if got := w.propsTime(p1); !got.Equal(w.now) {
		t.Fatalf("1.0.0's properties time %v, want the annotation's %v", got, w.now)
	}
	w.annotate(p2, nil, npm.PropDistTag)
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"latest"}, "2.0.0": nil},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("after removing (-want +got):\n%s", diff)
	}
}

// Deprecation is a property the index follows; an empty message is none.
func TestDeprecation(t *testing.T) {
	w := newWorld(t)
	p := "left-pad/-/left-pad-1.0.0.tgz"
	w.deploy(p, pkg("left-pad", "1.0.0", ""), nil)
	for _, c := range []struct {
		set    types.Properties
		remove []string
		want   string
	}{
		{types.Properties{npm.PropDeprecated: {"use 2"}}, nil, "use 2"},
		{types.Properties{npm.PropDeprecated: {""}}, nil, ""},
		{types.Properties{npm.PropDeprecated: {"gone"}}, nil, "gone"},
		{nil, []string{npm.PropDeprecated}, ""},
	} {
		w.annotate(p, c.set, c.remove...)
		if got := w.index()["left-pad"]["1.0.0"].Deprecated; got != c.want {
			t.Errorf("%v %v: %q, want %q", c.set, c.remove, got, c.want)
		}
	}
}

// Property changes the index does not follow, to folders, or to tarballs
// that are not indexed, leave the index alone.
func TestAnnotatedIgnores(t *testing.T) {
	w := newWorld(t)
	p := "left-pad/-/left-pad-1.0.0.tgz"
	w.deploy(p, pkg("left-pad", "1.0.0", ""), nil)
	stray := "left-pad/-/left-pad-1.0.0-copy.tgz"
	w.deploy(stray, pkg("left-pad", "1.0.0", ""), nil)
	before := w.index()
	w.annotate(p, types.Properties{"build.name": {"x"}})
	w.annotate("left-pad", types.Properties{npm.PropDistTag: {"beta"}})
	w.annotate(stray, types.Properties{npm.PropDistTag: {"beta"}})
	if diff := cmp.Diff(before, w.index()); diff != "" {
		t.Fatalf("index changed (-before +after):\n%s", diff)
	}
}

// A reindex rebuilds the index from the tarballs and their properties.
func TestReindex(t *testing.T) {
	w := newWorld(t)
	w.deploy("left-pad/-/left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""),
		types.Properties{npm.PropDistTag: {"beta"}, npm.PropDeprecated: {"old"}})
	w.deploy("@a/b/-/@a/b-2.0.0.tgz", pkg("@a/b", "2.0.0", `"description":"b"`), nil)
	w.deploy("@a/b/-/@a/b-1.0.0.tgz", pkg("@a/b", "1.0.0", ""), nil)
	// Redeployed, 2.0.0 is now the later of the two, though the tree lists
	// it first.
	w.deploy("@a/b/-/@a/b-2.0.0.tgz", pkg("@a/b", "2.0.0", `"description":"b2"`), nil)
	w.deploy("stray/-/stray-1.0.0.tgz", pkg("other", "1.0.0", ""), nil)
	w.deploy("readme.md", []byte("hi"), nil)
	want := w.index()
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, w.index()); diff != "" {
		t.Fatalf("reindexed (-want +got):\n%s", diff)
	}
	b, err := w.st.Packages().Find(w.ctx, w.c.Key, "@a/b")
	if err != nil || !b.Created.Equal(time.Date(2026, 10, 17, 9, 3, 0, 0, time.UTC)) {
		t.Fatalf("@a/b dated %v %v", b.Created, err)
	}
}

// Only a remote repository's cache is indexed (pkgindex.Stored): a tarball
// in the remote's own items, which no download stores there, joins no
// version, just as a reindex would not read it. The cache's tarball does.
func TestRemoteIndexesOnlyItsCache(t *testing.T) {
	w := newWorld(t)
	r := repo.Config{
		Key: "npm-remote", Class: repo.Remote, URL: "https://registry.npmjs.org",
		PackageType: repo.NPM,
	}
	repo.Defaults(&r)
	if err := w.st.Repos().Create(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		w.now = w.now.Add(time.Minute)
		if _, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: r, Cached: cached, Path: "left-pad/-/left-pad-1.0.0.tgz"},
			Body:   bytes.NewReader(pkg("left-pad", "1.0.0", "")),
		}); err != nil {
			t.Fatal(err)
		}
		_, err := w.st.Packages().Find(w.ctx, r.Key, "left-pad")
		if indexed := err == nil; indexed != cached {
			t.Fatalf("cached %v: indexed %v (%v)", cached, indexed, err)
		}
	}
}

// Content a tarball's item names but the blob store no longer holds is a
// failure, whether a reindex or a restore meets it.
func TestMissingContent(t *testing.T) {
	w := newWorld(t)
	it := w.deploy("left-pad/-/left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), nil)
	if err := w.items.Blobs.Delete(w.ctx, it.Actual.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := w.items.Reindex(w.ctx, w.c); err == nil {
		t.Fatal("reindexed without the content")
	}
	env := format.Env{Store: w.st, Blobs: w.items.Blobs}
	if err := (npm.Handler{}).Deployed(w.ctx, env, w.c, it); err == nil {
		t.Fatal("indexed without the content")
	}
}

// Inspect reads only what could be a canonical tarball, before the write's
// transaction, and notes whether the write sets its own dist-tags, which
// content that is no tarball needs as much as a tarball.
func TestInspect(t *testing.T) {
	w := newWorld(t)
	h := npm.Handler{}
	tarball := w.deploy("x/-/x-1.0.0.tgz", pkg("x", "1.0.0", ""), nil)
	garbage := w.deploy("y/-/y-1.0.0.tgz", []byte("garbage"), nil)
	env := format.Env{Blobs: w.items.Blobs}
	tagged := types.Properties{npm.PropDistTag: {"beta"}}
	for _, c := range []struct {
		w               format.Write
		inspected, read bool
		ownTags         bool
	}{
		{
			format.Write{Op: format.OpDeploy, Path: "x/-/x-1.0.0.tgz", Checksums: tarball.Actual},
			true, true, false,
		},
		{
			format.Write{
				Op: format.OpCopy, Path: "z/-/z-1.0.0.tgz", Checksums: tarball.Actual,
				Properties: tagged,
			},
			true, true, true,
		},
		{
			format.Write{Op: format.OpDeploy, Path: "x/x-1.0.0.tgz", Checksums: tarball.Actual},
			false, false, false,
		},
		{format.Write{Op: format.OpChecksum, Path: "x/-/x-1.0.0.tgz"}, false, false, false},
		{
			format.Write{
				Op: format.OpDeploy, Path: "y/-/y-1.0.0.tgz", Checksums: garbage.Actual,
				Properties: tagged,
			},
			true, false, true,
		},
	} {
		got, err := h.Inspect(w.ctx, env, w.c, c.w)
		if err != nil {
			t.Fatal(err)
		}
		tb, own, ok := npm.Inspected(got)
		if ok != c.inspected || (tb != nil) != c.read || own != c.ownTags ||
			tb != nil && tb.SHA256 != tarball.Actual.SHA256 {
			t.Errorf("%+v: %#v", c.w, got)
		}
	}
	_, err := h.Inspect(w.ctx, env, w.c, format.Write{
		Op: format.OpDeploy, Path: "x/-/x-1.0.0.tgz",
		Checksums: types.Checksums{SHA256: hex.EncodeToString(make([]byte, 32))},
	})
	if err == nil {
		t.Fatal("inspected content that is not stored")
	}
}

// npm repositories keep the deploy policy of every package type, with the
// release and snapshot handling between its checks, in Artifactory's
// order: blacked out, then the handling, then the patterns.
func TestAdmitPolicies(t *testing.T) {
	on, off, all := true, false, "**"
	deploy := format.Write{Op: format.OpDeploy, Path: "x/-/x-1.0.0-beta.1.tgz"}
	for _, c := range []struct {
		name string
		c    repo.Config
		w    format.Write
		// want is the refusal's message as far as it tells the checks
		// apart; "" admits the write.
		want string
	}{
		{"blacked out", repo.Config{BlackedOut: &on, HandleSnapshots: &off}, deploy, "is blacked out"},
		{
			"handling",
			repo.Config{HandleSnapshots: &off, ExcludesPattern: &all},
			deploy,
			"snapshot release handling",
		},
		{"patterns", repo.Config{ExcludesPattern: &all}, deploy, "include/exclude pattern"},
		{"admitted", repo.Config{HandleReleases: &off}, deploy, ""},
		// A checksum upload is exempt from the handling, not the patterns.
		{
			"checksum",
			repo.Config{HandleSnapshots: &off},
			format.Write{Op: format.OpChecksum, Path: deploy.Path},
			"",
		},
		{
			"checksum patterns",
			repo.Config{HandleSnapshots: &off, ExcludesPattern: &all},
			format.Write{Op: format.OpChecksum, Path: deploy.Path},
			"include/exclude pattern",
		},
	} {
		c.c.Key, c.c.PackageType = "npm-local", repo.NPM
		got, err := npm.Handler{}.Admit(context.Background(), format.Env{}, c.c, c.w)
		var refused *format.Rejection
		switch {
		case c.want == "" && (err != nil || got.Path != c.w.Path):
			t.Errorf("%s: %+v %v", c.name, got, err)
		case c.want != "" && (!errors.As(err, &refused) || !strings.Contains(refused.Message, c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// handleReleases and handleSnapshots refuse an npm tarball with 409 and
// the message 7.x gives (KB 000006800), a pre-release being a snapshot.
// The version is read from the path, so a copy or move is held to it too,
// and a path that names no version this way is exempt.
func TestReleaseSnapshotPolicy(t *testing.T) {
	w := newWorld(t)
	off := false
	releases, snapshots := w.c, w.c
	releases.Key, releases.HandleSnapshots = "npm-releases", &off
	snapshots.Key, snapshots.HandleReleases = "npm-snapshots", &off
	for _, c := range []repo.Config{releases, snapshots} {
		if err := w.st.Repos().Create(w.ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	try := func(c repo.Config, p, version string) error {
		w.now = w.now.Add(time.Minute)
		_, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: p},
			Body:   bytes.NewReader(pkg("x", version, "")),
		})
		return err
	}
	for _, c := range []struct {
		repo    repo.Config
		path    string
		refused bool
	}{
		{releases, "x/-/x-1.0.0.tgz", false},
		{releases, "x/-/x-1.0.0+build.1.tgz", false},
		{releases, "x/-/x-1.0.0-beta.1.tgz", true},
		{snapshots, "x/-/x-1.0.0-beta.1.tgz", false},
		{snapshots, "x/-/x-1.0.0.tgz", true},
		{snapshots, "@s/x/-/@s/x-1.0.0.tgz", true},
		// Paths that name no version are exempt, as 7.x exempts a path
		// its layout cannot read.
		{snapshots, "x/x-1.0.0.tgz", false},
		{snapshots, "x/-/y-1.0.0.tgz", false},
		{snapshots, "x/-/x-1.0.0.tar.gz", false},
		{snapshots, "x/-/x-latest.tgz", false},
		{snapshots, ".x/-/.x-1.0.0.tgz", false},
		{snapshots, "readme.md", false},
	} {
		err := try(c.repo, c.path, "1.0.0")
		var refused *format.Rejection
		switch {
		case !c.refused && err != nil:
			t.Errorf("%s %s: %v", c.repo.Key, c.path, err)
		case c.refused && (!errors.As(err, &refused) || refused.Status != http.StatusConflict ||
			refused.Message != "The repository '"+c.repo.Key+"' rejected the resolution of an "+
				"artifact '"+c.repo.Key+":"+c.path+"' due to conflict in the snapshot release "+
				"handling policy."):
			t.Errorf("%s %s: %v", c.repo.Key, c.path, err)
		}
	}
	// A copy lands only where the target's handling allows it.
	src := w.deploy("x/-/x-2.0.0.tgz", pkg("x", "2.0.0", ""), nil)
	for _, c := range []struct {
		to     repo.Config
		failed int
	}{{releases, 0}, {snapshots, 1}} {
		res, err := w.items.Transfer(w.ctx, item.Transfer{
			From: item.Target{Repo: w.c, Path: src.FullPath()},
			To:   item.Target{Repo: c.to, Path: src.FullPath()},
		})
		if err != nil || res.Failed != c.failed || c.failed > 0 &&
			!strings.Contains(res.Messages[0].Text, "snapshot release handling") {
			t.Errorf("copy to %s: %+v %v", c.to.Key, res, err)
		}
	}
}

func TestServes(t *testing.T) {
	if !npm.Serves(repo.NPM) || npm.Serves(repo.Generic) {
		t.Fatal("serves")
	}
}
