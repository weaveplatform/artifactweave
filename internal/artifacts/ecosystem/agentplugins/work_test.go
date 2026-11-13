package agentplugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// errFault is what the faulty stores here answer.
var errFault = errors.New("the store failed")

// listingFails is a store whose package listings fail.
type listingFails struct{ format.Store }

func (s listingFails) Packages() port.PackageStore { return failingList{s.Store.Packages()} }

type failingList struct{ port.PackageStore }

func (failingList) List(context.Context, string, kernel.Page) ([]model.Package, string, error) {
	return nil, "", errFault
}

// versionsFail is a store whose version listings fail.
type versionsFail struct{ format.Store }

func (s versionsFail) Packages() port.PackageStore { return failingVersions{s.Store.Packages()} }

type failingVersions struct{ port.PackageStore }

func (failingVersions) Versions(context.Context, int64) ([]model.PackageVersion, error) {
	return nil, errFault
}

// itemGetFails is a store whose item lookups fail.
type itemGetFails struct{ format.Store }

func (s itemGetFails) Items() port.ItemStore { return failingGet{s.Store.Items()} }

type failingGet struct{ port.ItemStore }

func (failingGet) Get(context.Context, string, bool, string) (model.Item, error) {
	return model.Item{}, errFault
}

// repoGetFails is a store whose repository lookups fail.
type repoGetFails struct{ format.Store }

func (s repoGetFails) Repos() port.RepoStore { return failingRepo{s.Store.Repos()} }

type failingRepo struct{ port.RepoStore }

func (failingRepo) Get(context.Context, string) (repo.Config, error) {
	return repo.Config{}, errFault
}

// queueGetFails is a work queue whose reads fail.
type queueGetFails struct{ work.WorkQueue }

func (queueGetFails) Get(context.Context, string, string) (work.WorkItem, error) {
	return work.WorkItem{}, errFault
}

// workWorld is a store with a local Agent Plugins repository, and an Env
// whose Derive stores a file as the item service does, without hooks, and
// whose Enqueue records the work asked for and queues it in the store.
type workWorld struct {
	t       *testing.T
	st      *inmem.Store
	c       repo.Config
	env     format.Env
	derived []string
	queued  []string
}

func newWorkWorld(t *testing.T) *workWorld {
	t.Helper()
	w := &workWorld{
		t:  t,
		st: inmem.New(),
		c:  repo.Config{Key: "plugins", Class: repo.Local, PackageType: repo.AgentPlugins},
	}
	if err := w.st.Repos().Create(t.Context(), *repo.Defaults(&w.c)); err != nil {
		t.Fatal(err)
	}
	w.env = format.Env{
		Store:     w.st,
		WorkQueue: w.st.WorkQueue(),
		Derive: func(ctx context.Context, d format.Derived) (model.Item, error) {
			data, err := io.ReadAll(d.Body)
			if err != nil {
				return model.Item{}, err
			}
			sum := sha256.Sum256(data)
			w.derived = append(w.derived, d.Path+" by "+d.By)
			return w.st.Items().Put(ctx, model.Item{
				RepoKey: d.Repo.Key, Name: d.Path, Size: int64(len(data)),
				Actual: kernel.Checksums{SHA256: hex.EncodeToString(sum[:])},
			})
		},
		Enqueue: func(ctx context.Context, kind, key string, payload []byte) error {
			w.queued = append(w.queued, kind+" "+key)
			return w.st.WorkQueue().Enqueue(ctx, kind, key, payload, time.Now())
		},
	}
	return w
}

