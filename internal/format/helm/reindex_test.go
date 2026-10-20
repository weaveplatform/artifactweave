package helm_test

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// wipe removes the repository's whole index.
func (w *world) wipe() {
	w.t.Helper()
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) reindexPath(p string) {
	w.t.Helper()
	if err := w.items.ReindexPath(w.ctx, w.c, p); err != nil {
		w.t.Fatal(err)
	}
}

// A partial reindex rebuilds what the archives under a folder, read to its
// last level, or one archive give, leaves the rest of the index alone, and
// queues index.yaml. A version with archives elsewhere keeps them.
func TestReindexPath(t *testing.T) {
	w := newWorld(t)
	w.deploy("team/a/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: team"))
	w.deploy("team/api-1.0.0.tgz", chartTgz("api", "1.0.0"))
	w.deploy("teamwork/db-1.0.0.tgz", chartTgz("db", "1.0.0"))
	w.deploy("other/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: other"))
	w.deploy("other/cache-2.0.0.tgz", chartTgz("cache", "2.0.0"))
	whole := w.index(w.c)
	w.wipe()
	w.queued(helm.KindIndex)

	w.reindexPath("team")
	got := w.index(w.c)
	if len(got) != 2 || got["api"] == nil || got["web"] == nil {
		t.Fatalf("team/ alone: %v", got)
	}
	w.described(w.c, "web", "1.0.0", "team/a/web-1.0.0.tgz", "team", "team/a/web-1.0.0.tgz")
	if q := w.queued(helm.KindIndex); !slices.Equal(q, []string{w.c.Key}) {
		t.Fatal(q)
	}
	// One archive; the version keeps the one it links in team/.
	w.reindexPath("other/web-1.0.0.tgz")
	w.described(w.c, "web", "1.0.0", "other/web-1.0.0.tgz", "other",
		"other/web-1.0.0.tgz", "team/a/web-1.0.0.tgz")
	// A path is no prefix of a name: team/ took nothing of teamwork/, and a
	// wildcard is no wildcard.
	w.reindexPath("te*")
	if _, ok := w.index(w.c)["db"]; ok {
		t.Fatal(w.index(w.c))
	}
	w.reindexPath("/teamwork/")
	w.reindexPath("other")
	if diff := w.index(w.c); len(diff) != len(whole) {
		t.Fatalf("%v, want %v", diff, whole)
	}
	// Nothing at the path changes nothing.
	w.reindexPath("missing")
	if got := w.index(w.c); len(got) != len(whole) {
		t.Fatal(got)
	}
	// The root is the whole repository.
	w.wipe()
	w.reindexPath("")
	if got := w.index(w.c); len(got) != len(whole) {
		t.Fatal(got)
	}
}

// A partial reindex drops what the index says of the archives under the
// path that their content does not back: a link to a file that holds no
// chart, and the version it was the last archive of.
func TestReindexPathDropsStaleLinks(t *testing.T) {
	w := newWorld(t)
	w.deploy("x/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	w.deploy("y/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: y"))
	junk := w.deploy("y/junk.tgz", []byte("no chart"))
	lone := w.deploy("z/lone.tgz", []byte("no chart either"))
	link := func(name, version string, it types.Item) {
		t.Helper()
		p, err := w.st.Packages().
			Ensure(w.ctx, types.Package{RepoKey: w.c.Key, Name: name, Type: w.c.PackageType})
		if err != nil {
			t.Fatal(err)
		}
		pv, err := w.st.Packages().Version(w.ctx, p.ID, version)
		if err != nil {
			pv, err = w.st.Packages().PutVersion(w.ctx, types.PackageVersion{
				PackageID: p.ID,
				Version:   version,
				Metadata: []byte(
					`{"chart":{},"path":"elsewhere","digest":"","created":"0001-01-01T00:00:00Z"}`,
				),
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := w.st.Packages().
			LinkFile(w.ctx, types.VersionFile{VersionID: pv.ID, ItemID: it.ID}); err != nil {
			t.Fatal(err)
		}
	}
	link("web", "1.0.0", junk)
	link("ghost", "0.1.0", lone)
	w.reindexPath("y")
	w.described(w.c, "web", "1.0.0", "y/web-1.0.0.tgz", "y", "x/web-1.0.0.tgz", "y/web-1.0.0.tgz")
	w.reindexPath("z")
	if _, ok := w.index(w.c)["ghost"]; ok {
		t.Fatal(w.index(w.c))
	}
}

// A reindex sets the properties every indexed chart carries, and drops an
// appVersion its chart does not have.
func TestReindexSetsProperties(t *testing.T) {
	w := newWorld(t)
	it := w.deploy("web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	if err := w.items.Annotate(w.ctx, w.c, it, types.Properties{
		helm.PropAppVersion: {"stale"}, helm.PropName: {"wrong"},
	}, []string{helm.PropVersion}); err != nil {
		t.Fatal(err)
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	got := w.props("web-1.0.0.tgz")
	if !got.Has(helm.PropName, "web") || !got.Has(helm.PropVersion, "1.0.0") {
		t.Fatal(got)
	}
	if _, ok := got[helm.PropAppVersion]; ok {
		t.Fatal(got)
	}
}

// A hook that finds a version's newest link holding no chart of it, which
// only an index out of step with the tree has, leaves it out, and drops the
// version when nothing else is left.
func TestStaleNewestLink(t *testing.T) {
	w := newWorld(t)
	w.deploy("x/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	stale := w.deploy("y/stale.tgz", []byte("no chart"))
	p, err := w.st.Packages().Find(w.ctx, w.c.Key, "web")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := w.st.Packages().Version(w.ctx, p.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.Packages().
		LinkFile(w.ctx, types.VersionFile{VersionID: pv.ID, ItemID: stale.ID}); err != nil {
		t.Fatal(err)
	}
	w.deploy("x/web-1.0.0.tgz", chartTgz("api", "1.0.0"))
	if got := w.index(w.c); len(got) != 1 || got["api"] == nil {
		t.Fatal(got)
	}
}
