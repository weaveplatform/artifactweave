package artifacts

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
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
	// List walks the repository in byte order of path, a page at a time,
	// with properties when asked; Discard removes a folder with what is
	// below it, and nothing at a path is not an error.
	for _, c := range []artifactsapi.ItemChange{
		{Repository: "fed", Path: "a/b", Folder: true, Properties: map[string][]string{"k": {"v"}}},
		{Repository: "fed", Path: "a-c", Folder: true},
		{Repository: "fed", Path: "a/b/c", Folder: true},
	} {
		if err := fi.Put(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	paths := func(cs []artifactsapi.ItemChange) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Path)
		}
		return out
	}
	first, err := fi.List(ctx, "fed", "", 2, true)
	if err != nil || !slices.Equal(paths(first), []string{"a", "a-c"}) {
		t.Fatalf("first page %v, %v", paths(first), err)
	}
	rest, err := fi.List(ctx, "fed", "a-c", 10, true)
	if err != nil || !slices.Equal(paths(rest), []string{"a/b", "a/b/c"}) || !slices.Equal(rest[0].Properties["k"], []string{"v"}) {
		t.Fatalf("next page %+v, %v", rest, err)
	}
	if bare, err := fi.List(ctx, "fed", "a-c", 1, false); err != nil || len(bare) != 1 || bare[0].Properties != nil {
		t.Fatalf("without properties %+v, %v", bare, err)
	}
	if err := fi.Discard(ctx, "fed", "a"); err != nil {
		t.Fatal(err)
	}
	if err := fi.Discard(ctx, "fed", "a/b"); err != nil {
		t.Fatal(err)
	}
	if left, err := fi.List(ctx, "fed", "", 10, false); err != nil || !slices.Equal(paths(left), []string{"a-c"}) {
		t.Fatalf("after discard %v, %v", paths(left), err)
	}
	if err := fi.Discard(ctx, "fed", "/"); !errors.Is(err, kernel.ErrInvalidItemPath) {
		t.Fatalf("discard of the root: %v", err)
	}
	for _, key := range []string{"loc", "missing"} {
		if err := fi.Discard(ctx, key, "a"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("discard from %s: %v", key, err)
		}
		if _, err := fi.List(ctx, key, "", 1, false); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("list of %s: %v", key, err)
		}
		folder.Repository = key
		if err := fi.Put(ctx, folder); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("put into %s: %v", key, err)
		}
		if err := fi.Delete(ctx, key, "a", true); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("delete from %s: %v", key, err)
		}
		if _, err := fi.Holds(ctx, key, "x"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("holds in %s: %v", key, err)
		}
	}

	// Holds finds the repository's own file of a content, past the other
	// repositories' files of it.
	sum := strings.Repeat("ab", 32)
	holds := func() bool {
		t.Helper()
		held, err := fi.Holds(ctx, "fed", sum)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	for i := range 101 {
		if _, err := m.store.Items().Put(ctx, model.Item{
			RepoKey: "loc", Name: fmt.Sprintf("x%d.bin", i), Size: 1, Actual: kernel.Checksums{SHA256: sum},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if holds() {
		t.Fatal("fed holds content only loc has")
	}
	if _, err := m.store.Items().Put(ctx, model.Item{
		RepoKey: "fed", Name: "x.bin", Size: 1, Actual: kernel.Checksums{SHA256: sum},
	}); err != nil {
		t.Fatal(err)
	}
	if !holds() {
		t.Fatal("fed's own file of the content was not found")
	}
	if held, err := fi.Holds(ctx, "fed", ""); err != nil || held {
		t.Fatalf("no content: %v, %v", held, err)
	}
}