// plugin indexes version of plugin name with the manifests of harnesses.
func (w *workWorld) plugin(name, version string, harnesses ...string) {
	w.t.Helper()
	ctx := w.t.Context()
	p, err := w.st.Packages().Ensure(ctx, model.Package{
		RepoKey: w.c.Key, Type: repo.AgentPlugins, Name: name, Created: time.Now(),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	meta, err := json.Marshal(Metadata{
		Name: name, Version: version, Path: Path(name, version), Harnesses: harnesses,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.st.Packages().PutVersion(ctx, model.PackageVersion{
		PackageID: p.ID, Version: version, Metadata: meta,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// run runs the marketplace work of the repository key.
func (w *workWorld) run(key string) error {
	w.t.Helper()
	w.derived = nil
	return Handler{}.Work(w.t.Context(), w.env, work.WorkItem{Kind: KindMarketplace, Key: key})
}

// TestUnitMarketplaceWorkStoresEveryFile: the work stores the five
// marketplace files at the repository root, as the system, with what
// marketplaces builds, and on a later run rewrites only a file whose
// content changed, so the others keep their dates and checksums.
func TestUnitMarketplaceWorkStoresEveryFile(t *testing.T) {
	w := newWorkWorld(t)
	w.plugin("alpha", "1.0.0", "claude")
	if err := w.run(w.c.Key); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude-marketplace.json by _system_",
		"codex-marketplace.json by _system_",
		"cursor-marketplace.json by _system_",
		"marketplace.json by _system_",
		"vscode-marketplace.json by _system_",
	}
	if !slices.Equal(w.derived, want) {
		t.Fatalf("stored %v, want %v", w.derived, want)
	}
	built, err := marketplaces(t.Context(), w.st, w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range built {
		it, err := w.st.Items().Get(t.Context(), w.c.Key, false, name)
		sum := sha256.Sum256(data)
		if err != nil || it.Actual.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("%s stored as %+v (%v), want the built content", name, it, err)
		}
	}
	if err := w.run(w.c.Key); err != nil || len(w.derived) != 0 {
		t.Errorf("an unchanged index rewrote %v (%v), want nothing", w.derived, err)
	}
	w.plugin("beta", "1.0.0", "cursor")
	if err := w.run(w.c.Key); err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"cursor-marketplace.json by _system_", "marketplace.json by _system_",
	}; !slices.Equal(w.derived, want) {
		t.Errorf("a Cursor plugin rewrote %v, want %v", w.derived, want)
	}
}

// TestUnitMarketplaceWorkLeavesAFolderInPlace: a folder at a marketplace
// name is left as it is, and the other files are stored, rather than the
// work failing on every run until the queue parks it.
func TestUnitMarketplaceWorkLeavesAFolderInPlace(t *testing.T) {
	w := newWorkWorld(t)
	if _, err := w.st.Items().Put(t.Context(), model.Item{
		RepoKey: w.c.Key, Name: "marketplace.json", Folder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.run(w.c.Key); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.derived, "marketplace.json by _system_") || len(w.derived) != 4 {
		t.Errorf("stored %v, want the four other files", w.derived)
	}
}

// TestUnitMarketplaceWorkOwesNothingElse: work of another kind, for a
// repository that is gone, or for one that is not a local Agent Plugins
// repository stores nothing and succeeds, since no run could do it.
func TestUnitMarketplaceWorkOwesNothingElse(t *testing.T) {
	w := newWorkWorld(t)
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local, PackageType: repo.Generic},
		{Key: "plugins-remote", Class: repo.Remote, PackageType: repo.AgentPlugins, URL: "https://x.example"},
	} {
		if err := w.st.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, it := range []work.WorkItem{
		{Kind: "luarocks.manifest", Key: w.c.Key},
		{Kind: KindMarketplace, Key: "absent"},
		{Kind: KindMarketplace, Key: "libs-local"},
		{Kind: KindMarketplace, Key: "plugins-remote"},
	} {
		if err := (Handler{}).Work(t.Context(), w.env, it); err != nil || len(w.derived) != 0 {
			t.Errorf("%s %s: stored %v (%v), want nothing", it.Kind, it.Key, w.derived, err)
		}
	}
}

// TestUnitMarketplaceWorkFailsOnWhatItCannotDo: a repository, index, item
// or write fault fails the run, so the queue retries it.
func TestUnitMarketplaceWorkFailsOnWhatItCannotDo(t *testing.T) {
	for _, tc := range []struct {
		name string
		tune func(*workWorld)
		want string
	}{
		{"repository", func(w *workWorld) { w.env.Store = repoGetFails{w.st} }, "agentplugins: repository plugins"},
		{"index", func(w *workWorld) { w.env.Store = listingFails{w.st} }, "agentplugins: list packages"},
		{"item", func(w *workWorld) { w.env.Store = itemGetFails{w.st} }, "agentplugins: read claude-marketplace.json"},
		{"write", func(w *workWorld) {
			w.env.Derive = func(context.Context, format.Derived) (model.Item, error) {
				return model.Item{}, errFault
			}
		}, "agentplugins: store claude-marketplace.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkWorld(t)
			tc.tune(w)
			err := w.run(w.c.Key)
			if !errors.Is(err, errFault) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ran with %v, want the fault named %q", err, tc.want)
			}
		})
	}
}

// TestUnitMarketplaceDownloadsOweTheWork: a download of a marketplace file
// of a local repository owes the marketplace work, and nothing else owes
// it. Resolve leaves every path as it is, and queues the work only for a
// marketplace file that is neither stored nor owed already.
func TestUnitMarketplaceDownloadsOweTheWork(t *testing.T) {
	w := newWorkWorld(t)
	h := Handler{}
	if got := h.WorkKinds(); !slices.Equal(got, []string{KindMarketplace}) {
		t.Errorf("work kinds %v", got)
	}
	ref := []format.WorkRef{{Kind: KindMarketplace, Key: w.c.Key}}
	if got := h.Owed(w.c, false, "cursor-marketplace.json"); !slices.Equal(got, ref) {
		t.Errorf("a marketplace file owes %v, want %v", got, ref)
	}
	for _, tc := range []struct {
		name   string
		cached bool
		path   string
	}{
		{"cached", true, "marketplace.json"},
		{"below the root", false, "docs/marketplace.json"},
		{"a bundle", false, Path("alpha", "1.0.0")},
	} {
		if got := h.Owed(w.c, tc.cached, tc.path); got != nil {
			t.Errorf("%s owes %v, want nothing", tc.name, got)
		}
	}
	resolve := func(path string, cached bool) {
		t.Helper()
		got, err := h.Resolve(t.Context(), w.env, w.c, format.Download{Path: path, Cached: cached})
		if err != nil || got != path {
			t.Fatalf("resolved %s to %q (%v), want it unchanged", path, got, err)
		}
	}
	resolve("plugins.json", false)
	resolve("marketplace.json", true)
	if len(w.queued) != 0 {
		t.Fatalf("queued %v for paths that owe nothing", w.queued)
	}
	resolve("marketplace.json", false)
	resolve("cursor-marketplace.json", false)
	if want := []string{KindMarketplace + " plugins"}; !slices.Equal(w.queued, want) {
		t.Fatalf("queued %v, want %v once, then the queued work", w.queued, want)
	}
	if err := w.run(w.c.Key); err != nil {
		t.Fatal(err)
	}
	w.queued = nil
	resolve("marketplace.json", false)
	if len(w.queued) != 0 {
		t.Errorf("queued %v for a stored file", w.queued)
	}
}

// TestUnitMarketplaceResolveFailsOnAFault: an item or work queue fault
// fails the download rather than serving a file that may lag.
func TestUnitMarketplaceResolveFailsOnAFault(t *testing.T) {
	for _, tc := range []struct {
		name string
		tune func(*workWorld)
		want string
	}{
		{"item", func(w *workWorld) { w.env.Store = itemGetFails{w.st} }, "agentplugins: marketplace:"},
		{"work queue", func(w *workWorld) { w.env.WorkQueue = queueGetFails{w.st.WorkQueue()} }, "agentplugins: marketplace work:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkWorld(t)
			tc.tune(w)
			_, err := Handler{}.Resolve(
				t.Context(),
				w.env,
				w.c,
				format.Download{Path: "marketplace.json"},
			)
			if !errors.Is(err, errFault) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("resolved with %v, want the fault named %q", err, tc.want)
			}
		})
	}
}

// TestUnitMarketplaceFilesAreDerived: the marketplace files are derived
// content, which replication leaves for each repository to build, and
// every other path is unique content.
func TestUnitMarketplaceFilesAreDerived(t *testing.T) {
	c := repo.Config{Key: "plugins", Class: repo.Local, PackageType: repo.AgentPlugins}
	for path, want := range map[string]format.Class{
		"marketplace.json":         format.ClassDerived,
		"vscode-marketplace.json":  format.ClassDerived,
		"docs/marketplace.json":    format.ClassUnique,
		Path("alpha", "1.0.0"):     format.ClassUnique,
		"alpha/1.0.0/notes.txt":    format.ClassUnique,
		"codex-marketplace.json.1": format.ClassUnique,
	} {
		if got := (Handler{}).Classify(c, path); got != want {
			t.Errorf("%s classed %v, want %v", path, got, want)
		}
	}
}

// TestUnitMarketplaceWorkIsQueuedByTreeChanges: a bundle's delete, a
// stored marketplace file's restore or delete and a reindex queue the
// marketplace work; folders, cached items and other files do not.
func TestUnitMarketplaceWorkIsQueuedByTreeChanges(t *testing.T) {
	w := newWorkWorld(t)
	h := Handler{}
	ctx := t.Context()
	for _, it := range []model.Item{
		{Name: "alpha/1.0.0", Folder: true},
		{Name: Path("alpha", "1.0.0"), Cached: true},
		{Name: "notes.txt"},
		{Name: "docs/marketplace.json"},
	} {
		if err := h.Deleting(ctx, w.env, w.c, it); err != nil {
			t.Fatal(err)
		}
		if err := h.Deployed(ctx, w.env, w.c, it); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.queued) != 0 {
		t.Fatalf("queued %v for items the files do not follow", w.queued)
	}
	for _, it := range []model.Item{{Name: Path("alpha", "1.0.0")}, {Name: "marketplace.json"}} {
		if err := h.Deleting(ctx, w.env, w.c, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Deployed(ctx, w.env, w.c, model.Item{Name: "cursor-marketplace.json"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(ctx, w.env, w.c); err != nil {
		t.Fatal(err)
	}
	want := slices.Repeat([]string{KindMarketplace + " plugins"}, 4)
	if !slices.Equal(w.queued, want) {
		t.Errorf("queued %v, want %v", w.queued, want)
	}
}
