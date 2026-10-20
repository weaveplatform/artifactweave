package build

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

// sidecarWorld is a promotion world whose formats keep sidecars, holding
// run 1 of app, whose artifact app/a.bin has its signature beside it.
func sidecarWorld(t *testing.T) *world {
	t.Helper()
	w := promotionWorld(t)
	w.svc.Formats = func(repo.PackageType) format.Handler { return keeper{} }
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.put("libs-local", false, "app/a.bin.sig", "signed", nil)
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, nil)))
	return w
}

// deny refuses want on the sidecar at path in repository key.
func deny(key, path string, want types.Actions) perms {
	return perms{item: func(c repo.Config, p string, w types.Actions) bool {
		return c.Key != key || p != path || !w.Has(want)
	}}
}

// A promotion takes an artifact's sidecars with it, so it needs on each
// the rights it needs on the artifact: read, and delete for a move, where
// it is, and deploy, and delete to replace a file, where it lands
// (decision 0039). Without them the artifact stays, reported.
func TestPromoteSidecars(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		a      perms
		copy   bool
		before func(w *world)
		want   string
	}{
		{
			"no read", deny("libs-local", "app/a.bin.sig", types.ActionRead), true, nil,
			"ERROR Cannot copy libs-local:app/a.bin: no permission to copy its sidecar app/a.bin.sig",
		},
		{
			"no delete for a move", deny("libs-local", "app/a.bin.sig", types.ActionDelete), false, nil,
			"ERROR Cannot move libs-local:app/a.bin: no permission to move its sidecar app/a.bin.sig",
		},
		{
			"no deploy", deny("release-local", "app/a.bin.sig", types.ActionWrite), false, nil,
			"ERROR Cannot move libs-local:app/a.bin: no permission to deploy its sidecar to " +
				"release-local:app/a.bin.sig",
		},
		{
			"no delete to replace", deny("release-local", "app/a.bin.sig", types.ActionDelete), false,
			func(w *world) { w.put("release-local", false, "app/a.bin.sig", "other", nil) },
			"User doesn't have permissions to override 'release-local:app/a.bin.sig'",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := sidecarWorld(t)
			if c.before != nil {
				c.before(w)
			}
			res, err := w.svc.Promote(ctx, Promotion{
				Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true,
				Copy: c.copy, By: "deployer",
			}, c.a)
			if err != nil {
				t.Fatal(err)
			}
			if got := texts(res.Messages); !strings.Contains(got, c.want) {
				t.Fatalf("report lacks %q:\n%s", c.want, got)
			}
			if !w.has("libs-local", "app/a.bin") || !w.has("libs-local", "app/a.bin.sig") ||
				w.has("release-local", "app/a.bin") {
				t.Error("a refused promotion moved something")
			}
		})
	}
	w := sidecarWorld(t)
	w.put("release-local", false, "app/a.bin.sig", "other", nil)
	if _, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true, By: "deployer",
	}, everyone); err != nil {
		t.Fatal(err)
	}
	if w.has("libs-local", "app/a.bin.sig") || !w.has("release-local", "app/a.bin.sig") {
		t.Error("the sidecar did not go with its artifact")
	}
}

// A run deleted with its artifacts takes their sidecars, so it needs
// delete on each.
func TestDeleteArtifactsSidecars(t *testing.T) {
	ctx := context.Background()
	w := sidecarWorld(t)
	del := Delete{Name: "app", Numbers: []string{"1"}, Artifacts: true}
	if _, err := w.svc.Delete(
		ctx,
		del,
		deny("libs-local", "app/a.bin.sig", types.ActionDelete),
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("without delete on the sidecar: %v", err)
	}
	if !w.has("libs-local", "app/a.bin") || !w.has("libs-local", "app/a.bin.sig") {
		t.Fatal("a refused delete changed something")
	}
	if _, err := w.svc.Delete(ctx, del, everyone); err != nil {
		t.Fatal(err)
	}
	if w.has("libs-local", "app/a.bin") || w.has("libs-local", "app/a.bin.sig") {
		t.Fatal("the sidecar did not go with its artifact")
	}
}

// failingGets fails reading the item at path in repository key.
type failingGets struct {
	store.Store
	key, path string
}

func (f failingGets) Items() store.ItemStore { return failingItems{f.Store.Items(), f.key, f.path} }

type failingItems struct {
	store.ItemStore
	key, path string
}

//nolint:wrapcheck // test double
func (f failingItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if key == f.key && p == f.path {
		return types.Item{}, errBoom
	}
	return f.ItemStore.Get(ctx, key, cached, p)
}

// A store that fails while the sidecars are looked up fails the promotion
// and the delete: the source's sidecar, and the file a sidecar would
// replace where the caller may not delete.
func TestSidecarFaults(t *testing.T) {
	ctx := context.Background()
	promote := func(w *world) error {
		_, err := w.svc.Promote(ctx, Promotion{
			Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true, By: "x",
		}, deny("release-local", "app/a.bin.sig", types.ActionDelete))
		return err
	}
	w := sidecarWorld(t)
	w.svc.Store = failingGets{w.db, "libs-local", "app/a.bin.sig"}
	if err := promote(w); !errors.Is(err, errBoom) {
		t.Errorf("promote, the source's sidecar failing: %v", err)
	}
	if _, err := w.svc.Delete(ctx, Delete{Name: "app", Numbers: []string{"1"}, Artifacts: true},
		everyone); !errors.Is(err, errBoom) {
		t.Errorf("delete, the sidecar failing: %v", err)
	}
	w.svc.Store = failingGets{w.db, "release-local", "app/a.bin.sig"}
	if err := promote(w); !errors.Is(err, errBoom) {
		t.Errorf("promote, the target's sidecar failing: %v", err)
	}
}
