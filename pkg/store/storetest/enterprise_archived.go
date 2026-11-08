package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runArchivedItems covers archived items (decision 0024).
func runArchivedItems(t *testing.T, newStore Factory) {
	t.Run("ArchivedItems", func(t *testing.T) { testArchivedItems(t, newStore(t)) })
}

// fxArchived builds the archived copy of repoKey/full.
func fxArchived(repoKey, full string, i int) types.ArchivedItem {
	parent, name, depth, err := types.SplitItemPath(full)
	if err != nil {
		panic(err)
	}
	return types.ArchivedItem{
		Namespace: "ns-1",
		Item: types.Item{
			ID:      int64(100 + i),
			RepoKey: repoKey,
			Path:    parent,
			Name:    name,
			Depth:   depth,
			Size: int64(
				10 * i,
			),
			Actual:     blob(i).Checksums,
			Original:   types.Checksums{SHA1: blob(i).Checksums.SHA1},
			MimeType:   "application/java-archive",
			Created:    storekit.At(i),
			CreatedBy:  "ci",
			Modified:   storekit.At(i + 1),
			ModifiedBy: "ci",
			Updated:    storekit.At(i + 2),
		},
		Properties: types.Properties{"build.number": {fmt.Sprint(i)}, "env": {"dev", "prod"}},
		Stats: types.ItemStats{
			DownloadCount:    int64(i),
			LastDownloaded:   storekit.At(i + 3),
			LastDownloadedBy: "u",
		},
		PackageType:    "maven",
		PackageName:    "com.acme:app",
		PackageVersion: fmt.Sprintf("1.%d", i),
		PolicyKey:      []string{"p-a", "p-b"}[i%2],
		ExecutionID:    int64(i%2 + 1),
		ArchivedAt:     storekit.At(i + 10),
		ColdBlobSHA256: blob(i).Checksums.SHA256,
	}
}

func testArchivedItems(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	var added []types.ArchivedItem
	add := func(a types.ArchivedItem) types.ArchivedItem {
		t.Helper()
		got := storekit.Get[types.ArchivedItem](t)(as.AddItem(ctx, a))
		if got.ID <= 0 {
			t.Fatalf("archived item ID = %d", got.ID)
		}
		a.ID = got.ID
		storekit.Same(t, "AddItem", a, got)
		added = append(added, a)
		return a
	}
	first := add(fxArchived("libs", "com/acme/app.jar", 1))
	storekit.Same(t, "Item", first, storekit.Get[types.ArchivedItem](t)(as.Item(ctx, first.ID)))
	_, err := as.Item(ctx, first.ID+100)
	storekit.WantErr(t, err, store.ErrNotFound)

	// A second archived copy of the same path lists first.
	second := fxArchived("libs", "com/acme/app.jar", 2)
	second.ExpiresAt = storekit.At(30)
	second = add(second)
	add(fxArchived("libs", "com/acme/other.jar", 3))
	root := fxArchived("other", "readme.txt", 4)
	root.ExpiresAt = storekit.At(25)
	root = add(root)
	late := fxArchived("libs", "z.jar", 5)
	late.ExpiresAt = storekit.At(40)
	late = add(late)

	storekit.Same(t, "FindItems", []types.ArchivedItem{second, first},
		storekit.Get[[]types.ArchivedItem](t)(as.FindItems(ctx, "libs", "/com/acme/app.jar")))
	storekit.Same(t, "FindItems at the root", []types.ArchivedItem{root},
		storekit.Get[[]types.ArchivedItem](t)(as.FindItems(ctx, "other", "readme.txt")))
	storekit.Same(t, "FindItems none", []types.ArchivedItem(nil),
		storekit.Get[[]types.ArchivedItem](t)(as.FindItems(ctx, "other", "com/acme/app.jar")))
	_, err = as.FindItems(ctx, "libs", "a/../b")
	storekit.WantErr(t, err, store.ErrInvalid)
	bad := fxArchived("libs", "x.jar", 6)
	bad.Item.Path, bad.Item.Name = ".", ""
	_, err = as.AddItem(ctx, bad)
	storekit.WantErr(t, err, store.ErrInvalid)

	ids := func(f store.ArchivedFilter) []int64 {
		var out []int64
		for _, v := range storekit.Pages(t, 2, func(p store.Page) ([]types.ArchivedItem, string, error) {
			return as.Items(ctx, f, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	id := func(i int) int64 { return added[i-1].ID }
	storekit.Same(
		t,
		"Items",
		[]int64{id(1), id(2), id(3), id(4), id(5)},
		ids(store.ArchivedFilter{}),
	)
	storekit.Same(
		t,
		"Items(libs)",
		[]int64{id(1), id(2), id(3), id(5)},
		ids(store.ArchivedFilter{RepoKey: "libs"}),
	)
	storekit.Same(
		t,
		"Items(policy p-b)",
		[]int64{id(1), id(3), id(5)},
		ids(store.ArchivedFilter{PolicyKey: "p-b"}),
	)
	storekit.Same(
		t,
		"Items(execution 1)",
		[]int64{id(2), id(4)},
		ids(store.ArchivedFilter{ExecutionID: 1}),
	)
	_, _, err = as.Items(
		ctx,
		store.ArchivedFilter{},
		store.Page{After: store.Cursor("not-a-number")},
	)
	storekit.WantErr(t, err, store.ErrInvalidCursor)
	_, _, err = as.Items(ctx, store.ArchivedFilter{}, store.Page{After: "%%%"})
	storekit.WantErr(t, err, store.ErrInvalidCursor)

	storekit.Same(t, "ExpiredItems", []types.ArchivedItem{root, second},
		storekit.Get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, storekit.At(35), 0)))
	storekit.Same(t, "ExpiredItems limited", []types.ArchivedItem{root},
		storekit.Get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, storekit.At(35), 1)))
	storekit.Same(t, "ExpiredItems at the boundary", []types.ArchivedItem{root},
		storekit.Get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, storekit.At(30), 0)))

	// Returned values are copies.
	got := storekit.Get[types.ArchivedItem](t)(as.Item(ctx, first.ID))
	got.Properties["env"][0] = "changed"
	storekit.Same(
		t,
		"Item after mutating a copy",
		first,
		storekit.Get[types.ArchivedItem](t)(as.Item(ctx, first.ID)),
	)

	storekit.Must(t, as.DeleteItem(ctx, first.ID))
	_, err = as.Item(ctx, first.ID)
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.WantErr(t, as.DeleteItem(ctx, first.ID), store.ErrNotFound)
}
