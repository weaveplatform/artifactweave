package storetest

import (
	"context"
	"fmt"
	"testing"

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
			Created:    at(i),
			CreatedBy:  "ci",
			Modified:   at(i + 1),
			ModifiedBy: "ci",
			Updated:    at(i + 2),
		},
		Properties: types.Properties{"build.number": {fmt.Sprint(i)}, "env": {"dev", "prod"}},
		Stats: types.ItemStats{
			DownloadCount:    int64(i),
			LastDownloaded:   at(i + 3),
			LastDownloadedBy: "u",
		},
		PackageType:    "maven",
		PackageName:    "com.acme:app",
		PackageVersion: fmt.Sprintf("1.%d", i),
		PolicyKey:      []string{"p-a", "p-b"}[i%2],
		ExecutionID:    int64(i%2 + 1),
		ArchivedAt:     at(i + 10),
		ColdBlobSHA256: blob(i).Checksums.SHA256,
	}
}

func testArchivedItems(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	var added []types.ArchivedItem
	add := func(a types.ArchivedItem) types.ArchivedItem {
		t.Helper()
		got := get[types.ArchivedItem](t)(as.AddItem(ctx, a))
		if got.ID <= 0 {
			t.Fatalf("archived item ID = %d", got.ID)
		}
		a.ID = got.ID
		same(t, "AddItem", a, got)
		added = append(added, a)
		return a
	}
	first := add(fxArchived("libs", "com/acme/app.jar", 1))
	same(t, "Item", first, get[types.ArchivedItem](t)(as.Item(ctx, first.ID)))
	_, err := as.Item(ctx, first.ID+100)
	wantErr(t, err, store.ErrNotFound)

	// A second archived copy of the same path lists first.
	second := fxArchived("libs", "com/acme/app.jar", 2)
	second.ExpiresAt = at(30)
	second = add(second)
	add(fxArchived("libs", "com/acme/other.jar", 3))
	root := fxArchived("other", "readme.txt", 4)
	root.ExpiresAt = at(25)
	root = add(root)
	late := fxArchived("libs", "z.jar", 5)
	late.ExpiresAt = at(40)
	late = add(late)

	same(t, "FindItems", []types.ArchivedItem{second, first},
		get[[]types.ArchivedItem](t)(as.FindItems(ctx, "libs", "/com/acme/app.jar")))
	same(t, "FindItems at the root", []types.ArchivedItem{root},
		get[[]types.ArchivedItem](t)(as.FindItems(ctx, "other", "readme.txt")))
	same(t, "FindItems none", []types.ArchivedItem(nil),
		get[[]types.ArchivedItem](t)(as.FindItems(ctx, "other", "com/acme/app.jar")))
	_, err = as.FindItems(ctx, "libs", "a/../b")
	wantErr(t, err, store.ErrInvalid)
	bad := fxArchived("libs", "x.jar", 6)
	bad.Item.Path, bad.Item.Name = ".", ""
	_, err = as.AddItem(ctx, bad)
	wantErr(t, err, store.ErrInvalid)

	ids := func(f store.ArchivedFilter) []int64 {
		var out []int64
		for _, v := range pages(t, 2, func(p store.Page) ([]types.ArchivedItem, string, error) {
			return as.Items(ctx, f, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	id := func(i int) int64 { return added[i-1].ID }
	same(t, "Items", []int64{id(1), id(2), id(3), id(4), id(5)}, ids(store.ArchivedFilter{}))
	same(
		t,
		"Items(libs)",
		[]int64{id(1), id(2), id(3), id(5)},
		ids(store.ArchivedFilter{RepoKey: "libs"}),
	)
	same(
		t,
		"Items(policy p-b)",
		[]int64{id(1), id(3), id(5)},
		ids(store.ArchivedFilter{PolicyKey: "p-b"}),
	)
	same(t, "Items(execution 1)", []int64{id(2), id(4)}, ids(store.ArchivedFilter{ExecutionID: 1}))
	_, _, err = as.Items(
		ctx,
		store.ArchivedFilter{},
		store.Page{After: store.Cursor("not-a-number")},
	)
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = as.Items(ctx, store.ArchivedFilter{}, store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	same(t, "ExpiredItems", []types.ArchivedItem{root, second},
		get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, at(35), 0)))
	same(t, "ExpiredItems limited", []types.ArchivedItem{root},
		get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, at(35), 1)))
	same(t, "ExpiredItems at the boundary", []types.ArchivedItem{root},
		get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, at(30), 0)))

	// Returned values are copies.
	got := get[types.ArchivedItem](t)(as.Item(ctx, first.ID))
	got.Properties["env"][0] = "changed"
	same(t, "Item after mutating a copy", first, get[types.ArchivedItem](t)(as.Item(ctx, first.ID)))

	must(t, as.DeleteItem(ctx, first.ID))
	_, err = as.Item(ctx, first.ID)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, as.DeleteItem(ctx, first.ID), store.ErrNotFound)
}
