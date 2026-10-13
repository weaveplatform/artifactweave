package item

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	// A file restored where a file is in the way replaces it.
	got, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "b/copy.txt"},
		types.TrashRecord{Item: f, Properties: types.Properties{"k": {"v"}}}, "bob")
	if err != nil || got.FullPath() != "b/copy.txt" || got.Actual.SHA256 != f.Actual.SHA256 {
		t.Fatalf("restore %+v, %v", got, err)
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
	if err != nil || n != 3 {
		t.Fatalf("zapped %d, %v; want the folder and two files", n, err)
	}
	got, err := w.db.Items().Get(ctx, "up", true, "pkg/a.json")
	if err != nil || !got.Updated.Equal(time.Unix(0, 0)) {
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
