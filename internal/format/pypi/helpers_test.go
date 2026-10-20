package pypi_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// entry is an archive entry: a folder when its name ends in "/".
type entry struct{ name, body string }

// zipOf is entries as a zip. Writes to a buffer cannot fail, so neither
// can this.
func zipOf(entries ...entry) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, _ := zw.Create(e.name)
		_, _ = io.WriteString(w, e.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// tarOf is entries as a tar, gzipped when gz is set.
func tarOf(gz bool, entries ...entry) []byte {
	var buf bytes.Buffer
	var w io.Writer = &buf
	var g *gzip.Writer
	if gz {
		g = gzip.NewWriter(&buf)
		w = g
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e.name, "/") {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		_ = tw.WriteHeader(h)
		_, _ = io.WriteString(tw, e.body)
	}
	_ = tw.Close()
	if g != nil {
		_ = g.Close()
	}
	return buf.Bytes()
}

// meta is a core metadata file naming name and version, with more header
// lines after them and a description body.
func meta(name, version string, more ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Metadata-Version: 2.1\nName: %s\nVersion: %s\n", name, version)
	for _, l := range more {
		b.WriteString(l + "\n")
	}
	b.WriteString("\nName: not-a-field\nThe description.\n")
	return b.String()
}

// distName is name as a distribution's file name spells it.
func distName(name string) string { return strings.ReplaceAll(name, "-", "_") }

// wheelName is the file name of a pure-Python wheel of name and version.
func wheelName(name, version string) string {
	return distName(name) + "-" + version + "-py3-none-any.whl"
}

// wheel is a wheel of name and version with METADATA m.
func wheel(name, version, m string) []byte {
	info := distName(name) + "-" + version + ".dist-info/"
	return zipOf(
		entry{distName(name) + "/__init__.py", ""},
		entry{info + "METADATA", m},
		entry{info + "RECORD", ""},
	)
}

// sdist is an sdist of name and version, a tar.gz with PKG-INFO m in its
// top-level folder.
func sdist(name, version, m string) []byte {
	top := distName(name) + "-" + version + "/"
	return tarOf(true, entry{top, ""}, entry{top + "PKG-INFO", m}, entry{top + "setup.py", ""})
}

// world is a PyPI local repository with the item service over it, the
// format with settings, and a clock that moves a minute on every write.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	items *item.Service
	h     pypi.Handler
	c     repo.Config
	now   time.Time
}

func newWorld(t *testing.T, s pypi.Settings) *world {
	t.Helper()
	return newWorldOn(t, inmem.New(), s)
}

func newWorldOn(t *testing.T, st store.Store, s pypi.Settings) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: st, h: pypi.Handler{Settings: s},
		now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
	}
	w.items = &item.Service{
		Store: st, Blobs: blobs, Now: func() time.Time { return w.now },
		Formats: func(repo.PackageType) format.Handler { return w.h },
	}
	w.c = w.repo("pypi-local", repo.Local)
	return w
}

// repo creates a PyPI repository of class.
func (w *world) repo(key string, class repo.Class) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: class, PackageType: repo.PyPI}
	if class == repo.Remote {
		c.URL = "https://pypi.example"
	}
	repo.Defaults(&c)
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *world) deploy(p string, body []byte, props types.Properties) types.Item {
	w.t.Helper()
	it, err := w.tryDeploy(w.c, p, body, props)
	if err != nil {
		w.t.Fatal(err)
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

func (w *world) item(p string) types.Item {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) annotate(p string, set types.Properties, remove ...string) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if err := w.items.Annotate(w.ctx, w.c, w.item(p), set, remove); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) props(p string) types.Properties {
	w.t.Helper()
	props, err := w.st.Items().Properties(w.ctx, w.item(p).ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return props
}

func (w *world) remove(p string) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "tester"); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) transfer(from, to string, move bool) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{Repo: w.c, Path: from}, To: item.Target{Repo: w.c, Path: to},
		Move: move, By: "tester",
	})
	if err != nil || res.Failed > 0 {
		w.t.Fatalf("transfer %s to %s: %v %v", from, to, err, res.Messages)
	}
}

// index is the repository's index: each project's versions, each with its
// Metadata.
func (w *world) index() map[string]map[string]pypi.Metadata {
	w.t.Helper()
	out := map[string]map[string]pypi.Metadata{}
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		vs, err := w.st.Packages().Versions(w.ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		out[p.Name] = map[string]pypi.Metadata{}
		for _, v := range vs {
			m, err := pypi.DecodeMetadata(v.Metadata)
			if err != nil {
				w.t.Fatal(err)
			}
			out[p.Name][v.Version] = m
		}
	}
	return out
}

// paths lists the paths of m's files, in its order.
func paths(m pypi.Metadata) []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, f.Path)
	}
	return out
}

// annotation is a change to pypi.yanked.
func annotation() format.Annotation {
	return format.Annotation{Set: types.Properties{pypi.PropYanked: {"true"}}}
}
