package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// trashcan turns the trash can on or off.
func trashcan(t *testing.T, db store.Store, on bool) {
	t.Helper()
	if err := db.Settings().Put(context.Background(), types.SettingTrashcan,
		types.TrashcanSettings{Enabled: on, RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
}

// entries counts what the trash can holds from repoKey.
func entries(t *testing.T, db store.Store, repoKey string) int {
	t.Helper()
	all, err := (&trash.Service{Store: db}).Entries(context.Background(), repoKey, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// refs is the reference count of sha's blob.
func refs(t *testing.T, db store.Store, sha string) int64 {
	t.Helper()
	b, err := db.Blobs().Get(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return b.Refs
}

// lifecycle deploys, deletes, restores and collects content for real: a
// store, a blob store, the item service writing to both, and the clock
// garbage collection and the trash can's retention run on.
type lifecycle struct {
	db    *inmem.Store
	blobs *fs.Store
	items *item.Service
	clock time.Time
}

func newLifecycle(t *testing.T) *lifecycle {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Remove dates its trash entries by the real clock, which this one
	// starts from so that expiry is measured from the delete.
	l := &lifecycle{db: inmem.New(), blobs: blobs, clock: time.Now().UTC()}
	l.items = &item.Service{Store: l.db, Blobs: blobs, Now: l.now}
	for _, key := range []string{"libs", "other", "restored"} {
		c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic}
		if err := l.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func (l *lifecycle) now() time.Time { return l.clock }

func (l *lifecycle) repo(t *testing.T, key string) repo.Config {
	t.Helper()
	c, err := l.db.Repos().Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// deploy stores body at p in key and returns its sha256.
func (l *lifecycle) deploy(t *testing.T, key, p, body string, props types.Properties) string {
	t.Helper()
	it, err := l.items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: l.repo(t, key), Path: p}, Body: strings.NewReader(body),
		Properties: props,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it.Actual.SHA256
}

func (l *lifecycle) bin() *trash.Service {
	return &trash.Service{Store: l.db, Items: l.items, Now: l.now}
}

// collect runs a garbage collection pass once the grace period has passed
// for everything deployed so far.
func (l *lifecycle) collect(t *testing.T) gc.Report {
	t.Helper()
	l.clock = l.clock.Add(gc.DefaultGrace + time.Minute)
	c, err := gc.New(l.db, l.blobs, gc.Options{Now: l.now})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func (l *lifecycle) stored(t *testing.T, sha string) bool {
	t.Helper()
	ok, err := l.blobs.Exists(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// While the trash can is on, a repository delete moves its content there
// under the repository's key (#504): every item at its original path, with
// its own properties, as deleted by the caller. Each file's entry holds its
// blob reference, so garbage collection keeps the content until the entry
// expires, and then frees what nothing else holds.
func TestUnitRemoveKeepsContentInTheTrashCan(t *testing.T) {
	ctx := context.Background()
	l := newLifecycle(t)
	one := l.deploy(t, "libs", "a/one.txt", "one", types.Properties{"k": {"v"}})
	l.deploy(t, "libs", "a/two.txt", "one", nil)
	three := l.deploy(t, "libs", "b/three.txt", "three", nil)
	l.deploy(t, "other", "one.txt", "one", nil)
	root, err := l.db.Items().EnsureRoot(ctx, types.Item{RepoKey: "libs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.db.Items().
		PatchProperties(ctx, root.ID, types.Properties{"team": {"x"}}, nil, l.clock); err != nil {
		t.Fatal(err)
	}

	files, found, err := Remove(ctx, l.db, "libs", "admin")
	if err != nil || !found || files != 3 {
		t.Fatalf("Remove = %d, %v, %v", files, found, err)
	}
	if left, _, _ := l.db.Items().
		Descendants(ctx, "libs", false, "", store.Page{}); len(left) != 0 {
		t.Fatalf("content left behind: %v", left)
	}
	if n, m := refs(t, l.db, one), refs(t, l.db, three); n != 3 || m != 1 {
		t.Fatalf("references %d and %d, want 3 and 1: none is released", n, m)
	}
	kept, err := l.bin().Entries(ctx, "libs", "")
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(kept))
	for _, e := range kept {
		it := e.Record.Item
		paths = append(paths, it.FullPath())
		if e.DeletedBy != "admin" || e.Record.PackageType != string(repo.Generic) ||
			it.RepoKey != "libs" || it.IsRoot() {
			t.Errorf("entry %+v", e)
		}
		// The root's properties went with the repository, onto no item.
		if _, ok := e.Record.Properties["team"]; ok {
			t.Errorf("%s carries the root's properties", it.FullPath())
		}
		if it.Name == "one.txt" && strings.Join(e.Record.Properties["k"], ",") != "v" {
			t.Errorf("one.txt lost its properties: %v", e.Record.Properties)
		}
	}
	slices.Sort(paths)
	if strings.Join(paths, " ") != "a a/one.txt a/two.txt b b/three.txt" {
		t.Fatalf("trashed %v", paths)
	}

	if rep := l.collect(t); rep.BlobsDeleted != 0 || rep.TrashPurged != 0 ||
		!l.stored(t, one) || !l.stored(t, three) {
		t.Fatalf("collected trashed content: %+v", rep)
	}
	l.clock = l.clock.Add(trash.DefaultRetentionDays * 24 * time.Hour)
	rep := l.collect(t)
	if rep.TrashPurged != 5 || rep.BlobsDeleted != 1 || l.stored(t, three) || !l.stored(t, one) {
		t.Fatalf("expiry: %+v", rep)
	}
	if n := refs(t, l.db, one); n != 1 {
		t.Fatalf("one.txt's content has %d references, want other's 1", n)
	}
}

// A restore of a deleted repository's root brings its content back into a
// repository that exists, as 7.x restores one (#504), taking over the
// references the entries held: nothing is lost and nothing is counted
// twice, so a later delete without the trash can frees the content.
func TestUnitRemoveThenRestore(t *testing.T) {
	ctx := context.Background()
	l := newLifecycle(t)
	one := l.deploy(t, "libs", "a/one.txt", "one", types.Properties{"k": {"v"}})
	three := l.deploy(t, "libs", "b/three.txt", "three", nil)
	if _, _, err := Remove(ctx, l.db, "libs", "admin"); err != nil {
		t.Fatal(err)
	}
	n, err := l.bin().Restore(ctx, "libs", "", trash.Destination{Repo: l.repo(t, "restored")},
		"admin", nil)
	if err != nil || n != 4 {
		t.Fatalf("Restore = %d, %v", n, err)
	}
	for p, sha := range map[string]string{"a/one.txt": one, "b/three.txt": three} {
		it, err := l.db.Items().Get(ctx, "restored", false, p)
		if err != nil || it.Actual.SHA256 != sha || !l.stored(t, sha) {
			t.Fatalf("%s: %+v, %v", p, it, err)
		}
		if r := refs(t, l.db, sha); r != 1 {
			t.Fatalf("%s: %d references, want 1", p, r)
		}
	}
	it, err := l.db.Items().Get(ctx, "restored", false, "a/one.txt")
	if err != nil {
		t.Fatal(err)
	}
	if props, _ := l.db.Items().
		Properties(ctx, it.ID); strings.Join(props["k"], ",") != "v" ||
		strings.Join(props["trash.originalRepository"], ",") != "libs" {
		t.Fatalf("restored properties %v", props)
	}
	if n := entries(t, l.db, "libs"); n != 0 {
		t.Fatalf("%d entries left after the restore", n)
	}

	trashcan(t, l.db, false)
	if _, _, err := Remove(ctx, l.db, "restored", "admin"); err != nil {
		t.Fatal(err)
	}
	if n := entries(t, l.db, "restored"); n != 0 || refs(t, l.db, one) != 0 ||
		refs(t, l.db, three) != 0 {
		t.Fatalf("with the trash can off: %d entries, references %d and %d",
			n, refs(t, l.db, one), refs(t, l.db, three))
	}
	if rep := l.collect(t); rep.BlobsDeleted != 2 || l.stored(t, one) || l.stored(t, three) {
		t.Fatalf("collect: %+v", rep)
	}
}

// Emptying the trash can, or cleaning the deleted repository's root from
// it, releases the references, and garbage collection frees the content.
func TestUnitRemoveThenEmptyOrClean(t *testing.T) {
	ctx := context.Background()
	for name, purge := range map[string]func(*trash.Service) (int, error){
		"empty": func(s *trash.Service) (int, error) { return s.Empty(ctx) },
		"clean": func(s *trash.Service) (int, error) { return s.Clean(ctx, "libs", "") },
	} {
		l := newLifecycle(t)
		sha := l.deploy(t, "libs", "a/one.txt", "one", nil)
		if _, _, err := Remove(ctx, l.db, "libs", "admin"); err != nil {
			t.Fatal(err)
		}
		if n, err := purge(l.bin()); err != nil || n != 2 {
			t.Fatalf("%s: %d, %v", name, n, err)
		}
		if rep := l.collect(t); refs(t, l.db, sha) != 0 || rep.BlobsDeleted != 1 ||
			l.stored(t, sha) {
			t.Fatalf("%s: %d references, %+v", name, refs(t, l.db, sha), rep)
		}
	}
}

// A remote repository's cache never reaches the trash can, which holds what
// was deleted from local repositories: its references are released.
func TestUnitRemoveReleasesARemoteCache(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	c := repo.Config{Key: "up", Class: repo.Remote, URL: "https://up.example"}
	if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("cd", 32)
	if _, err := db.Blobs().
		Ref(ctx, types.Blob{Checksums: types.Checksums{SHA256: sha}, Size: 1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Items().Put(ctx, types.Item{
		RepoKey: "up", Cached: true, Name: "c.txt", Size: 1, Actual: types.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	if files, _, err := Remove(ctx, db, "up", "admin"); err != nil || files != 1 {
		t.Fatalf("Remove = %d, %v", files, err)
	}
	if n := entries(t, db, "up"); n != 0 || refs(t, db, sha) != 0 {
		t.Fatalf("%d entries, %d references", n, refs(t, db, sha))
	}
}

// While the trash can is off, the content is released at once.
func TestUnitRemoveWhileTheTrashCanIsOff(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	sha := strings.Repeat("ab", 32)
	trashcan(t, db, false)
	if _, _, err := Remove(ctx, db, "libs", "admin"); err != nil {
		t.Fatal(err)
	}
	if n := entries(t, db, "libs"); n != 0 || refs(t, db, sha) != 0 {
		t.Fatalf("%d entries, %d references", n, refs(t, db, sha))
	}
}

// A repository larger than a page of items is trashed whole, in the one
// transaction, each file's reference held once.
func TestUnitRemoveLargeRepository(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	c := repo.Config{Key: "big", Class: repo.Local}
	if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Items().
		Put(ctx, types.Item{RepoKey: "big", Name: "bulk", Folder: true}); err != nil {
		t.Fatal(err)
	}
	const n = 2*store.MaxLimit + 1
	shas := make([]string, n)
	for i := range n {
		shas[i] = fmt.Sprintf("%064x", i+1)
		if _, err := db.Blobs().Ref(
			ctx, types.Blob{Checksums: types.Checksums{SHA256: shas[i]}, Size: 1}, 1,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Items().Put(ctx, types.Item{
			RepoKey: "big", Path: "bulk", Name: fmt.Sprintf("f%05d", i), Size: 1,
			Actual: types.Checksums{SHA256: shas[i]},
		}); err != nil {
			t.Fatal(err)
		}
	}
	files, _, err := Remove(ctx, db, "big", "admin")
	if err != nil || files != n {
		t.Fatalf("Remove = %d, %v", files, err)
	}
	if got := entries(t, db, "big"); got != n+1 {
		t.Fatalf("%d entries, want %d", got, n+1)
	}
	for _, sha := range shas {
		if r := refs(t, db, sha); r != 1 {
			t.Fatalf("%s has %d references, want 1", sha, r)
		}
	}
	if purged, err := (&trash.Service{Store: db}).Empty(ctx); err != nil || purged != n+1 {
		t.Fatalf("Empty = %d, %v", purged, err)
	}
	for _, sha := range shas {
		if r := refs(t, db, sha); r != 0 {
			t.Fatalf("%s has %d references after emptying", sha, r)
		}
	}
}

// racing makes a concurrent writer's change just before the content delete,
// after the trash can listed the content: what a transaction at PostgreSQL's
// read committed can see.
type racing struct {
	*inmem.Store
	race func(ctx context.Context)
}

func (r racing) Items() store.ItemStore { return racingItems{r.Store.Items(), r.race} }

type racingItems struct {
	store.ItemStore
	race func(ctx context.Context)
}

func (r racingItems) Delete(
	ctx context.Context,
	repoKey string,
	cached bool,
	path string,
) (store.DeleteResult, error) {
	r.race(ctx)
	return r.ItemStore.Delete(ctx, repoKey, cached, path) //nolint:wrapcheck // test double
}

// A file added meanwhile is deleted without an entry and its reference
// released; a file removed meanwhile would leave an entry holding a
// reference no one removed, so the delete is refused and changes nothing.
func TestUnitRemoveWhileTheContentChanges(t *testing.T) {
	ctx := context.Background()
	held := strings.Repeat("ab", 32)

	db := world(t)
	late := strings.Repeat("ef", 32)
	added := racing{db, func(ctx context.Context) {
		if _, err := db.Blobs().
			Ref(ctx, types.Blob{Checksums: types.Checksums{SHA256: late}, Size: 1}, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Items().Put(ctx, types.Item{
			RepoKey: "libs", Name: "late.txt", Size: 1, Actual: types.Checksums{SHA256: late},
		}); err != nil {
			t.Fatal(err)
		}
	}}
	if files, _, err := Remove(ctx, added, "libs", "admin"); err != nil || files != 2 {
		t.Fatalf("Remove = %d, %v", files, err)
	}
	if n := entries(t, db, "libs"); n != 1 || refs(t, db, held) != 1 || refs(t, db, late) != 0 {
		t.Fatalf("%d entries, references %d and %d", n, refs(t, db, held), refs(t, db, late))
	}

	db = world(t)
	removed := racing{db, func(ctx context.Context) {
		if _, err := db.Items().Delete(ctx, "libs", false, "f.txt"); err != nil {
			t.Fatal(err)
		}
	}}
	if _, _, err := Remove(ctx, removed, "libs", "admin"); !errors.Is(err, ErrChanged) {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := db.Items().Get(ctx, "libs", false, "f.txt"); err != nil {
		t.Fatalf("the refused delete was not rolled back: %v", err)
	}
	if n := entries(t, db, "libs"); n != 0 || refs(t, db, held) != 1 {
		t.Fatalf("%d entries, %d references", n, refs(t, db, held))
	}
}
