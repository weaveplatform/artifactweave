package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// runArchivedItems covers archived items (decision 0024).
func runArchivedItems(t *testing.T, newStore Factory) {
	t.Run("ArchivedItems", func(t *testing.T) { testArchivedItems(t, newStore(t)) })
}

// fxArchived builds the archived copy of repoKey/full.
func fxArchived(repoKey, full string, i int) model.ArchivedItem {
	parent, name, depth, err := kernel.SplitItemPath(full)
	if err != nil {
		panic(err)
	}
	return model.ArchivedItem{
		Namespace: "ns-1",
		Item: model.Item{
			ID:      int64(100 + i),
			RepoKey: repoKey,
			Path:    parent,
			Name:    name,
			Depth:   depth,
			Size: int64(
				10 * i,
			),
			Actual:     testBlob(i).Checksums,
			Original:   kernel.Checksums{SHA1: testBlob(i).Checksums.SHA1},
			MimeType:   "application/java-archive",
			Created:    storekit.At(i),
			CreatedBy:  "ci",
			Modified:   storekit.At(i + 1),
			ModifiedBy: "ci",
			Updated:    storekit.At(i + 2),
		},
		Properties: model.Properties{"build.number": {fmt.Sprint(i)}, "env": {"dev", "prod"}},
		Stats: model.ItemStats{
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
		ColdBlobSHA256: testBlob(i).Checksums.SHA256,
	}
}

func testArchivedItems(t *testing.T, s port.Store) {
	ctx := context.Background()
	as := s.ArchivedItems()
	var added []model.ArchivedItem
	add := func(a model.ArchivedItem) model.ArchivedItem {
		t.Helper()
		got := storekit.Get[model.ArchivedItem](t)(as.AddItem(ctx, a))
		if got.ID <= 0 {
			t.Fatalf("archived item ID = %d", got.ID)
		}
		a.ID = got.ID
		storekit.Same(t, "AddItem", a, got)
		added = append(added, a)
		return a
	}
	first := add(fxArchived("libs", "com/acme/app.jar", 1))
	storekit.Same(t, "Item", first, storekit.Get[model.ArchivedItem](t)(as.Item(ctx, first.ID)))
	_, err := as.Item(ctx, first.ID+100)
	storekit.WantErr(t, err, kernel.ErrNotFound)

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

	storekit.Same(t, "FindItems", []model.ArchivedItem{second, first},
		storekit.Get[[]model.ArchivedItem](t)(as.FindItems(ctx, "libs", "/com/acme/app.jar")))
	storekit.Same(t, "FindItems at the root", []model.ArchivedItem{root},
		storekit.Get[[]model.ArchivedItem](t)(as.FindItems(ctx, "other", "readme.txt")))
	storekit.Same(t, "FindItems none", []model.ArchivedItem(nil),
		storekit.Get[[]model.ArchivedItem](t)(as.FindItems(ctx, "other", "com/acme/app.jar")))
	_, err = as.FindItems(ctx, "libs", "a/../b")
	storekit.WantErr(t, err, kernel.ErrInvalid)
	bad := fxArchived("libs", "x.jar", 6)
	bad.Item.Path, bad.Item.Name = ".", ""
	_, err = as.AddItem(ctx, bad)
	storekit.WantErr(t, err, kernel.ErrInvalid)

	ids := func(f port.ArchivedFilter) []int64 {
		page := storekit.Pages(t, 2, func(p kernel.Page) ([]model.ArchivedItem, string, error) {
			return as.Items(ctx, f, p)
		})
		out := make([]int64, 0, len(page))
		for _, v := range page {
			out = append(out, v.ID)
		}
		return out
	}
	id := func(i int) int64 { return added[i-1].ID }
	storekit.Same(
		t,
		"Items",
		[]int64{id(1), id(2), id(3), id(4), id(5)},
		ids(port.ArchivedFilter{}),
	)
	storekit.Same(
		t,
		"Items(libs)",
		[]int64{id(1), id(2), id(3), id(5)},
		ids(port.ArchivedFilter{RepoKey: "libs"}),
	)
	storekit.Same(
		t,
		"Items(policy p-b)",
		[]int64{id(1), id(3), id(5)},
		ids(port.ArchivedFilter{PolicyKey: "p-b"}),
	)
	storekit.Same(
		t,
		"Items(execution 1)",
		[]int64{id(2), id(4)},
		ids(port.ArchivedFilter{ExecutionID: 1}),
	)
	_, _, err = as.Items(
		ctx,
		port.ArchivedFilter{},
		kernel.Page{After: kernel.Cursor("not-a-number")},
	)
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	_, _, err = as.Items(ctx, port.ArchivedFilter{}, kernel.Page{After: "%%%"})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)

	storekit.Same(t, "ExpiredItems", []model.ArchivedItem{root, second},
		storekit.Get[[]model.ArchivedItem](t)(as.ExpiredItems(ctx, storekit.At(35), 0)))
	storekit.Same(t, "ExpiredItems limited", []model.ArchivedItem{root},
		storekit.Get[[]model.ArchivedItem](t)(as.ExpiredItems(ctx, storekit.At(35), 1)))
	storekit.Same(t, "ExpiredItems at the boundary", []model.ArchivedItem{root},
		storekit.Get[[]model.ArchivedItem](t)(as.ExpiredItems(ctx, storekit.At(30), 0)))

	// Returned values are copies.
	got := storekit.Get[model.ArchivedItem](t)(as.Item(ctx, first.ID))
	got.Properties["env"][0] = "changed"
	storekit.Same(
		t,
		"Item after mutating a copy",
		first,
		storekit.Get[model.ArchivedItem](t)(as.Item(ctx, first.ID)),
	)

	storekit.Must(t, as.DeleteItem(ctx, first.ID))
	_, err = as.Item(ctx, first.ID)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, as.DeleteItem(ctx, first.ID), kernel.ErrNotFound)
}
