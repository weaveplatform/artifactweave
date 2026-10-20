package pypi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A reindex from an empty index rebuilds it from the files and their
// properties alone, the project dated by its earliest file, whichever
// version that is.
func TestReindexFromEmpty(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	first := w.deploy("x/x-2.0.zip", zipOf(entry{"PKG-INFO", meta("x", "2.0")}), nil)
	w.deploy("x/x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0")}),
		types.Properties{pypi.PropYanked: {"bad"}})
	w.deploy("m/m.tar.gz", []byte("x"), types.Properties{
		pypi.PropName: {"m"}, pypi.PropVersion: {"1"}, pypi.PropRequiresPython: {">=3"},
	})
	kept := w.index()
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
	if diff := cmp.Diff(kept, w.index()); diff != "" {
		t.Fatalf("(-kept +rebuilt):\n%s", diff)
	}
	p, err := w.st.Packages().Find(w.ctx, w.c.Key, "x")
	if err != nil || !p.Created.Equal(first.Modified) {
		t.Fatalf("project %+v %v, want created %s", p, err, first.Modified)
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A distribution is indexed by its core metadata wherever it is stored, as
// a file of its project's version: the project is the normalised name, the
// version PEP 440's normal form, and the files are listed by file name,
// then path. Its record carries what the pages and the JSON API need, and
// its version keeps one copy of each core metadata text.
func TestIndexWherever(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	m := meta("Jfrog.PyPI", "1.0-RC1", "Summary: Tools", "Requires-Python: >=3.9")
	whl := w.deploy("jfrog-pypi/1.0-RC1/"+wheelName("Jfrog.PyPI", "1.0-RC1"),
		wheel("Jfrog.PyPI", "1.0-RC1", m), nil)
	tgz := w.deploy("dist/jfrog_pypi-1.0-RC1.tar.gz", sdist("Jfrog.PyPI", "1.0-RC1", m), nil)
	again := w.deploy("other/jfrog_pypi-1.0-RC1.tar.gz", sdist("Jfrog.PyPI", "1.0-RC1", m), nil)
	w.deploy("jfrog-pypi/1.0-RC1/README.md", []byte("read me"), nil)

	idx := w.index()
	got, ok := idx["jfrog-pypi"]["1.0rc1"]
	if len(idx) != 1 || len(idx["jfrog-pypi"]) != 1 || !ok {
		t.Fatalf("index %v", idx)
	}
	record := func(it types.Item, core bool) pypi.File {
		return pypi.File{
			Path: it.FullPath(), Name: it.Name, SHA256: it.Actual.SHA256, MD5: it.Actual.MD5,
			Size: it.Size, Uploaded: it.Modified.UTC(), RequiresPython: ">=3.9",
			Metadata: sha(m), Core: core,
		}
	}
	want := pypi.Metadata{
		Files: []pypi.File{record(whl, true), record(tgz, false), record(again, false)},
		Texts: map[string]string{sha(m): m},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("metadata (-want +got):\n%s", diff)
	}
	if got.Text(got.Files[0]) != m || got.Text(pypi.File{}) != "" {
		t.Errorf("text %q", got.Text(got.Files[0]))
	}
	want2 := types.Properties{
		pypi.PropName: {"Jfrog.PyPI"}, pypi.PropVersion: {"1.0-RC1"},
		pypi.PropNormalizedName: {"jfrog-pypi"}, pypi.PropSummary: {"Tools"},
		pypi.PropRequiresPython: {">=3.9"},
	}
	for _, it := range []types.Item{whl, tgz, again} {
		if diff := cmp.Diff(want2, w.props(it.FullPath())); diff != "" {
			t.Errorf("%s (-want +got):\n%s", it.FullPath(), diff)
		}
	}
	if props := w.props("jfrog-pypi/1.0-RC1/README.md"); len(props) != 0 {
		t.Errorf("README %v", props)
	}
}

// The version is dated by its earliest file, and a delete of that file
// moves the date on and drops its record, and its text once no record
// names it.
func TestVersionDate(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	first := w.deploy("a/x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0", "Summary: a")}), nil)
	second := w.deploy("b/x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0", "Summary: b")}), nil)
	created := func() time.Time {
		p, err := w.st.Packages().Find(w.ctx, w.c.Key, "x")
		if err != nil {
			t.Fatal(err)
		}
		v, err := w.st.Packages().Version(w.ctx, p.ID, "1.0")
		if err != nil {
			t.Fatal(err)
		}
		return v.Created
	}
	if !created().Equal(first.Modified) {
		t.Fatalf("created %s, want %s", created(), first.Modified)
	}
	w.remove("a/x-1.0.zip")
	m := w.index()["x"]["1.0"]
	if !created().Equal(second.Modified) || !slices.Equal(paths(m), []string{"b/x-1.0.zip"}) ||
		len(m.Texts) != 1 {
		t.Fatalf("after the delete: %s %+v", created(), m)
	}
	w.remove("b/x-1.0.zip")
	if idx := w.index(); len(idx) != 0 {
		t.Fatalf("after the last delete: %v", idx)
	}
}

