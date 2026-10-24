package pkgindex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitLinkAndUnlink(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	at := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "npm", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range []string{"a.tgz", "a.json", "b.tgz"} {
		it, err := db.Items().Put(ctx, types.Item{RepoKey: "npm", Name: n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
	}
	v1 := Version{RepoKey: "npm", PackageType: repo.NPM, Package: "left-pad", Version: "1.0.0"}
	v2 := v1
	v2.Version = "2.0.0"
	for _, l := range []struct {
		v  Version
		id int64
	}{{v1, ids[0]}, {v1, ids[1]}, {v2, ids[2]}} {
		if err := Link(ctx, db, l.v, l.id, at); err != nil {
			t.Fatal(err)
		}
	}
	p, err := db.Packages().Find(ctx, "npm", "left-pad")
	if err != nil || p.Type != repo.NPM {
		t.Fatalf("package %+v %v", p, err)
	}
	versions := func() int {
		vs, err := db.Packages().Versions(ctx, p.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		return len(vs)
	}
	if versions() != 2 {
		t.Fatalf("versions %d", versions())
	}
	// Deleting an item drops its links; Prune then removes the versions left
	// without files, and the package with its last version.
	prune := func(drop ...int64) {
		t.Helper()
		var vs []types.PackageVersion
		for _, id := range drop {
			pv, ok, err := VersionOf(ctx, db, id)
			if err != nil || !ok {
				t.Fatalf("version of %d: %v %v", id, ok, err)
			}
			vs = append(vs, pv)
		}
		for _, id := range drop {
			it, err := db.Items().GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Items().Delete(ctx, "npm", false, it.FullPath()); err != nil {
				t.Fatal(err)
			}
		}
		if err := Prune(ctx, db, vs); err != nil {
			t.Fatal(err)
		}
	}
	prune(ids[0])
	if versions() != 2 {
		t.Fatalf("a version with a file left was pruned: %d versions", versions())
	}
	prune(ids[1])
	if versions() != 1 {
		t.Fatalf("an empty version survived: %d versions", versions())
	}
	prune(ids[2])
	if _, err := db.Packages().Find(ctx, "npm", "left-pad"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("package survived its last version: %v", err)
	}
	if _, ok, err := VersionOf(ctx, db, 999); ok || err != nil {
		t.Fatalf("an unindexed item: %v %v", ok, err)
	}
	if err := Link(ctx, db, v1, 999, at); err == nil {
		t.Fatal("linking a missing item should fail")
	}
}

// failing is a store whose package index fails the operation named fail.
type failing struct {
	*inmem.Store
	fail string
}

func (f failing) Packages() store.PackageStore { return failingPackages{f.Store.Packages(), f.fail} }

type failingPackages struct {
	store.PackageStore
	fail string
}

var errBoom = errors.New("boom")

func (f failingPackages) err(op string) error {
	if f.fail == op {
		return errBoom
	}
	return nil
}

func (f failingPackages) Ensure(ctx context.Context, p types.Package) (types.Package, error) {
	if err := f.err("ensure"); err != nil {
		return p, err
	}
	return f.PackageStore.Ensure(ctx, p)
}

func (f failingPackages) Version(
	ctx context.Context,
	id int64,
	v string,
) (types.PackageVersion, error) {
	if err := f.err("version"); err != nil {
		return types.PackageVersion{}, err
	}
	return f.PackageStore.Version(ctx, id, v)
}

func (f failingPackages) VersionOf(ctx context.Context, id int64) (types.PackageVersion, error) {
	if err := f.err("versionOf"); err != nil {
		return types.PackageVersion{}, err
	}
	return f.PackageStore.VersionOf(ctx, id)
}

func (f failingPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if err := f.err("files"); err != nil {
		return nil, err
	}
	return f.PackageStore.Files(ctx, id)
}

func (f failingPackages) DeleteVersion(ctx context.Context, id int64) error {
	if err := f.err("deleteVersion"); err != nil {
		return err
	}
	return f.PackageStore.DeleteVersion(ctx, id)
}

func (f failingPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if err := f.err("versions"); err != nil {
		return nil, err
	}
	return f.PackageStore.Versions(ctx, id)
}

func (f failingPackages) Delete(ctx context.Context, id int64) error {
	if err := f.err("delete"); err != nil {
		return err
	}
	return f.PackageStore.Delete(ctx, id)
}

func TestUnitStoreFailures(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	v := Version{RepoKey: "r", PackageType: repo.Generic, Package: "p", Version: "1"}
	for _, op := range []string{"ensure", "version"} {
		db := inmem.New()
		it, _ := db.Items().Put(ctx, types.Item{RepoKey: "r", Name: "f"})
		if err := Link(ctx, failing{db, op}, v, it.ID, at); !errors.Is(err, errBoom) {
			t.Errorf("link with %s failing: %v", op, err)
		}
	}
	// An orphan version (its file gone) and a package with a second version
	// reach every step of Prune.
	for _, op := range []string{"versionOf", "files", "deleteVersion", "versions", "delete"} {
		db := inmem.New()
		it, _ := db.Items().Put(ctx, types.Item{RepoKey: "r", Name: "f"})
		if err := Link(ctx, db, v, it.ID, at); err != nil {
			t.Fatal(err)
		}
		pv, _, _ := VersionOf(ctx, db, it.ID)
		if op == "versionOf" {
			if _, _, err := VersionOf(ctx, failing{db, op}, it.ID); !errors.Is(err, errBoom) {
				t.Errorf("versionOf failing: %v", err)
			}
			continue
		}
		if _, err := db.Items().Delete(ctx, "r", false, "f"); err != nil {
			t.Fatal(err)
		}
		if err := Prune(
			ctx,
			failing{db, op},
			[]types.PackageVersion{pv, pv},
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("prune with %s failing: %v", op, err)
		}
	}
}
