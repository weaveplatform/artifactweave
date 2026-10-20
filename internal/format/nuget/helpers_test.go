package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// zipFile is an entry of a test zip.
type zipFile struct{ name, body string }

// zipOf is files as a zip. Writes to a buffer cannot fail, so neither can
// this, and the same files always give the same bytes.
func zipOf(files ...zipFile) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, _ := zw.Create(f.name)
		_, _ = io.WriteString(w, f.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// nuspecXML is a .nuspec of id at version in the 2013/05 schema, with more
// elements in its <metadata>.
func nuspecXML(id, version, more string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">` +
		`<metadata><id>` + id + `</id><version>` + version + `</version>` + more +
		`</metadata></package>`
}

// pkg is a package of id at version, with more nuspec elements, as dotnet
// pack lays it out.
func pkg(id, version, more string) []byte {
	return zipOf(
		zipFile{"_rels/.rels", "<Relationships/>"},
		zipFile{id + ".nuspec", nuspecXML(id, version, more)},
		zipFile{"lib/net8.0/" + id + ".dll", "MZ " + more},
		zipFile{"[Content_Types].xml", "<Types/>"},
	)
}

// symbols is the symbols package of id at version.
func symbols(id, version string) []byte {
	return zipOf(
		zipFile{id + ".nuspec", nuspecXML(id, version,
			`<packageTypes><packageType name="SymbolsPackage" /></packageTypes>`)},
		zipFile{"lib/net8.0/" + id + ".pdb", "PDB"},
	)
}

// world is a NuGet local repository with the item and trash services over
// it, a work queue, and a clock that moves a minute on every write.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	items *item.Service
	trash *trash.Service
	q     *work.Queue
	c     repo.Config
	now   time.Time
}

func newWorld(t *testing.T, tune func(*repo.Config)) *world {
	t.Helper()
	return newWorldOn(t, inmem.New(), tune)
}

func newWorldOn(t *testing.T, st store.Store, tune func(*repo.Config)) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: st,
		now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
	}
	clock := func() time.Time { return w.now }
	w.q, err = work.New(st, work.Options{Now: clock, Poll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	w.items = &item.Service{Store: st, Blobs: blobs, Now: clock, Work: w.q}
	if err := w.items.RegisterWork(w.q); err != nil {
		t.Fatal(err)
	}
	w.trash = &trash.Service{Store: st, Items: w.items, Now: clock}
	w.c = w.repo("nuget-local", repo.Local, tune)
	return w
}

// repo creates a repository of class with the NuGet type, as tune leaves
// it.
func (w *world) repo(key string, class repo.Class, tune func(*repo.Config)) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: class, PackageType: repo.NuGet}
	if class == repo.Remote {
		c.URL = "https://api.nuget.org/v3/index.json"
	}
	repo.Defaults(&c)
	if tune != nil {
		tune(&c)
	}
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	stored, err := w.st.Repos().Get(w.ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return stored
}

func (w *world) deploy(p string, body []byte, props types.Properties) types.Item {
	w.t.Helper()
	it, err := w.tryDeploy(w.c, p, body, props)
	if err != nil {
		w.t.Fatalf("deploy %s: %v", p, err)
	}
	return it
}

func (w *world) tryDeploy(
	c repo.Config,
	p string,
	body []byte,
	props types.Properties,
) (types.Item, error) {
	w.now = w.now.Add(time.Minute)
	return w.items.Deploy(w.ctx, item.Deploy{ //nolint:wrapcheck // test helper
		Target: item.Target{Repo: c, Path: p}, Body: bytes.NewReader(body),
		Properties: props, By: "tester",
	})
}

func (w *world) remove(p string) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "tester"); err != nil {
		w.t.Fatalf("delete %s: %v", p, err)
	}
}

func (w *world) restore(p string) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if _, err := w.trash.Restore(w.ctx, w.c.Key, p, trash.Destination{Repo: w.c}, "tester",
		nil); err != nil {
		w.t.Fatalf("restore %s: %v", p, err)
	}
}

// transfer copies or moves from:src to to:dst and returns the messages of
// what failed.
func (w *world) transfer(
	from repo.Config,
	src string,
	to repo.Config,
	dst string,
	move bool,
) []string {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{Repo: from, Path: src}, To: item.Target{Repo: to, Path: dst},
		Move: move, By: "tester",
	})
	if err != nil {
		w.t.Fatalf("transfer %s to %s: %v", src, dst, err)
	}
	if res.Failed == 0 {
		return nil
	}
	out := make([]string, 0, len(res.Messages))
	for _, m := range res.Messages {
		out = append(out, m.Text)
	}
	return out
}

func (w *world) props(p string) types.Properties {
	w.t.Helper()
	it := w.item(p)
	props, err := w.st.Items().Properties(w.ctx, it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return props
}

func (w *world) item(p string) types.Item {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatalf("get %s: %v", p, err)
	}
	return it
}

func (w *world) stored(p string) bool {
	_, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	return err == nil
}

// indexed is one version as the index holds it.
type indexed struct {
	Created  time.Time
	Metadata nuget.Metadata
	Files    []string
}

// index is c's index: each package's versions.
func (w *world) index(c repo.Config) map[string]map[string]indexed {
	w.t.Helper()
	out := map[string]map[string]indexed{}
	pkgs, _, err := w.st.Packages().List(w.ctx, c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		vs, err := w.st.Packages().Versions(w.ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		out[p.Name] = map[string]indexed{}
		for _, v := range vs {
			m, err := nuget.DecodeMetadata(v.Metadata)
			if err != nil {
				w.t.Fatal(err)
			}
			ids, err := w.st.Packages().Files(w.ctx, v.ID)
			if err != nil {
				w.t.Fatal(err)
			}
			var files []string
			for _, id := range ids {
				it, err := w.st.Items().GetByID(w.ctx, id)
				if err != nil {
					w.t.Fatal(err)
				}
				files = append(files, it.FullPath())
			}
			out[p.Name][v.Version] = indexed{
				Created:  v.Created,
				Metadata: m,
				Files:    sortedStrings(files),
			}
		}
	}
	return out
}

// version is one version of the repository's index; it fails the test when
// there is none.
func (w *world) version(name, version string) indexed {
	w.t.Helper()
	v, ok := w.index(w.c)[name][version]
	if !ok {
		w.t.Fatalf("no %s %s in %v", name, version, w.index(w.c))
	}
	return v
}

// versions lists the versions of package name.
func (w *world) versions(name string) []string {
	w.t.Helper()
	var out []string
	for v := range w.index(w.c)[name] {
		out = append(out, v)
	}
	return sortedStrings(out)
}

func (w *world) reindex() {
	w.t.Helper()
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		w.t.Fatal(err)
	}
}

func sortedStrings(s []string) []string { return slices.Sorted(slices.Values(s)) }
