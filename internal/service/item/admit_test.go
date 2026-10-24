package item

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// guard is a format that refuses every folder create and every change to
// the property "guarded".
type guard struct {
	generic.Handler
	folders []string
}

func (g *guard) AdmitFolder(_ context.Context, _ format.Env, _ repo.Config, p string) error {
	g.folders = append(g.folders, p)
	return format.Reject(http.StatusBadRequest, "no folders")
}

func (*guard) AdmitAnnotation(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	_ types.Item,
	a format.Annotation,
) error {
	if _, ok := a.Set["guarded"]; ok {
		return format.Reject(http.StatusBadRequest, "guarded")
	}
	return nil
}

func TestUnitFormatAdmitsFoldersAndPropertyChanges(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	g := &guard{}
	it, err := w.deploy("a/b.txt", "b", types.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := w.db.Items().Get(ctx, "libs", false, "a")
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Formats = func(repo.PackageType) format.Handler { return g }

	var r *format.Rejection
	if _, err := w.svc.MakeFolder(
		ctx,
		Target{Repo: w.loc, Path: "/c/d/"},
		nil,
		"x",
	); !errors.As(err, &r) ||
		len(g.folders) != 1 ||
		g.folders[0] != "c/d" {
		t.Fatalf("a refused folder = %v, asked about %v", err, g.folders)
	}
	if _, err := w.db.Items().Get(ctx, "libs", false, "c"); err == nil {
		t.Fatal("a refused folder create made its parent")
	}
	if err := w.svc.Annotate(
		ctx,
		w.loc,
		it,
		types.Properties{"guarded": {"1"}},
		nil,
	); !errors.As(
		err,
		&r,
	) {
		t.Fatalf("a refused property change = %v", err)
	}
	if err := w.svc.Annotate(ctx, w.loc, it, types.Properties{"free": {"1"}}, nil); err != nil {
		t.Fatal(err)
	}

	// A restore into a folder made since brings its properties back
	// without asking: they were set already.
	w.svc.Formats = nil
	if err := w.db.Items().PatchProperties(ctx, folder.ID, types.Properties{"guarded": {"1"}},
		nil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "a"}, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "a"}, nil, "x"); err != nil {
		t.Fatal(err)
	}
	entries, err := w.db.Trash().Find(ctx, "libs", "a")
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Formats = func(repo.PackageType) format.Handler { return g }
	for _, e := range entries {
		if !e.Record.Item.Folder {
			continue
		}
		if _, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "a"}, e.Record, "x"); err != nil {
			t.Fatalf("a restore into a folder made since = %v", err)
		}
	}
	now, err := w.db.Items().Get(ctx, "libs", false, "a")
	if err != nil {
		t.Fatal(err)
	}
	props, err := w.db.Items().Properties(ctx, now.ID)
	if err != nil || len(props["guarded"]) != 1 {
		t.Fatalf("the restored folder's properties = %v, %v", props, err)
	}
}
