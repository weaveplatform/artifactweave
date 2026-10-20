// Package formattest is the contract suite for package formats (decision
// 0032). Every format runs Run with a fixture that builds packages of its
// type, and one set of scenarios proves, on the in-memory, SQLite and
// PostgreSQL stores, that its package index (decision 0018) stays right on
// every write path the item service offers: deploy, overwrite, delete, copy,
// move, restore from the trash can, property changes and a remote
// repository's cache.
//
// "Right" has two halves. The fixture says which versions a scenario leaves
// and which files each links. And the item tree is the source of truth, so
// the index the hooks kept must be the one the format's reindex reads from
// the tree: a reindex over it changes nothing, and a reindex from an empty
// index rebuilds it, every version's metadata included. A format that
// indexes nothing must leave the index empty.
//
// Two options of the fixture cover what P8's formats add (decision 0032,
// "Verification"):
//   - Format.RepoWork lists the kinds of derived work a format keys by
//     repository, such as Helm's index.yaml (decision 0039). Every write
//     that changes a repository's versions must leave exactly one item of
//     each, keyed by the repository's key, however many packages it
//     touched: after a deploy, an overwrite, a copy (its target), a move
//     (both repositories), a delete and a restore. Format.Work keeps the
//     kinds keyed by package, as npm's, Go's and Maven's are.
//   - File.SidecarOf marks a file as a sidecar its format keeps with its
//     owner (format.SidecarKeeper), such as a Helm chart's .prov. A sidecar
//     is never a version file. It follows its owner through a delete, a
//     copy, a move and a trash restore. On its own it is an ordinary file
//     that may arrive before its owner. A format that records its sidecars
//     in a version's metadata has that compared after a reindex from empty
//     too.
//
// The suite drives the item service, whose entry points every API calls:
// the properties API and AQL properties.update call Service.Annotate, which
// internal/server's TestPropertyWritesReachTheFormat holds them to.
package formattest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// errBroken is a format that breaks the contract.
var errBroken = errors.New("formattest: contract broken")

// File is one file of a version as a client deploys it.
type File struct {
	// Path is repository-relative.
	Path string
	Body []byte
	// SidecarOf, when set, makes the file a sidecar that the format keeps
	// with another file of the same version: the path of that owner. A
	// scenario that deletes, copies, moves or restores a version's files
	// one at a time names the owner alone and checks that its sidecars
	// went with it.
	SidecarOf string
}

// Version is one version of a package as a client deploys it.
type Version struct {
	// Package and Version are what the index calls it; both are empty for
	// a format that keeps no index.
	Package, Version string
	// Files are deployed in order.
	Files []File
	// Linked are the paths of the files the index links to the version;
	// nil means every file but the sidecars, which are never linked.
	Linked []string
}

// Format is a format under the contract.
type Format struct {
	// Type is the package type of the repositories the suite creates.
	Type repo.PackageType
	// Layout, when set, is the repositories' repoLayoutRef.
	Layout string
	// Local, when set, adjusts each local repository the suite creates
	// before it is stored, for a format that serves only some of its
	// type's repositories, as Terraform's terraformType chooses modules or
	// providers (decision 0040).
	Local func(*repo.Config)
	// Handler is the format; nil means the one formats.For picks for Type.
	Handler format.Handler
	// Version returns version v of package p as deployed for the nth time.
	// The suite names packages "alpha" and "beta" and versions "1.0" and
	// "2.0"; the fixture maps them to its ecosystem's names. Every n gives
	// the same paths, and n > 0 changes the content of at least one file,
	// so a redeploy overwrites.
	Version func(p, v string, n int) Version
	// Properties are the property sets the suite applies in turn to a
	// version's files: a format whose index follows properties names the
	// ones it reads. Nil means a set the format does not read.
	Properties []types.Properties
	// Work lists the kinds of work the format queues on a deploy, keyed by
	// package. Each committed deploy leaves exactly one item of each per
	// package; a kind listed in neither Work nor RepoWork must never be
	// queued.
	Work []string
	// RepoWork lists the kinds of work the format queues keyed by the
	// repository's key alone, as Helm's index.yaml is (decision 0039): the
	// key a reindex of the repository is queued under too (decision 0033).
	// Every committed write that changes a repository's versions leaves
	// exactly one item of each for that repository, however many packages
	// it touched, and none for a repository it left alone.
	RepoWork []string
	// NoRemoteCache marks a format that does not index a remote
	// repository's cache yet, as decision 0018 has the npm, Go and Maven
	// formats do. Docker sets it until its remote repositories come in P9.
	NoRemoteCache bool
}

