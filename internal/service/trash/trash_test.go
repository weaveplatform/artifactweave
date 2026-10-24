package trash_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type world struct {
	db    *inmem.Store
	items *item.Service
	trash *trash.Service
	clock time.Time
	libs  repo.Config
	other repo.Config
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{db: inmem.New(), clock: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	now := func() time.Time { return w.clock }
	w.items = &item.Service{Store: w.db, Blobs: blobs, Now: now}
	w.trash = &trash.Service{Store: w.db, Items: w.items, Now: now, Batch: 2}
	for _, key := range []string{"libs", "other"} {
		if err := w.db.Repos().
			Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Local})); err != nil {
			t.Fatal(err)
		}
	}
	w.libs, _ = w.db.Repos().Get(ctx, "libs")
	w.other, _ = w.db.Repos().Get(ctx, "other")
	return w
}

func (w *world) deploy(
	t *testing.T,
	c repo.Config,
	p, body string,
	props types.Properties,
) types.Item {
	t.Helper()
	it, err := w.items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: c, Path: p}, Body: strings.NewReader(body), Properties: props,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (w *world) remove(t *testing.T, c repo.Config, p string) {
	t.Helper()
	if _, err := w.items.Delete(
		context.Background(),
		item.Target{Repo: c, Path: p},
		"alice",
	); err != nil {
		t.Fatal(err)
	}
}