// Content wins: a deploy's own pypi.* values that disagree with the
// content are replaced, and a summary or Requires-Python the content lacks
// is removed. Properties the format does not keep stay.
func TestContentWins(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	w.deploy("x-1.0.tar.gz", sdist("x", "1.0", meta("x", "1.0")), types.Properties{
		pypi.PropName: {"y"}, pypi.PropVersion: {"9"}, pypi.PropSummary: {"stale"},
		pypi.PropRequiresPython: {">=2"}, "team": {"a"},
	})
	want := types.Properties{
		pypi.PropName: {"x"}, pypi.PropVersion: {"1.0"}, pypi.PropNormalizedName: {"x"},
		"team": {"a"},
	}
	if diff := cmp.Diff(want, w.props("x-1.0.tar.gz")); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	idx := w.index()
	if _, ok := idx["x"]["1.0"]; !ok || len(idx) != 1 {
		t.Fatalf("index %v", idx)
	}
	// The core metadata decides: renaming the file's project by its
	// properties moves nothing.
	w.annotate("x-1.0.tar.gz", types.Properties{pypi.PropName: {"z"}, pypi.PropVersion: {"2"}})
	if idx := w.index(); len(idx["x"]) != 1 || idx["z"] != nil {
		t.Fatalf("after renaming: %v", idx)
	}
	// A property value is stored as a property can hold it.
	w.deploy("n-1.0.tar.gz", sdist("n", "1.0", meta("n", "1.0", "Summary: a\x00b")), nil)
	if s := w.props("n-1.0.tar.gz")[pypi.PropSummary]; !slices.Equal(s, []string{"ab"}) {
		t.Errorf("summary %q", s)
	}
}

// A distribution whose content yields no metadata is indexed by its
// pypi.name and pypi.version when it carries both, as JFrog requires of
// manual uploads, and follows them; without them it is stored only.
func TestIndexedByProperties(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	body := []byte("not an archive")
	w.deploy("manual/thing-1.0.tar.gz", body, nil)
	w.deploy("manual/nameless.whl", body, types.Properties{pypi.PropVersion: {"1.0"}})
	w.deploy("manual/invalid.whl", body, types.Properties{
		pypi.PropName: {"no good"}, pypi.PropVersion: {"1.0"},
	})
	if idx := w.index(); len(idx) != 0 {
		t.Fatalf("index %v", idx)
	}
	w.annotate("manual/thing-1.0.tar.gz", types.Properties{
		pypi.PropName: {"My_Thing"}, pypi.PropVersion: {" 1.0.0 "},
		pypi.PropRequiresPython: {">=3.8"},
	})
	got := w.index()["my-thing"]["1.0.0"]
	if !slices.Equal(paths(got), []string{"manual/thing-1.0.tar.gz"}) ||
		got.Files[0].RequiresPython != ">=3.8" || got.Files[0].Metadata != "" || got.Texts != nil {
		t.Fatalf("indexed by its properties: %+v", got)
	}
	if props := w.props("manual/thing-1.0.tar.gz"); props[pypi.PropNormalizedName] != nil {
		t.Errorf("a file indexed by its properties keeps them: %v", props)
	}
	w.annotate("manual/thing-1.0.tar.gz", types.Properties{pypi.PropVersion: {"2.0"}})
	if idx := w.index(); idx["my-thing"]["1.0.0"].Files != nil || len(idx["my-thing"]) != 1 ||
		len(idx["my-thing"]["2.0"].Files) != 1 {
		t.Fatalf("after a new version: %v", idx)
	}
	w.annotate("manual/thing-1.0.tar.gz", types.Properties{pypi.PropRequiresPython: {">=3.10"}})
	if f := w.index()["my-thing"]["2.0"].Files[0]; f.RequiresPython != ">=3.10" {
		t.Fatalf("after a new Requires-Python: %+v", f)
	}
	w.annotate("manual/thing-1.0.tar.gz", nil, pypi.PropName)
	if idx := w.index(); len(idx) != 0 {
		t.Fatalf("after removing the name: %v", idx)
	}
	// A property the index does not follow changes nothing.
	w.annotate("manual/thing-1.0.tar.gz", types.Properties{"team": {"a"}})
	if idx := w.index(); len(idx) != 0 {
		t.Fatalf("after another property: %v", idx)
	}
}