// Backend opens a fresh, empty store for one scenario.
type Backend struct {
	Name string
	Open func(t *testing.T) store.Store
}

// Backends are the stores the suite runs on: the in-memory reference,
// SQLite on disk and PostgreSQL, which skips where Docker cannot run Linux
// containers.
func Backends() []Backend {
	return []Backend{
		{"inmem", func(*testing.T) store.Store { return inmem.New() }},
		{"sqlite", func(t *testing.T) store.Store {
			st, err := sqlite.Open(context.Background(),
				filepath.Join(t.TempDir(), "aw.db"), sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}},
		{"postgres", func(t *testing.T) store.Store {
			dsn := sqltest.PostgresDSN(t)
			st, err := postgres.Open(context.Background(), dsn, postgres.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			// The test binary shares one server, so each scenario starts
			// empty.
			sqltest.Reset(t, st.DB(), st.Dialect())
			return st
		}},
	}
}

// Run runs every scenario of the contract for f on every backend, each on
// a fresh store.
func Run(t *testing.T, f Format) {
	t.Helper()
	if err := check(f); err != nil {
		t.Fatal(err)
	}
	for _, b := range Backends() {
		t.Run(b.Name, func(t *testing.T) {
			for _, s := range scenarios {
				t.Run(s.name, func(t *testing.T) {
					if err := s.run(newWorld(t, b.Open(t), f)); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

// errFixture is a fixture that contradicts itself, which no format can
// pass.
var errFixture = errors.New("formattest: the fixture is inconsistent")

// check holds the fixture to its own rules: a kind is keyed either by
// package or by repository, a sidecar's owner is another file of its
// version that is no sidecar itself, and no sidecar is linked.
func check(f Format) error {
	for _, k := range f.RepoWork {
		if slices.Contains(f.Work, k) {
			return fmt.Errorf("%w: %s is in both Work and RepoWork", errFixture, k)
		}
	}
	for _, p := range []string{"alpha", "beta"} {
		for _, ver := range []string{"1.0", "2.0"} {
			for n := range 2 {
				if err := checkVersion(f.Version(p, ver, n)); err != nil {
					return fmt.Errorf("%s %s #%d: %w", p, ver, n, err)
				}
			}
		}
	}
	return nil
}

func checkVersion(v Version) error {
	owners := map[string]bool{}
	for _, f := range v.Files {
		if f.SidecarOf == "" {
			owners[f.Path] = true
		}
	}
	for _, f := range v.Files {
		switch {
		case f.SidecarOf == "":
		case !owners[f.SidecarOf]:
			return fmt.Errorf(
				"%w: the sidecar %s follows %s, which is no other file of its version",
				errFixture,
				f.Path,
				f.SidecarOf,
			)
		case slices.Contains(v.Linked, f.Path):
			return fmt.Errorf("%w: the sidecar %s is linked, but a sidecar is never a version file",
				errFixture, f.Path)
		}
	}
	return nil
}

// principal lists v's files but its sidecars. A scenario that names a
// version's files one at a time names these, since each sidecar goes with
// its owner.
func principal(v Version) []File {
	out := make([]File, 0, len(v.Files))
	for _, f := range v.Files {
		if f.SidecarOf == "" {
			out = append(out, f)
		}
	}
	return out
}

// sidecarFiles lists v's sidecars.
func sidecarFiles(v Version) []File {
	var out []File
	for _, f := range v.Files {
		if f.SidecarOf != "" {
			out = append(out, f)
		}
	}
	return out
}

// with lists the path p and the paths of p's sidecars in v: what an
// operation on the file at p changes.
func with(v Version, p string) []string {
	out := []string{p}
	for _, f := range v.Files {
		if f.SidecarOf == p {
			out = append(out, f.Path)
		}
	}
	return out
}

// pathsOf lists the paths of files.
func pathsOf(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// everything lists the paths of every file of vs.
func everything(vs ...Version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, pathsOf(v.Files)...)
	}
	return out
}

// defaultProperties are property sets no format reads.
var defaultProperties = []types.Properties{
	{"formattest.k": {"1"}},
	{"formattest.k": {"2"}, "formattest.l": {"x"}},
}

// Repository keys of the world.
const (
	keyA = "formattest-a"
	keyB = "formattest-b"
	keyC = "formattest-c"
	// keyRemote is the remote repository the cache scenario creates.
	keyRemote = "formattest-remote"
)

// world is a store with three empty local repositories of the format's
// type, and the item and trash services over them with a clock that moves
// a minute on every write.
type world struct {
	t       *testing.T
	ctx     context.Context
	f       Format
	h       format.Handler
	st      store.Store
	items   *item.Service
	trash   *trash.Service
	now     time.Time
	a, b, c repo.Config
}

var start = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

func newWorld(t *testing.T, st store.Store, f Format) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if f.Properties == nil {
		f.Properties = defaultProperties
	}
	w := &world{t: t, ctx: context.Background(), f: f, h: f.Handler, st: st, now: start}
	if w.h == nil {
		w.h = formats.For(f.Type)
	}
	now := func() time.Time { return w.now }
	w.items = &item.Service{
		Store: st, Blobs: blobs, Now: now,
		Formats: func(repo.PackageType) format.Handler { return w.h },
	}
	w.trash = &trash.Service{Store: st, Items: w.items, Now: now}
	w.a, w.b, w.c = w.repo(keyA, nil), w.repo(keyB, nil), w.repo(keyC, nil)
	return w
}

// repo creates a local repository of the format's type, as tune leaves it.
func (w *world) repo(key string, tune func(*repo.Config)) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: w.f.Type, RepoLayoutRef: w.f.Layout}
	if w.f.Local != nil {
		w.f.Local(&c)
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

// remote creates a remote repository of the format's type. Only its cache
// holds items, which the item service stores as a download through it does.
func (w *world) remote(key string) repo.Config {
	w.t.Helper()
	c := repo.Config{
		Key: key, Class: repo.Remote, URL: "https://upstream.example",
		PackageType: w.f.Type, RepoLayoutRef: w.f.Layout,
	}
	repo.Defaults(&c)
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	stored, err := w.st.Repos().Get(w.ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return stored
}

func (w *world) tick() { w.now = w.now.Add(time.Minute) }

// version is the fixture's version v of package p, deployed for the nth
// time.
func (w *world) version(p, v string, n int) Version { return w.f.Version(p, v, n) }

// deploy deploys v's files to c in order, setting props on each.
func (w *world) deploy(c repo.Config, v Version, props types.Properties) error {
	for _, f := range v.Files {
		w.tick()
		if _, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: f.Path}, Body: bytes.NewReader(f.Body),
			Properties: props, By: "formattest",
		}); err != nil {
			return fmt.Errorf("deploy %s to %s: %w", f.Path, c.Key, err)
		}
	}
	return nil
}

// cache stores v's files in remote repository c's cache in order, as
// downloads through c do.
func (w *world) cache(c repo.Config, v Version) error {
	for _, f := range v.Files {
		w.tick()
		if _, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: c, Cached: true, Path: f.Path},
			Body:   bytes.NewReader(f.Body), By: "formattest",
		}); err != nil {
			return fmt.Errorf("cache %s in %s: %w", f.Path, c.Key, err)
		}
	}
	return nil
}

// file returns the item at p in c.
func (w *world) file(c repo.Config, p string) (types.Item, error) {
	it, err := w.st.Items().Get(w.ctx, c.Key, false, p)
	if err != nil {
		return it, fmt.Errorf("get %s in %s: %w", p, c.Key, err)
	}
	return it, nil
}

// indexedVersion is a version as the snapshot shows it.
type indexedVersion struct {
	OrderKey                int64
	Created, LastDownloaded time.Time
	Metadata                string
	Files                   []string
}

// indexedPackage is a package as the snapshot shows it.
type indexedPackage struct {
	Type     repo.PackageType
	Created  time.Time
	Versions map[string]indexedVersion
}

// snapshot is a repository's whole index, with files named by path so it
// compares across reindexes, which may recreate versions under new IDs.
type snapshot map[string]indexedPackage

func (w *world) snapshot(c repo.Config) (snapshot, error) {
	out := snapshot{}
	for page := (store.Page{Limit: store.MaxLimit}); ; {
		pkgs, next, err := w.st.Packages().List(w.ctx, c.Key, page)
		if err != nil {
			return nil, fmt.Errorf("packages of %s: %w", c.Key, err)
		}
		for _, p := range pkgs {
			ip := indexedPackage{
				Type:     p.Type,
				Created:  p.Created,
				Versions: map[string]indexedVersion{},
			}
			vs, err := w.st.Packages().Versions(w.ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("versions of %s: %w", p.Name, err)
			}
			for _, v := range vs {
				ids, err := w.st.Packages().Files(w.ctx, v.ID)
				if err != nil {
					return nil, fmt.Errorf("files of %s %s: %w", p.Name, v.Version, err)
				}
				iv := indexedVersion{
					OrderKey: v.OrderKey, Created: v.Created, LastDownloaded: v.LastDownloaded,
					Metadata: string(v.Metadata),
				}
				for _, id := range ids {
					it, err := w.st.Items().GetByID(w.ctx, id)
					if err != nil {
						return nil, fmt.Errorf("item %d of %s %s: %w", id, p.Name, v.Version, err)
					}
					iv.Files = append(iv.Files, it.FullPath())
				}
				slices.Sort(iv.Files)
				ip.Versions[v.Version] = iv
			}
			out[p.Name] = ip
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

var sameInstant = cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })

// verify holds c's index to its tree. A format that keeps an index must be
// a Reindexer, and a reindex over the index the hooks kept must change
// nothing; a reindex from an empty index must rebuild it, but for what only
// history knows: a package's first push and a version's last download.
// A format that is no Reindexer must keep no index.
func (w *world) verify(cs ...repo.Config) error {
	for _, c := range cs {
		if err := w.verifyOne(c); err != nil {
			return fmt.Errorf("%s: %w", c.Key, err)
		}
	}
	return nil
}

func (w *world) verifyOne(c repo.Config) error {
	kept, err := w.snapshot(c)
	if err != nil {
		return err
	}
	if _, ok := w.h.(format.Reindexer); !ok {
		if len(kept) > 0 {
			return fmt.Errorf("%w: a format that cannot reindex indexed %v", errBroken, kept)
		}
		return nil
	}
	if err := w.items.Reindex(w.ctx, c); err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	again, err := w.snapshot(c)
	if err != nil {
		return err
	}
	if diff := cmp.Diff(kept, again, sameInstant, cmpopts.EquateEmpty()); diff != "" {
		return fmt.Errorf("%w: a reindex changed the index the hooks kept "+
			"(-kept +reindexed):\n%s", errBroken, diff)
	}
	if err := w.wipe(c); err != nil {
		return err
	}
	if err := w.items.Reindex(w.ctx, c); err != nil {
		return fmt.Errorf("reindex from empty: %w", err)
	}
	rebuilt, err := w.snapshot(c)
	if err != nil {
		return err
	}
	historyOnly := cmp.Options{
		cmpopts.IgnoreFields(indexedPackage{}, "Created"),
		cmpopts.IgnoreFields(indexedVersion{}, "LastDownloaded"),
	}
	if diff := cmp.Diff(
		kept,
		rebuilt,
		sameInstant,
		cmpopts.EquateEmpty(),
		historyOnly,
	); diff != "" {
		return fmt.Errorf("%w: a reindex from empty differs from the index the hooks kept "+
			"(-kept +rebuilt):\n%s", errBroken, diff)
	}
	return nil
}

// wipe removes c's whole index.
func (w *world) wipe(c repo.Config) error {
	pkgs, _, err := w.st.Packages().List(w.ctx, c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		return fmt.Errorf("packages of %s: %w", c.Key, err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			return fmt.Errorf("drop %s: %w", p.Name, err)
		}
	}
	return nil
}

// expect checks that c's index holds exactly the versions vs, each linking
// its files.
func (w *world) expect(c repo.Config, vs ...Version) error {
	want := map[string]map[string][]string{}
	for _, v := range vs {
		if v.Package == "" {
			continue
		}
		linked := slices.Clone(v.Linked)
		if linked == nil {
			linked = pathsOf(principal(v))
		}
		slices.Sort(linked)
		if want[v.Package] == nil {
			want[v.Package] = map[string][]string{}
		}
		want[v.Package][v.Version] = linked
	}
	s, err := w.snapshot(c)
	if err != nil {
		return err
	}
	got := map[string]map[string][]string{}
	for name, p := range s {
		got[name] = map[string][]string{}
		for ver, v := range p.Versions {
			got[name][ver] = v.Files
		}
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		return fmt.Errorf("%w: %s's index (-want +got):\n%s", errBroken, c.Key, diff)
	}
	return nil
}

// work counts the queued items of each kind.
func (w *world) work() (map[string]int64, error) {
	stats, err := w.st.WorkQueue().Stats(w.ctx)
	if err != nil {
		return nil, fmt.Errorf("work queue: %w", err)
	}
	out := map[string]int64{}
	for _, s := range stats {
		if n := s.Pending + s.Leased + s.Parked; n > 0 {
			out[s.Kind] = n
		}
	}
	return out, nil
}

// drain completes every queued item, so the next write's work is all the
// queue then holds.
func (w *world) drain() error {
	q := w.st.WorkQueue()
	for {
		items, err := q.Lease(w.ctx, store.WorkLease{
			Holder: "formattest", Limit: store.MaxLimit, Until: w.now.Add(time.Hour), Now: w.now,
		})
		if err != nil {
			return fmt.Errorf("lease the queued work: %w", err)
		}
		if len(items) == 0 {
			return nil
		}
		for _, it := range items {
			if err := q.Complete(w.ctx, it.Kind, it.Key, it.Generation, w.now); err != nil {
				return fmt.Errorf("complete %s %s: %w", it.Kind, it.Key, err)
			}
		}
	}
}

// repoWork checks that the queue holds exactly one item of each kind the
// format keys by repository for each of cs, keyed by its key, and none for
// any other repository.
func (w *world) repoWork(after string, cs ...repo.Config) error {
	got, err := w.work()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(cs))
	for _, c := range cs {
		keys = append(keys, c.Key)
	}
	for _, k := range w.f.RepoWork {
		if got[k] != int64(len(cs)) {
			return fmt.Errorf("%w: after %s, %d queued %s items; want one for each of %v",
				errBroken, after, got[k], k, keys)
		}
		for _, key := range keys {
			if _, err := w.st.WorkQueue().Get(w.ctx, k, key); err != nil {
				return fmt.Errorf("%w: after %s, no queued %s item keyed %s: %w",
					errBroken, after, k, key, err)
			}
		}
	}
	return nil
}

// holds checks that every path is a file in c when want is set, and that
// none is when it is not.
func (w *world) holds(c repo.Config, want bool, paths ...string) error {
	for _, p := range paths {
		it, err := w.st.Items().Get(w.ctx, c.Key, c.Class == repo.Remote, p)
		switch {
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("get %s in %s: %w", p, c.Key, err)
		case want && (err != nil || it.Folder):
			return fmt.Errorf("%w: %s lacks the file %s", errBroken, c.Key, p)
		case !want && err == nil:
			return fmt.Errorf("%w: %s still holds %s", errBroken, c.Key, p)
		}
	}
	return nil
}

// remove deletes the item at p in c, or in its cache for a remote.
func (w *world) remove(c repo.Config, p string) error {
	w.tick()
	if _, err := w.items.Delete(
		w.ctx,
		item.Target{Repo: c, Cached: c.Class == repo.Remote, Path: p},
		"formattest",
	); err != nil {
		return fmt.Errorf("delete %s from %s: %w", p, c.Key, err)
	}
	return nil
}

// refused checks that err is the format's refusal with status.
func refused(err error, status int) error {
	var r *format.Rejection
	switch {
	case err == nil:
		return fmt.Errorf("%w: want a %d refusal, got none", errBroken, status)
	case !errors.As(err, &r) || r.Status != status:
		return fmt.Errorf("%w: want a %d refusal, got %w", errBroken, status, err)
	}
	return nil
}

// paths lists v's file paths.
func paths(v Version) string {
	out := make([]string, 0, len(v.Files))
	for _, f := range v.Files {
		out = append(out, f.Path)
	}
	return strings.Join(out, ", ")
}
