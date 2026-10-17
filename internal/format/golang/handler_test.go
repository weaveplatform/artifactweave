package golang_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const dir = "example.com/m/@v/v1.0.0"

// world is an in-memory store with Go repositories, and the item service
// over them with the registered formats and a clock that moves a minute
// on every write.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	items *item.Service
	now   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: inmem.New(),
		now: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC),
	}
	w.items = &item.Service{
		Store: w.st, Blobs: blobs, Formats: formats.For,
		Now: func() time.Time { w.now = w.now.Add(time.Minute); return w.now },
	}
	return w
}

func (w *world) repo(key string) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Go}
	repo.Defaults(&c)
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *world) deploy(c repo.Config, p, body string) error {
	_, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: p}, Body: strings.NewReader(body), By: "test",
	})
	return err //nolint:wrapcheck // the item service's own
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

// version returns the indexed version v of module m in c and its files'
// paths; ok is false when it is not indexed.
func (w *world) version(
	c repo.Config,
	m, v string,
) (pv types.PackageVersion, files []string, ok bool) {
	w.t.Helper()
	p, err := w.st.Packages().Find(w.ctx, c.Key, m)
	if errors.Is(err, store.ErrNotFound) {
		return pv, nil, false
	}
	w.must(err)
	pv, err = w.st.Packages().Version(w.ctx, p.ID, v)
	if errors.Is(err, store.ErrNotFound) {
		return pv, nil, false
	}
	w.must(err)
	ids, err := w.st.Packages().Files(w.ctx, pv.ID)
	w.must(err)
	for _, id := range ids {
		it, err := w.st.Items().GetByID(w.ctx, id)
		w.must(err)
		files = append(files, it.FullPath())
	}
	return pv, files, true
}

// Go repositories have the Go format, which the item service calls on
// every write.
func TestRegistered(t *testing.T) {
	if _, ok := formats.For(repo.Go).(golang.Handler); !ok {
		t.Fatalf("%T", formats.For(repo.Go))
	}
	if golang.Serves(repo.Generic) {
		t.Fatal("serves generic")
	}
}

// A version's .zip and .mod cannot change once stored, on a deploy or as
// the target of a copy or a move, even for a caller who may overwrite; the
// same content and any .info pass, and files outside version paths keep the
// generic overwrite rules.
func TestRepublish(t *testing.T) {
	w := newWorld(t)
	a, b := w.repo("go-a"), w.repo("go-b")
	for _, f := range []string{".zip", ".mod", ".info"} {
		w.must(w.deploy(a, dir+f, "first "+f))
	}
	w.must(w.deploy(a, dir+".zip", "first .zip"))
	w.must(w.deploy(a, dir+".info", "second .info"))
	w.must(w.deploy(a, "example.com/m/@v/list", "one"))
	w.must(w.deploy(a, "example.com/m/@v/list", "two"))
	for _, f := range []string{".zip", ".mod"} {
		err := w.deploy(a, dir+f, "second "+f)
		var r *format.Rejection
		if !errors.As(err, &r) || r.Status != http.StatusConflict {
			t.Fatalf("%s: %v", f, err)
		}
		want := "Version v1.0.0 of module example.com/m already has a different " + f +
			"; delete the version to replace it."
		if r.Message != want {
			t.Errorf("%s: %q", f, r.Message)
		}
	}
	_, files, _ := w.version(a, "example.com/m", "v1.0.0")
	if len(files) != 3 {
		t.Fatalf("the refusals changed the version: %v", files)
	}

	w.must(w.deploy(b, dir+".zip", "other .zip"))
	w.must(w.deploy(b, dir+".mod", "first .mod"))
	for _, move := range []bool{false, true} {
		res, err := w.items.Transfer(w.ctx, item.Transfer{
			From: item.Target{Repo: b}, To: item.Target{Repo: a}, Move: move, CanOverwrite: true,
			By: "test",
		})
		w.must(err)
		if res.Failed != 1 ||
			!strings.Contains(res.Messages[0].Text, "already has a different .zip") {
			t.Errorf("move %v: %+v", move, res)
		}
	}
	got, err := w.st.Items().Get(w.ctx, a.Key, false, dir+".zip")
	w.must(err)
	blob, _, err := w.items.Blobs.Open(w.ctx, got.Actual.SHA256)
	w.must(err)
	defer blob.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(blob)
	if body.String() != "first .zip" {
		t.Fatalf("the stored .zip is %q", body.String())
	}
}

// Copy and move targets are checked when the store cannot say what is
// there; checksum uploads change no content and are not.
func TestAdmit(t *testing.T) {
	ctx := context.Background()
	c := repo.Config{Key: "go-local", PackageType: repo.Go}
	fail := faulty{Store: inmem.New(), fail: map[string]bool{"Get": true}}
	h := golang.Handler{}
	if _, err := h.Admit(ctx, format.Env{Store: fail}, c, format.Write{
		Op: format.OpCopy, Path: dir + ".zip",
	}); !errors.Is(err, errStore) {
		t.Fatalf("copy: %v", err)
	}
	if _, err := h.Admit(ctx, format.Env{}, c, format.Write{
		Op: format.OpChecksum, Path: dir + ".zip",
	}); err != nil {
		t.Fatalf("checksum: %v", err)
	}
	blackedOut := true
	c.BlackedOut = &blackedOut
	_, err := h.Admit(ctx, format.Env{}, c, format.Write{Op: format.OpDeploy, Path: dir + ".zip"})
	var r *format.Rejection
	if !errors.As(err, &r) || r.Status != http.StatusNotFound {
		t.Fatalf("blacked out: %v", err)
	}
}

