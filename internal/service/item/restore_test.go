package item

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestRestoreRecord(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	f, err := w.deploy("a/f.txt", "content", types.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Restore(
		ctx,
		Target{Repo: w.loc},
		types.TrashRecord{Item: f},
		"bob",
	); !errors.Is(
		err,
		types.ErrInvalidItemPath,
	) {
		t.Fatalf("restore to the root: %v", err)
	}
	// The repository's own properties stay on the repository: a restore
	// brings back the item's alone (7.x fixed the reverse, RTFACT-29810).
	root, err := w.svc.EnsureRoot(ctx, Target{Repo: w.loc}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Annotate(ctx, w.loc, root, types.Properties{"k": {"repo"}, "r": {"1"}},
		nil); err != nil {
		t.Fatal(err)
	}
	// A file restored where a file is in the way replaces it.
	got, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "b/copy.txt"},
		types.TrashRecord{Item: f, Properties: types.Properties{"k": {"v"}}}, "bob")
	if err != nil || got.FullPath() != "b/copy.txt" || got.Actual.SHA256 != f.Actual.SHA256 {
		t.Fatalf("restore %+v, %v", got, err)
	}
	// The recorded property, and the two every restored item carries:
	// where it was deleted from and when it was restored (RTFACT-29810).
	restoredAt := strconv.FormatInt(at.UnixMilli(), 10)
	if props, err := w.db.Items().Properties(ctx, got.ID); err != nil ||
		len(props) != 3 || !props.Has("k", "v") ||
		!props.Has(PropTrashOriginalRepository, "libs") ||
		!props.Has(PropTrashRestoredTime, restoredAt) {
		t.Fatalf("restored properties %v, %v", props, err)
	}
	if _, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "a/f.txt/x"},
		types.TrashRecord{Item: f}, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a file in the way of a parent folder: %v", err)
	}
	folder := types.Item{Folder: true, Created: at}
	if _, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "a/f.txt"},
		types.TrashRecord{Item: folder}, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a folder where a file is: %v", err)
	}
	if got, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "new/dir"},
		types.TrashRecord{Item: folder}, "bob"); err != nil || !got.Folder {
		t.Fatalf("folder restore %+v, %v", got, err)
	}
}

func TestRestoreAnnotates(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	f, err := w.deploy("a/f.txt", "content", types.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	restoredAt := strconv.FormatInt(at.UnixMilli(), 10)
	// An item deleted from another repository names that one, and a
	// restore time recorded by an earlier restore gives way to this one's.
	f.RepoKey = "elsewhere"
	recorded := types.Properties{
		"k":                   {"v"},
		PropTrashRestoredTime: {"1"},
	}
	got, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "b/f.txt"},
		types.TrashRecord{Item: f, Properties: recorded}, "bob")
	if err != nil {
		t.Fatal(err)
	}
	want := types.Properties{
		"k":                         {"v"},
		PropTrashOriginalRepository: {"elsewhere"},
		PropTrashRestoredTime:       {restoredAt},
	}
	if props, err := w.db.Items().Properties(ctx, got.ID); err != nil ||
		!reflect.DeepEqual(props, want) {
		t.Fatalf("restored properties %v, %v", props, err)
	}
	// The trash entry's own record is left as it was.
	if len(recorded) != 2 || recorded[PropTrashRestoredTime][0] != "1" {
		t.Fatalf("record changed: %v", recorded)
	}
	// A folder already at the path gains them beside its own.
	dir, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "b"},
		types.TrashRecord{Item: types.Item{RepoKey: "libs", Folder: true, Created: at}}, "bob")
	if err != nil || !dir.Folder {
		t.Fatalf("folder restore %+v, %v", dir, err)
	}
	if props, err := w.db.Items().Properties(ctx, dir.ID); err != nil ||
		!props.Has(PropTrashOriginalRepository, "libs") ||
		!props.Has(PropTrashRestoredTime, restoredAt) {
		t.Fatalf("folder properties %v, %v", props, err)
	}
}

func TestZap(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	remote := *repo.Defaults(&repo.Config{Key: "up", Class: repo.Remote, URL: "https://up.example"})
	if err := w.db.Repos().Create(ctx, remote); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"pkg/a.json", "pkg/b.json"} {
		if _, err := w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: remote, Cached: true, Path: p}, Body: strings.NewReader(p),
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := w.svc.Zap(ctx, Target{Repo: remote, Cached: true, Path: "pkg"})
	if err != nil || n != 0 {
		t.Fatalf("zapped %d, %v; want no metadata retrieval records", n, err)
	}
	got, err := w.db.Items().Get(ctx, "up", true, "pkg/a.json")
	if err != nil || !got.Updated.Equal(at) {
		t.Fatalf("zapped item %+v, %v", got, err)
	}
	if _, err := w.svc.Zap(ctx, Target{Repo: w.loc, Path: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("zap outside a cache: %v", err)
	}
	if _, err := w.svc.Zap(
		ctx,
		Target{Repo: remote, Cached: true, Path: "absent"},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("zap of a missing path: %v", err)
	}
}