func (w *world) refs(t *testing.T, sha string) int64 {
	t.Helper()
	b, err := w.db.Blobs().Get(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return b.Refs
}

func (w *world) exists(c repo.Config, p string) bool {
	_, err := w.db.Items().Get(context.Background(), c.Key, false, p)
	return err == nil
}

func TestUnitSettings(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	got, err := trash.Settings(ctx, db)
	if err != nil || !got.Enabled || got.RetentionDays != trash.DefaultRetentionDays {
		t.Fatalf("unset = %+v, %v", got, err)
	}
	if err := db.Settings().Put(ctx, types.SettingTrashcan,
		types.TrashcanSettings{Enabled: false, RetentionDays: 3}); err != nil {
		t.Fatal(err)
	}
	if got, err = trash.Settings(ctx, db); err != nil || got.Enabled || got.RetentionDays != 3 {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if err := db.Settings().
		Put(ctx, types.SettingTrashcan, map[string]bool{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	if got, err = trash.Settings(
		ctx,
		db,
	); err != nil ||
		got.RetentionDays != trash.DefaultRetentionDays {
		t.Fatalf("no retention stored = %+v, %v", got, err)
	}
	if err := db.Settings().Put(ctx, types.SettingTrashcan, "not an object"); err != nil {
		t.Fatal(err)
	}
	if _, err := trash.Settings(ctx, db); err == nil {
		t.Fatal("a malformed setting must fail")
	}
}

func TestUnitRestoreToOriginal(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	f := w.deploy(t, w.libs, "a/b/f.txt", "content", types.Properties{"k": {"v"}})
	w.deploy(t, w.libs, "a/g.txt", "other content", nil)
	w.remove(t, w.libs, "a")
	if w.exists(w.libs, "a/b/f.txt") || w.refs(t, f.Actual.SHA256) != 1 {
		t.Fatal("a trashed file must be gone from the repository and keep its reference")
	}
	n, err := w.trash.Restore(ctx, "libs", "a", trash.Destination{Repo: w.libs}, "bob", nil)
	if err != nil || n != 4 {
		t.Fatalf("restored %d, %v; want the folders a and a/b and two files", n, err)
	}
	got, err := w.db.Items().Get(ctx, "libs", false, "a/b/f.txt")
	if err != nil || got.Actual.SHA256 != f.Actual.SHA256 || !got.Created.Equal(f.Created) {
		t.Fatalf("restored %+v, %v", got, err)
	}
	props, err := w.db.Items().Properties(ctx, got.ID)
	if err != nil || props["k"][0] != "v" {
		t.Fatalf("properties %v, %v", props, err)
	}
	if w.refs(t, f.Actual.SHA256) != 1 {
		t.Fatal("restoring must pass the trash entry's reference on, not take another")
	}
	if left, _ := w.trash.Entries(ctx, "libs", ""); len(left) != 0 {
		t.Fatalf("restored entries left in the trash: %v", left)
	}
	if _, err := w.trash.Restore(
		ctx,
		"libs",
		"a",
		trash.Destination{Repo: w.libs},
		"bob",
		nil,
	); !errors.Is(
		err,
		trash.ErrNothingTrashed,
	) {
		t.Fatalf("second restore: %v", err)
	}
}

func TestUnitRestoreElsewhere(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "a/b/f.txt", "content", nil)
	w.remove(t, w.libs, "a/b")
	if _, err := w.trash.Restore(
		ctx,
		"libs",
		"a/b",
		trash.Destination{Repo: w.other, Path: "x/y"},
		"bob",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if !w.exists(w.other, "x/y") || !w.exists(w.other, "x/y/f.txt") ||
		w.exists(w.libs, "a/b/f.txt") {
		t.Fatal("restore into another repository and path")
	}
	// A whole repository restored below a path keeps its tree under it.
	w.deploy(t, w.libs, "top.txt", "top", nil)
	w.remove(t, w.libs, "top.txt")
	if _, err := w.trash.Restore(
		ctx,
		"libs",
		"",
		trash.Destination{Repo: w.other, Path: "all"},
		"bob",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if !w.exists(w.other, "all/top.txt") {
		t.Fatal("restore of a repository below a path")
	}
}

func TestUnitRestoreNewestAndOverwrite(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "f.txt", "first", nil)
	w.remove(t, w.libs, "f.txt")
	w.clock = w.clock.Add(time.Hour)
	second := w.deploy(t, w.libs, "f.txt", "second", nil)
	w.remove(t, w.libs, "f.txt")
	current := w.deploy(t, w.libs, "f.txt", "current", nil)
	if _, err := w.trash.Restore(
		ctx,
		"libs",
		"f.txt",
		trash.Destination{Repo: w.libs},
		"bob",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	got, err := w.db.Items().Get(ctx, "libs", false, "f.txt")
	if err != nil || got.Actual.SHA256 != second.Actual.SHA256 {
		t.Fatalf("restored %+v, %v; want the newest trashed copy", got, err)
	}
	if w.refs(t, current.Actual.SHA256) != 0 {
		t.Fatal("the file a restore replaces releases its reference")
	}
	if left, _ := w.trash.Entries(ctx, "libs", "f.txt"); len(left) != 1 {
		t.Fatalf("the older copy stays in the trash: %v", left)
	}
}

func TestUnitRestorePermit(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	// Four items in two batches of two, the refused one last: the first
	// batch must not be restored either.
	for _, p := range []string{"a/1.txt", "a/2.txt", "a/secret.txt"} {
		w.deploy(t, w.libs, p, p, nil)
	}
	w.remove(t, w.libs, "a")
	var asked []string
	_, err := w.trash.Restore(ctx, "libs", "a", trash.Destination{Repo: w.other}, "bob",
		func(it types.Item) bool {
			asked = append(asked, it.RepoKey+"/"+it.FullPath())
			return it.FullPath() != "a/secret.txt"
		})
	if !errors.Is(err, trash.ErrNotPermitted) ||
		!strings.Contains(err.Error(), "libs/a/secret.txt") {
		t.Fatalf("restore with a refused item: %v", err)
	}
	if len(asked) != 4 || asked[0] != "libs/a" {
		t.Fatalf("the permit is asked about every selected item: %v", asked)
	}
	if w.exists(w.other, "a") || w.exists(w.other, "a/1.txt") {
		t.Fatal("a refused restore moves nothing")
	}
	if left, _ := w.trash.Entries(ctx, "libs", ""); len(left) != 4 {
		t.Fatalf("a refused restore keeps every entry: %v", left)
	}
	n, err := w.trash.Restore(ctx, "libs", "a/1.txt", trash.Destination{Repo: w.other}, "bob",
		func(types.Item) bool { return true })
	if err != nil || n != 1 || !w.exists(w.other, "a/1.txt") {
		t.Fatalf("permitted restore: %d, %v", n, err)
	}
	// Nothing trashed is reported before any item is asked about.
	_, err = w.trash.Restore(ctx, "libs", "absent", trash.Destination{Repo: w.other}, "bob",
		func(types.Item) bool { t.Fatal("asked with nothing selected"); return false })
	if !errors.Is(err, trash.ErrNothingTrashed) {
		t.Fatalf("restore of nothing: %v", err)
	}
}

func TestUnitRestoreConflict(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "a/f.txt", "content", nil)
	w.remove(t, w.libs, "a/f.txt")
	w.deploy(t, w.libs, "a/f.txt/below.txt", "a folder now", nil)
	_, err := w.trash.Restore(ctx, "libs", "a/f.txt", trash.Destination{Repo: w.libs}, "bob", nil)
	if !errors.Is(err, item.ErrConflict) {
		t.Fatalf("restore over a folder: %v", err)
	}
	if left, _ := w.trash.Entries(ctx, "libs", ""); len(left) != 1 {
		t.Fatal("a failed restore keeps its entry")
	}
}

func TestUnitRestoreIntoExistingFolder(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.items.MakeFolder(ctx, item.Target{Repo: w.libs, Path: "d"},
		types.Properties{"old": {"1"}}, "alice"); err != nil {
		t.Fatal(err)
	}
	w.remove(t, w.libs, "d")
	keep, err := w.items.MakeFolder(ctx, item.Target{Repo: w.libs, Path: "d"},
		types.Properties{"new": {"2"}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.trash.Restore(
		ctx,
		"libs",
		"d",
		trash.Destination{Repo: w.libs},
		"bob",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	props, _ := w.db.Items().Properties(ctx, keep.ID)
	if props["old"][0] != "1" || props["new"][0] != "2" {
		t.Fatalf("a folder restored onto one recreated since keeps both: %v", props)
	}
}

// Keep records a repository's whole content as trashed, folders included,
// each item with its own properties and the repository's package type, and
// counts the references the file entries hold. It deletes nothing: the
// repository delete that calls it does, in the same transaction (#504).
func TestUnitKeep(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	one := w.deploy(t, w.other, "a/one.txt", "same", types.Properties{"k": {"v"}}).Actual.SHA256
	w.deploy(t, w.other, "a/b/two.txt", "same", nil)
	three := w.deploy(t, w.other, "three.txt", "three", nil).Actual.SHA256
	held, err := w.trash.Keep(ctx, w.other, "carol")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 2 || held[one] != 2 || held[three] != 1 {
		t.Fatalf("held %v", held)
	}
	entries, err := w.trash.Entries(ctx, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Record.Item.FullPath())
		if e.DeletedBy != "carol" || !e.DeletedAt.Equal(w.clock) ||
			e.Record.PackageType != string(w.other.PackageType) {
			t.Errorf("entry %+v", e)
		}
		if e.Record.Item.Name == "one.txt" && strings.Join(e.Record.Properties["k"], ",") != "v" {
			t.Errorf("one.txt's properties %v", e.Record.Properties)
		}
	}
	slices.Sort(paths)
	if strings.Join(paths, " ") != "a a/b a/b/two.txt a/one.txt three.txt" {
		t.Fatalf("kept %v", paths)
	}
	if !w.exists(w.other, "a/one.txt") || w.refs(t, one) != 2 {
		t.Fatal("Keep deleted or released something")
	}
}

// Keep reads a repository a page at a time, and keeps every page.
func TestUnitKeepPages(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	const n = store.MaxLimit + 1
	for i := range n {
		if _, err := w.db.Items().
			Put(ctx, types.Item{RepoKey: "other", Name: "f" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if held, err := w.trash.Keep(ctx, w.other, "carol"); err != nil || len(held) != 0 {
		t.Fatalf("Keep = %v, %v", held, err)
	}
	if entries, err := w.trash.Entries(ctx, "other", ""); err != nil || len(entries) != n {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
}

func TestUnitCleanEmptyPurge(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var shas []string
	for _, p := range []string{"a/1.txt", "a/2.txt", "b/3.txt"} {
		shas = append(shas, w.deploy(t, w.libs, p, "content of "+p, nil).Actual.SHA256)
	}
	w.remove(t, w.libs, "a")
	w.remove(t, w.libs, "b")
	n, err := w.trash.Clean(ctx, "libs", "a")
	if err != nil || n != 3 || w.refs(t, shas[0]) != 0 || w.refs(t, shas[2]) != 1 {
		t.Fatalf("clean a: %d, %v", n, err)
	}
	if n, err = w.trash.Clean(ctx, "libs", "nothing"); err != nil || n != 0 {
		t.Fatalf("clean of nothing: %d, %v", n, err)
	}
	if n, err = w.trash.Empty(ctx); err != nil || n != 2 || w.refs(t, shas[2]) != 0 {
		t.Fatalf("empty: %d, %v", n, err)
	}

	// Expiry follows the retention setting.
	w.deploy(t, w.libs, "old.txt", "old", nil)
	w.remove(t, w.libs, "old.txt")
	w.clock = w.clock.Add(24 * time.Hour)
	w.deploy(t, w.libs, "new.txt", "new", nil)
	w.remove(t, w.libs, "new.txt")
	if err := w.db.Settings().Put(ctx, types.SettingTrashcan,
		types.TrashcanSettings{Enabled: true, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Hour)
	if n, err = w.trash.PurgeExpired(ctx); err != nil || n != 1 {
		t.Fatalf("purge: %d, %v", n, err)
	}
	if left, _ := w.trash.Entries(
		ctx,
		"",
		"",
	); len(left) != 1 ||
		left[0].Record.Item.Name != "new.txt" {
		t.Fatalf("left %v", left)
	}
}

// While the trash can is off nothing in it expires, as 7.x's small garbage
// collection does not run then; turned on again, the retention applies to
// what it kept (#490).
func TestUnitPurgeExpiredWhileOff(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	sha := w.deploy(t, w.libs, "kept.txt", "kept", nil).Actual.SHA256
	w.remove(t, w.libs, "kept.txt")
	put := func(on bool) {
		t.Helper()
		if err := w.db.Settings().Put(ctx, types.SettingTrashcan,
			types.TrashcanSettings{Enabled: on, RetentionDays: 1}); err != nil {
			t.Fatal(err)
		}
	}
	put(false)
	w.clock = w.clock.Add(30 * 24 * time.Hour)
	if n, err := w.trash.PurgeExpired(ctx); err != nil || n != 0 || w.refs(t, sha) != 1 {
		t.Fatalf("purged while off: %d, %v", n, err)
	}
	if left, _ := w.trash.Entries(ctx, "libs", ""); len(left) != 1 {
		t.Fatalf("left while off %v", left)
	}
	put(true)
	if n, err := w.trash.PurgeExpired(ctx); err != nil || n != 1 || w.refs(t, sha) != 0 {
		t.Fatalf("purged once on: %d, %v", n, err)
	}
}

func TestUnitChildrenAndFind(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "a/b/f.txt", "f", nil)
	w.deploy(t, w.libs, "top.txt", "top", nil)
	w.deploy(t, w.other, "o.txt", "o", nil)
	w.remove(t, w.libs, "a")
	w.remove(t, w.libs, "top.txt")
	w.remove(t, w.other, "o.txt")
	for dir, want := range map[string][]trash.Child{
		"":       {{Name: "libs", Folder: true}, {Name: "other", Folder: true}},
		"libs":   {{Name: "a", Folder: true}, {Name: "top.txt"}},
		"libs/a": {{Name: "b", Folder: true}},
		"libs/x": {},
	} {
		got, err := w.trash.Children(ctx, dir, nil)
		if err != nil || len(got) != len(want) {
			t.Fatalf("%q: %v, %v", dir, got, err)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", dir, got, want)
			}
		}
	}
	e, err := w.trash.Find(ctx, "libs", "top.txt")
	if err != nil || e.DeletedBy != "alice" {
		t.Fatalf("find %+v, %v", e, err)
	}
	if _, err := w.trash.Find(ctx, "libs", "absent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("find absent: %v", err)
	}
}

// Below lists the tree under a folder, direct children or every descendant,
// with the newest entry at each path; a folder only implied by what lies
// below it has none.
func TestUnitBelow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "a/b/f.txt", "f", nil)
	w.deploy(t, w.libs, "top.txt", "first", nil)
	w.remove(t, w.libs, "a/b/f.txt")
	w.remove(t, w.libs, "top.txt")
	w.clock = w.clock.Add(time.Hour)
	w.deploy(t, w.libs, "top.txt", "second", nil)
	w.remove(t, w.libs, "top.txt")
	// A file trashed at x, then a folder x holding a trashed file: the
	// folder takes the file's place, as in Artifactory's trash can.
	w.deploy(t, w.other, "x", "file", nil)
	w.remove(t, w.other, "x")
	w.deploy(t, w.other, "x/y.txt", "y", nil)
	w.remove(t, w.other, "x/y.txt")
	type got struct {
		path          string
		folder, entry bool
	}
	for _, tc := range []struct {
		dir  string
		deep bool
		want []got
	}{
		{"", false, []got{{"libs", true, false}, {"other", true, false}}},
		{"", true, []got{
			{"libs", true, false},
			{"libs/a", true, false},
			{"libs/a/b", true, false},
			{"libs/a/b/f.txt", false, true},
			{"libs/top.txt", false, true},
			{"other", true, false},
			{"other/x", true, false},
			{"other/x/y.txt", false, true},
		}},
		{"libs/a", true, []got{{"libs/a/b", true, false}, {"libs/a/b/f.txt", false, true}}},
		{"other", false, []got{{"other/x", true, false}}},
	} {
		nodes, err := w.trash.Below(ctx, tc.dir, tc.deep, nil)
		if err != nil {
			t.Fatal(err)
		}
		var have []got
		for _, n := range nodes {
			have = append(have, got{n.Path, n.Folder, n.Entry != nil})
		}
		if !slices.Equal(have, tc.want) {
			t.Errorf("Below(%q, %v) = %v, want %v", tc.dir, tc.deep, have, tc.want)
		}
	}
	nodes, err := w.trash.Below(ctx, "libs", false, nil)
	if err != nil || len(nodes) != 2 || nodes[1].Entry == nil ||
		!nodes[1].Entry.DeletedAt.Equal(w.clock) {
		t.Fatalf("newest top.txt: %+v, %v", nodes, err)
	}
	if nodes[1].Entry.Record.PackageType != string(repo.Generic) {
		t.Fatalf("package type %q", nodes[1].Entry.Record.PackageType)
	}
}

// A repository or folder shows only when an entry the filter keeps lies at
// or below it.
func TestUnitChildrenFiltered(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "a/b/f.txt", "f", nil)
	w.deploy(t, w.libs, "a/c/g.txt", "g", nil)
	w.deploy(t, w.libs, "top.txt", "top", nil)
	w.deploy(t, w.other, "o.txt", "o", nil)
	w.remove(t, w.libs, "a")
	w.remove(t, w.libs, "top.txt")
	w.remove(t, w.other, "o.txt")
	// Everything trashed from libs under a/b, and the folders on the way.
	sees := func(it types.Item) bool {
		return it.RepoKey == "libs" &&
			(it.FullPath() == "a" || strings.HasPrefix(it.FullPath(), "a/b"))
	}
	for dir, want := range map[string][]trash.Child{
		"":       {{Name: "libs", Folder: true}},
		"libs":   {{Name: "a", Folder: true}},
		"libs/a": {{Name: "b", Folder: true}},
		"libs/c": {},
		"other":  {},
	} {
		got, err := w.trash.Children(ctx, dir, sees)
		if err != nil || len(got) != len(want) {
			t.Fatalf("%q: %v, %v", dir, got, err)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", dir, got, want)
			}
		}
	}
}

var errBoom = errors.New("boom")

// failing is an in-memory store whose trash, blob index or settings fail the
// operations named in fail.
type failing struct {
	*inmem.Store
	fail map[string]error
}

func (f failing) Trash() store.TrashStore       { return failTrash{f.Store.Trash(), f.fail} }
func (f failing) Blobs() store.BlobIndex        { return failBlobs{f.Store.Blobs(), f.fail} }
func (f failing) Settings() store.SettingsStore { return failSettings{f.Store.Settings(), f.fail} }
func (f failing) Items() store.ItemStore        { return failItems{f.Store.Items(), f.fail} }

type failItems struct {
	store.ItemStore
	fail map[string]error
}

func (f failItems) Descendants(
	ctx context.Context,
	r string,
	c bool,
	p string,
	pg store.Page,
) ([]types.Item, string, error) {
	if err := f.fail["descendants"]; err != nil {
		return nil, "", err
	}
	return f.ItemStore.Descendants(ctx, r, c, p, pg) //nolint:wrapcheck // test double
}

func (f failItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if err := f.fail["properties"]; err != nil {
		return nil, err
	}
	return f.ItemStore.Properties(ctx, id) //nolint:wrapcheck // test double
}

func (f failing) Tx(ctx context.Context, fn func(context.Context) error) error {
	return f.Store.Tx(ctx, fn) //nolint:wrapcheck // test double
}

type failTrash struct {
	store.TrashStore
	fail map[string]error
}

func (f failTrash) List(
	ctx context.Context,
	r, p string,
	pg store.Page,
) ([]types.TrashEntry, string, error) {
	if err := f.fail["list"]; err != nil {
		return nil, "", err
	}
	return f.TrashStore.List(ctx, r, p, pg) //nolint:wrapcheck // test double
}

func (f failTrash) Find(ctx context.Context, r, p string) ([]types.TrashEntry, error) {
	if err := f.fail["find"]; err != nil {
		return nil, err
	}
	return f.TrashStore.Find(ctx, r, p) //nolint:wrapcheck // test double
}

func (f failTrash) Expired(
	ctx context.Context,
	before time.Time,
	n int,
) ([]types.TrashEntry, error) {
	if err := f.fail["expired"]; err != nil {
		return nil, err
	}
	return f.TrashStore.Expired(ctx, before, n) //nolint:wrapcheck // test double
}

func (f failTrash) Add(ctx context.Context, e types.TrashEntry) (types.TrashEntry, error) {
	if err := f.fail["add"]; err != nil {
		return e, err
	}
	return f.TrashStore.Add(ctx, e) //nolint:wrapcheck // test double
}

func (f failTrash) Delete(ctx context.Context, id int64) error {
	if err := f.fail["delete"]; err != nil {
		return err
	}
	return f.TrashStore.Delete(ctx, id) //nolint:wrapcheck // test double
}

type failBlobs struct {
	store.BlobIndex
	fail map[string]error
}

func (f failBlobs) Unref(ctx context.Context, sha string, n int64) (types.Blob, error) {
	if err := f.fail["unref"]; err != nil {
		return types.Blob{}, err
	}
	return f.BlobIndex.Unref(ctx, sha, n) //nolint:wrapcheck // test double
}

type failSettings struct {
	store.SettingsStore
	fail map[string]error
}

func (f failSettings) Get(ctx context.Context, key string, into any) error {
	if err := f.fail["settings"]; err != nil {
		return err
	}
	return f.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

func TestUnitStoreFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, w.libs, "a/f.txt", "content", nil)
	w.remove(t, w.libs, "a")
	w.deploy(t, w.other, "o.txt", "other", nil)
	svc := func(fail map[string]error) *trash.Service {
		st := failing{w.db, fail}
		return &trash.Service{Store: st, Items: &item.Service{Store: st, Blobs: w.items.Blobs}}
	}
	dst := trash.Destination{Repo: w.libs}
	for name, run := range map[string]func() error{
		"restore list": func() error {
			_, err := svc(map[string]error{"list": errBoom}).Restore(ctx, "libs", "a", dst, "bob", nil)
			return err
		},
		"restore drop": func() error {
			_, err := svc(map[string]error{"delete": errBoom}).Restore(ctx, "libs", "a", dst, "bob", nil)
			return err
		},
		"clean list": func() error {
			_, err := svc(map[string]error{"list": errBoom}).Clean(ctx, "libs", "a")
			return err
		},
		"clean delete": func() error {
			_, err := svc(map[string]error{"delete": errBoom}).Clean(ctx, "libs", "a")
			return err
		},
		"clean unref": func() error {
			_, err := svc(map[string]error{"unref": errBoom}).Clean(ctx, "libs", "a")
			return err
		},
		"empty list": func() error {
			_, err := svc(map[string]error{"list": errBoom}).Empty(ctx)
			return err
		},
		"purge settings": func() error {
			_, err := svc(map[string]error{"settings": errBoom}).PurgeExpired(ctx)
			return err
		},
		"purge expired": func() error {
			_, err := svc(map[string]error{"expired": errBoom}).PurgeExpired(ctx)
			return err
		},
		"children": func() error {
			_, err := svc(map[string]error{"list": errBoom}).Children(ctx, "libs", nil)
			return err
		},
		"find": func() error {
			_, err := svc(map[string]error{"find": errBoom}).Find(ctx, "libs", "a")
			return err
		},
		"keep list": func() error {
			_, err := svc(map[string]error{"descendants": errBoom}).Keep(ctx, w.other, "bob")
			return err
		},
		"keep properties": func() error {
			_, err := svc(map[string]error{"properties": errBoom}).Keep(ctx, w.other, "bob")
			return err
		},
		"keep add": func() error {
			_, err := svc(map[string]error{"add": errBoom}).Keep(ctx, w.other, "bob")
			return err
		},
	} {
		if err := run(); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An entry purged concurrently, or a blob row already gone, is skipped.
	n, err := svc(map[string]error{"delete": store.ErrNotFound}).Clean(ctx, "libs", "a")
	if err != nil || n != 0 {
		t.Fatalf("entries already gone: %d, %v", n, err)
	}
	if n, err = svc(
		map[string]error{"unref": store.ErrNotFound},
	).Clean(ctx, "libs", "a"); err != nil ||
		n != 2 {
		t.Fatalf("blob row already gone: %d, %v", n, err)
	}
}

func TestUnitDefaultClock(t *testing.T) {
	w := newWorld(t)
	w.clock = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	w.deploy(t, w.libs, "f.txt", "content", nil)
	w.remove(t, w.libs, "f.txt") // trashed in 2020, long past retention
	n, err := (&trash.Service{Store: w.db}).PurgeExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("purge against the real clock: %d, %v", n, err)
	}
}