// A version is dated by the first of its files stored, which a delete can
// change, and links only its version files: a folder at a version file's
// path and files beside them are no part of it.
func TestIndex(t *testing.T) {
	w := newWorld(t)
	c := w.repo("go-local")
	w.must(w.deploy(c, "example.com/m/@v/list", "v1.0.0\n"))
	w.must(w.deploy(c, "example.com/m/@v/v1.0.zip", "not canonical"))
	w.must(w.deploy(c, "README.md", "hello"))
	if _, _, ok := w.version(c, "example.com/m", "v1.0"); ok {
		t.Fatal("indexed a file outside version paths")
	}
	_, err := w.items.MakeFolder(w.ctx, item.Target{Repo: c, Path: dir + ".info"}, nil, "test")
	w.must(err)
	w.must(w.deploy(c, dir+".zip", "zip"))
	zipped := w.now
	w.must(w.deploy(c, dir+".mod", "module example.com/m\n"))
	modded := w.now
	pv, files, ok := w.version(c, "example.com/m", "v1.0.0")
	if !ok || len(files) != 2 || !pv.Created.Equal(zipped) {
		t.Fatalf("%+v %v %v; want two files from %v", pv, files, ok, zipped)
	}
	_, err = w.items.Delete(w.ctx, item.Target{Repo: c, Path: dir + ".zip"}, "test")
	w.must(err)
	pv, files, _ = w.version(c, "example.com/m", "v1.0.0")
	if len(files) != 1 || !pv.Created.Equal(modded) {
		t.Fatalf("after the .zip went: %+v %v; want the .mod from %v", pv, files, modded)
	}
	w.must(golang.Handler{}.Reindex(w.ctx, format.Env{Store: w.st}, c))
	again, files, _ := w.version(c, "example.com/m", "v1.0.0")
	if len(files) != 1 || !again.Created.Equal(modded) {
		t.Fatalf("reindexed: %+v %v", again, files)
	}
}

// A version's .info is metadata, replaced under the same path; its .zip,
// .mod and every other file are unique content.
func TestClassify(t *testing.T) {
	c := repo.Config{Key: "go-local", PackageType: repo.Go}
	for p, want := range map[string]format.Class{
		dir + ".info":           format.ClassMetadata,
		dir + ".zip":            format.ClassUnique,
		dir + ".mod":            format.ClassUnique,
		"example.com/m/@v/list": format.ClassUnique,
		"notes.info":            format.ClassUnique,
	} {
		if got := format.ClassOf(golang.Handler{}, c, p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}

// Store failures roll the write back with the cause.
func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	c := repo.Config{Key: "go-local", Class: repo.Local, PackageType: repo.Go}
	file := types.Item{RepoKey: c.Key, Name: dir + ".zip"}
	for _, f := range []struct {
		fail string
		run  func(h golang.Handler, env format.Env) error
	}{
		{"Get", func(h golang.Handler, env format.Env) error {
			return h.Deployed(ctx, env, c, file)
		}},
		{"Get", func(h golang.Handler, env format.Env) error {
			return h.Deleted(ctx, env, c, []types.Item{file, {Path: "example.com", Name: "m", Folder: true}})
		}},
		{"Ensure", func(h golang.Handler, env format.Env) error {
			return h.Deployed(ctx, env, c, file)
		}},
		{"Search", func(h golang.Handler, env format.Env) error {
			return h.Reindex(ctx, env, c)
		}},
	} {
		st := inmem.New()
		if err := st.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Items().Put(ctx, file); err != nil {
			t.Fatal(err)
		}
		env := format.Env{Store: faulty{Store: st, fail: map[string]bool{f.fail: true}}}
		if err := f.run(golang.Handler{}, env); !errors.Is(err, errStore) {
			t.Errorf("%s: %v", f.fail, err)
		}
	}
}

var errStore = errors.New("store boom")

// faulty fails the store methods fail names and delegates the rest.
type faulty struct {
	store.Store
	fail map[string]bool
}

func (f faulty) Items() store.ItemStore { return faultyItems{f.Store.Items(), f.fail} }

func (f faulty) Packages() store.PackageStore {
	return faultyPackages{f.Store.Packages(), f.fail}
}

type faultyItems struct {
	store.ItemStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (f faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if f.fail["Get"] {
		return types.Item{}, errStore
	}
	return f.ItemStore.Get(ctx, key, cached, p)
}

//nolint:wrapcheck // test double
func (f faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if f.fail["Search"] {
		return nil, errStore
	}
	return f.ItemStore.Search(ctx, q, o)
}

type faultyPackages struct {
	store.PackageStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (f faultyPackages) Ensure(ctx context.Context, p types.Package) (types.Package, error) {
	if f.fail["Ensure"] {
		return types.Package{}, errStore
	}
	return f.PackageStore.Ensure(ctx, p)
}
