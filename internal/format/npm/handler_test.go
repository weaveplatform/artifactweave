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
// time, and it gets the npm.* properties package.json gives.
func TestDeployIndexes(t *testing.T) {
	w := newWorld(t)
	fields := `"description":"pads <left>","keywords":["pad","left"],"scripts":{"install":"make"}`
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
			FileCount: 2, UnpackedSize: int64(len(manifest) + len("module.exports = 1")),
		},
		HasInstallScript: true,
		Published:        w.now,
	}}}
	if diff := cmp.Diff(want, w.index()); diff != "" {
		t.Fatalf("index (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(types.Properties{
		npm.PropName: {"@acme/left-pad"}, npm.PropVersion: {"1.3.0"},
		npm.PropDescription: {"pads <left>"}, npm.PropKeywords: {`["pad","left"]`},
	}, w.props(p)); diff != "" {
		t.Fatalf("properties (-want +got):\n%s", diff)
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
func TestDistTagsMove(t *testing.T) {
	w := newWorld(t)
	p1, p2 := "left-pad/-/left-pad-1.0.0.tgz", "left-pad/-/left-pad-2.0.0.tgz"
	w.deploy(p1, pkg("left-pad", "1.0.0", ""), types.Properties{npm.PropDistTag: {"beta", "rc"}})
	w.deploy(p2, pkg("left-pad", "2.0.0", ""), types.Properties{npm.PropDistTag: {"beta"}})
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"rc"}, "2.0.0": {"beta"}},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("after deploy (-want +got):\n%s", diff)
	}
	w.annotate(p1, types.Properties{npm.PropDistTag: {"beta", "rc", "next", ""}})
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"beta", "next", "rc"}, "2.0.0": nil},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("after annotate (-want +got):\n%s", diff)
	}
	if _, ok := w.props(p2)[npm.PropDistTag]; ok {
		t.Fatalf("2.0.0 kept its tags: %v", w.props(p2))
	}
	w.annotate(p1, nil, npm.PropDistTag)
	if diff := cmp.Diff(map[string][]string{"1.0.0": nil, "2.0.0": nil},
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

// Only a local repository has tarballs to reindex.
func TestReindexLocalOnly(t *testing.T) {
	w := newWorld(t)
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		err := npm.Handler{}.Reindex(w.ctx, format.Env{Store: w.st}, repo.Config{
			Key: "npm-" + string(class), Class: class, PackageType: repo.NPM,
		})
		var refused *format.Rejection
		if !errors.As(err, &refused) || refused.Status != http.StatusBadRequest {
			t.Errorf("%s: %v", class, err)
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
// transaction.
func TestInspect(t *testing.T) {
	w := newWorld(t)
	h := npm.Handler{}
	tarball := w.deploy("x/-/x-1.0.0.tgz", pkg("x", "1.0.0", ""), nil)
	garbage := w.deploy("y/-/y-1.0.0.tgz", []byte("garbage"), nil)
	env := format.Env{Blobs: w.items.Blobs}
	for _, c := range []struct {
		w    format.Write
		want bool
	}{
		{format.Write{Op: format.OpDeploy, Path: "x/-/x-1.0.0.tgz", Checksums: tarball.Actual}, true},
		{format.Write{Op: format.OpCopy, Path: "z/-/z-1.0.0.tgz", Checksums: tarball.Actual}, true},
		{format.Write{Op: format.OpDeploy, Path: "x/x-1.0.0.tgz", Checksums: tarball.Actual}, false},
		{format.Write{Op: format.OpChecksum, Path: "x/-/x-1.0.0.tgz"}, false},
		{format.Write{Op: format.OpDeploy, Path: "y/-/y-1.0.0.tgz", Checksums: garbage.Actual}, false},
	} {
		got, err := h.Inspect(w.ctx, env, w.c, c.w)
		if err != nil {
			t.Fatal(err)
		}
		if tb, ok := got.(*npm.Tarball); ok != c.want || ok && tb.SHA256 != tarball.Actual.SHA256 {
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

// npm repositories keep the deploy policy of every package type.
func TestAdmitIsGeneric(t *testing.T) {
	blacked := true
	c := repo.Config{Key: "npm-local", PackageType: repo.NPM, BlackedOut: &blacked}
	_, err := npm.Handler{}.Admit(context.Background(), format.Env{}, c,
		format.Write{Op: format.OpDeploy, Path: "x/-/x-1.0.0.tgz"})
	var refused *format.Rejection
	if !errors.As(err, &refused) || refused.Status != http.StatusNotFound {
		t.Fatalf("admit %v", err)
	}
}

func TestServes(t *testing.T) {
	if !npm.Serves(repo.NPM) || npm.Serves(repo.Generic) {
		t.Fatal("serves")
	}
}