// pypi.yanked yanks a file, with a reason unless it is "true" or empty,
// and "false" in any case does not; the index follows every change.
func TestYanked(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	p := "x/1.0/" + wheelName("x", "1.0")
	w.deploy(p, wheel("x", "1.0", meta("x", "1.0")), types.Properties{pypi.PropYanked: {"true"}})
	state := func() (bool, string) {
		f := w.index()["x"]["1.0"].Files[0]
		return f.Yanked, f.YankedReason
	}
	if y, r := state(); !y || r != "" {
		t.Fatalf("deployed yanked: %v %q", y, r)
	}
	for _, c := range []struct {
		value  string
		yanked bool
		reason string
	}{
		{"broken build", true, "broken build"},
		{"False", false, ""},
		{"", true, ""},
		{"TRUE", true, ""},
		{"false", false, ""},
	} {
		w.annotate(p, types.Properties{pypi.PropYanked: {c.value}})
		if y, r := state(); y != c.yanked || r != c.reason {
			t.Errorf("%q: %v %q", c.value, y, r)
		}
	}
	w.annotate(p, types.Properties{pypi.PropYanked: {"gone"}})
	w.annotate(p, nil, pypi.PropYanked)
	if y, _ := state(); y {
		t.Error("still yanked once the property is removed")
	}
}

// A file overwritten with another version's content leaves its version,
// which goes when that was its last file, and joins the other.
func TestOverwriteMovesVersion(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	w.deploy("a/x.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0")}), nil)
	w.deploy("b/x.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0")}), nil)
	w.deploy("a/x.zip", zipOf(entry{"PKG-INFO", meta("x", "2.0")}), nil)
	idx := w.index()
	if !slices.Equal(paths(idx["x"]["1.0"]), []string{"b/x.zip"}) ||
		!slices.Equal(paths(idx["x"]["2.0"]), []string{"a/x.zip"}) {
		t.Fatalf("index %v", idx)
	}
	w.deploy("b/x.zip", zipOf(entry{"PKG-INFO", meta("y", "1.0")}), nil)
	idx = w.index()
	if idx["x"]["1.0"].Files != nil || len(idx["y"]["1.0"].Files) != 1 {
		t.Fatalf("after its last file left: %v", idx)
	}
	w.deploy("b/x.zip", []byte("no archive"), nil)
	w.deploy("a/x.zip", []byte("no archive"), nil)
	if idx := w.index(); len(idx) != 0 {
		t.Fatalf("after both became unreadable: %v", idx)
	}
}

// A move within the repository keeps the file in its version under its
// new path; a copy adds a record.
func TestMoveAndCopy(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	w.deploy("a/x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0")}), nil)
	w.transfer("a/x-1.0.zip", "b/x-1.0.zip", false)
	w.transfer("a/x-1.0.zip", "c/x-1.0.zip", true)
	if got := paths(
		w.index()["x"]["1.0"],
	); !slices.Equal(
		got,
		[]string{"b/x-1.0.zip", "c/x-1.0.zip"},
	) {
		t.Fatalf("%v", got)
	}
}

// A remote repository's cache is what its index reads; files a remote
// repository holds outside it, and folders, are not.
func TestNotIndexed(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	ctx, h := w.ctx, w.h
	remote := w.repo("pypi-remote", repo.Remote)
	it := types.Item{Name: "x-1.0.zip", Folder: false}
	if err := h.Deployed(ctx, w.items.Env(), remote, it); err != nil {
		t.Fatal(err)
	}
	folder := types.Item{Name: "x.whl", Folder: true}
	if err := h.Annotated(ctx, w.items.Env(), w.c, folder, annotation()); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(ctx, w.items.Env(), w.c, types.Item{Name: "README"}); err != nil {
		t.Fatal(err)
	}
	if idx := w.index(); len(idx) != 0 {
		t.Fatalf("%v", idx)
	}
}
