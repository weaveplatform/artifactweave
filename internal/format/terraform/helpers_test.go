package terraform_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// world is an in-memory store with the item and trash services over it,
// the registered formats, blobs that count their opens, and a clock that
// moves a minute on every write.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	blobs *counting
	items *item.Service
	trash *trash.Service
	now   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return newWorldOn(t, inmem.New())
}

func newWorldOn(t *testing.T, st store.Store) *world {
	t.Helper()
	b, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: st, blobs: &counting{Store: b},
		now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
	}
	now := func() time.Time { w.now = w.now.Add(time.Minute); return w.now }
	w.items = &item.Service{Store: st, Blobs: w.blobs, Formats: formats.For, Now: now}
	w.trash = &trash.Service{Store: st, Items: w.items, Now: now}
	return w
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

// repo creates a local terraform repository of type tt, none when empty.
func (w *world) repo(key string, tt repo.TerraformType) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Terraform, TerraformType: tt}
	repo.Defaults(&c)
	w.must(w.st.Repos().Create(w.ctx, c))
	return c
}

func (w *world) deploy(c repo.Config, p string, body []byte) error {
	_, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: p}, Body: bytes.NewReader(body), By: "test",
	})
	return err //nolint:wrapcheck // the item service's own
}

func (w *world) remove(c repo.Config, p string) {
	w.t.Helper()
	_, err := w.items.Delete(w.ctx, item.Target{Repo: c, Path: p}, "test")
	w.must(err)
}

// indexed is a version as the index holds it: its files' paths, sorted,
// and its metadata.
type indexed struct {
	files []string
	meta  []byte
}

// version returns the indexed version v of package name in c; ok is
// false when it is not indexed.
func (w *world) version(c repo.Config, name, v string) (indexed, bool) {
	w.t.Helper()
	p, err := w.st.Packages().Find(w.ctx, c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return indexed{}, false
	}
	w.must(err)
	pv, err := w.st.Packages().Version(w.ctx, p.ID, v)
	if errors.Is(err, store.ErrNotFound) {
		return indexed{}, false
	}
	w.must(err)
	ids, err := w.st.Packages().Files(w.ctx, pv.ID)
	w.must(err)
	out := indexed{meta: pv.Metadata}
	for _, id := range ids {
		it, err := w.st.Items().GetByID(w.ctx, id)
		w.must(err)
		out.files = append(out.files, it.FullPath())
	}
	slices.Sort(out.files)
	return out, true
}

// packages lists the names of c's indexed packages.
func (w *world) packages(c repo.Config) []string {
	w.t.Helper()
	pkgs, _, err := w.st.Packages().List(w.ctx, c.Key, store.Page{Limit: store.MaxLimit})
	w.must(err)
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.Name)
	}
	slices.Sort(out)
	return out
}

// meta reads a version's metadata.
func (w *world) meta(v indexed) terraform.Meta {
	w.t.Helper()
	m, err := terraform.ReadMeta(v.meta)
	w.must(err)
	return m
}

// counting is a blob store that counts the opens of each blob.
type counting struct {
	blob.Store
	mu    sync.Mutex
	opens map[string]int
}

func (c *counting) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	c.mu.Lock()
	if c.opens == nil {
		c.opens = map[string]int{}
	}
	c.opens[sha]++
	c.mu.Unlock()
	return c.Store.Open(ctx, sha) //nolint:wrapcheck // test double
}

func (c *counting) count(sha string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens[sha]
}

func (c *counting) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens = nil
}

// release is a Terraform provider package's release under dir, of version
// ver of type typ: its archives for platforms, a SHA256SUMS listing them,
// and a signature, keyed by file name.
type release struct {
	dir, typ, ver string
	files         map[string][]byte
}

func newRelease(t testing.TB, dir, typ, ver string, platforms ...string) release {
	t.Helper()
	r := release{dir: dir, typ: typ, ver: ver, files: map[string][]byte{}}
	var sums bytes.Buffer
	for _, p := range platforms {
		body := zipOf(t, [2]string{"terraform-provider-" + typ + "_v" + ver, "binary for " + p})
		r.files[r.name(p+".zip")] = body
		sums.WriteString(sha256Hex(body) + "  " + r.name(p+".zip") + "\n")
	}
	r.files[r.name("SHA256SUMS")] = sums.Bytes()
	r.files[r.name("SHA256SUMS.sig")] = []byte("signature")
	return r
}

func (r release) name(suffix string) string {
	return "terraform-provider-" + r.typ + "_" + r.ver + "_" + suffix
}

func (r release) path(suffix string) string { return r.dir + "/" + r.name(suffix) }
