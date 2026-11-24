package artifacts

import (
	"errors"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitFederatedItems applies a member's change to a federated
// repository only: a missing or local repository is not found.
func TestUnitFederatedItems(t *testing.T) {
	m := newModule(t)
	ctx := t.Context()
	for _, c := range []repo.Config{
		{Key: "fed", Class: repo.Federated, PackageType: "generic"},
		{Key: "loc", Class: repo.Local, PackageType: "generic"},
	} {
		if err := m.store.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fi := m.API().FederatedItems
	folder := artifactsapi.ItemChange{Repository: "fed", Path: "a/b", Folder: true}
	if err := fi.Put(ctx, folder); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Items().Get(ctx, "fed", false, "a/b"); err != nil {
		t.Fatalf("applied folder: %v", err)
	}
	if err := fi.Delete(ctx, "fed", "a/b", true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Items().Get(ctx, "fed", false, "a/b"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("deleted folder: %v", err)
	}
	for _, key := range []string{"loc", "missing"} {
		folder.Repository = key
		if err := fi.Put(ctx, folder); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("put into %s: %v", key, err)
		}
		if err := fi.Delete(ctx, key, "a", true); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("delete from %s: %v", key, err)
		}
	}
}
