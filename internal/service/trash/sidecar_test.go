package trash_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// keeper keeps a signature beside every file ending .bin, as Helm keeps a
// chart's .prov.
type keeper struct{ generic.Handler }

func (keeper) Sidecars(_ repo.Config, p string) []string {
	if strings.HasSuffix(p, ".bin") {
		return []string{p + ".sig"}
	}
	return nil
}

// keeping is a world whose repositories keep sidecars, holding a/x.bin and
// its signature.
func keeping(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.items.Formats = func(repo.PackageType) format.Handler { return keeper{} }
	w.deploy(t, w.libs, "a/x.bin", "x", nil)
	w.deploy(t, w.libs, "a/x.bin.sig", "x signed", nil)
	return w
}

// A file's restore brings back the sidecars its delete removed, beside the
// file wherever it lands; a folder's restore brings each back once.
func TestUnitRestoreTakesSidecars(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, from string
		dst        func(*world) trash.Destination
		expected   int
		at         []string
	}{
		{"to the original", "a/x.bin", func(w *world) trash.Destination {
			return trash.Destination{Repo: w.libs}
		}, 2, []string{"a/x.bin", "a/x.bin.sig"}},
		{"elsewhere", "a/x.bin", func(w *world) trash.Destination {
			return trash.Destination{Repo: w.other, Path: "b/y.bin"}
		}, 2, []string{"b/y.bin", "b/y.bin.sig"}},
		{"a folder", "a", func(w *world) trash.Destination {
			return trash.Destination{Repo: w.libs}
		}, 2, []string{"a/x.bin", "a/x.bin.sig"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := keeping(t)
			w.remove(t, w.libs, "a/x.bin")
			dst := c.dst(w)
			n, err := w.trash.Restore(ctx, "libs", c.from, dst, "bob", nil)
			if err != nil || n != c.expected {
				t.Fatalf("restored %d, %v", n, err)
			}
			for _, p := range c.at {
				if !w.exists(dst.Repo, p) {
					t.Errorf("%s lacks %s", dst.Repo.Key, p)
				}
			}
			if left, err := w.trash.Entries(
				ctx,
				"libs",
				"a/x.bin.sig",
			); err != nil ||
				len(left) != 0 {
				t.Errorf("the sidecar's entry stayed: %v %v", left, err)
			}
		})
	}
}

// A sidecar another delete removed stays in the trash can: one before the
// file's, and one at the same moment by someone else.
func TestUnitRestoreLeavesOtherDeletesSidecars(t *testing.T) {
	ctx := context.Background()
	for name, removeSidecar := range map[string]func(*world){
		"earlier": func(w *world) {
			w.remove(t, w.libs, "a/x.bin.sig")
			w.clock = w.clock.Add(time.Minute)
		},
		"by someone else": func(w *world) {
			if _, err := w.items.Delete(ctx,
				item.Target{Repo: w.libs, Path: "a/x.bin.sig"}, "carol"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := keeping(t)
			removeSidecar(w)
			w.remove(t, w.libs, "a/x.bin")
			n, err := w.trash.Restore(
				ctx,
				"libs",
				"a/x.bin",
				trash.Destination{Repo: w.libs},
				"bob",
				nil,
			)
			if err != nil || n != 1 || w.exists(w.libs, "a/x.bin.sig") {
				t.Fatalf("restored %d, %v", n, err)
			}
			if left, err := w.trash.Entries(
				ctx,
				"libs",
				"a/x.bin.sig",
			); err != nil ||
				len(left) != 1 {
				t.Errorf("the sidecar's entry: %v %v", left, err)
			}
		})
	}
}

// The permit is asked about the sidecars too: refusing one restores
// nothing.
func TestUnitRestoreSidecarPermit(t *testing.T) {
	ctx := context.Background()
	w := keeping(t)
	w.remove(t, w.libs, "a/x.bin")
	_, err := w.trash.Restore(ctx, "libs", "a/x.bin", trash.Destination{Repo: w.libs}, "bob",
		func(it types.Item) bool { return !strings.HasSuffix(it.FullPath(), ".sig") })
	if !errors.Is(err, trash.ErrNotPermitted) || w.exists(w.libs, "a/x.bin") {
		t.Fatalf("a refused sidecar: %v", err)
	}
}

// A failure finding a sidecar's entry fails the restore before it starts.
func TestUnitRestoreSidecarFindFails(t *testing.T) {
	ctx := context.Background()
	w := keeping(t)
	w.remove(t, w.libs, "a/x.bin")
	st := failing{w.db, map[string]error{"find": errBoom}}
	svc := &trash.Service{Store: st, Items: &item.Service{
		Store: st, Blobs: w.items.Blobs, Formats: w.items.Formats,
	}}
	if _, err := svc.Restore(
		ctx,
		"libs",
		"a/x.bin",
		trash.Destination{Repo: w.libs},
		"bob",
		nil,
	); !errors.Is(err, errBoom) ||
		w.exists(w.libs, "a/x.bin") {
		t.Fatalf("restore: %v", err)
	}
}
