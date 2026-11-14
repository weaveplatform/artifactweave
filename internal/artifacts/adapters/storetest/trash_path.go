package storetest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// trashPathWorld holds trashed paths whose path order differs from the
// (repository, path, name) order List reads: "-", " " and "!" sort before
// "/", so r1/a-b and "r1/a b" come between r1/a and r1/a/b, and r1 sorts
// before r1-x although r1-x/... sorts before r1/...; é sorts as bytes. A
// path trashed twice is listed once per entry, in ID order.
var trashPathWorld = []struct{ repo, full string }{
	{"r1", "a/b/c.jar"},
	{"r1", "a-b/y"},
	{"r1", "a b/q"},
	{"r1", "a/b!"},
	{"r1", "a/x.txt"},
	{"r1", "a"},
	{"r1", "a.txt"},
	{"r1", "é/z"},
	{"r1", "a/x.txt"},
	{"r1-x", "a/b"},
	{"r1x", "q"},
	{"r", "z"},
	{"r1", "Z"},
}

// testTrashByPath: BelowByPath lists the entries below a trash can folder
// in path byte order, then by ID, in pages of any size, the order the
// storage API's views of the trash can walk its tree in (#1409).
func testTrashByPath(t *testing.T, s port.TrashStore) {
	ctx := context.Background()
	type entry struct {
		path string
		id   int64
	}
	all := make([]entry, 0, len(trashPathWorld))
	for i, w := range trashPathWorld {
		e := storekit.Get[model.TrashEntry](t)(s.Add(ctx, trashEntry(w.repo, w.full, i, i+1)))
		all = append(all, entry{w.repo + "/" + w.full, e.ID})
	}
	slices.SortFunc(all, func(a, b entry) int {
		return cmp.Or(strings.Compare(a.path, b.path), cmp.Compare(a.id, b.id))
	})
	below := func(dir string) []string {
		out := []string{}
		for _, e := range all {
			if dir == "" || strings.HasPrefix(e.path, dir+"/") {
				out = append(out, fmt.Sprintf("%s@%d", e.path, e.id))
			}
		}
		return out
	}
	byPath := func(dir string, size int) []string {
		t.Helper()
		got := storekit.Pages(t, size, func(p kernel.Page) ([]model.TrashEntry, string, error) {
			return s.BelowByPath(ctx, dir, p)
		})
		out := make([]string, 0, len(got))
		for _, e := range got {
			out = append(out, fmt.Sprintf("%s@%d", e.TrashPath(), e.ID))
		}
		return out
	}
	for _, c := range []struct{ dir, want string }{
		{"", ""},
		{"/", ""},
		{"r1", "r1"},
		{"/r1/", "r1"},
		{"r1/a", "r1/a"},
		{"r1/a/b", "r1/a/b"},
		{"r1/é", "r1/é"},
		{"r1-x", "r1-x"},
		{"none", "none"},
		{"r1/a/x.txt", "r1/a/x.txt"},
	} {
		for _, size := range []int{1, 2, 5, kernel.MaxLimit} {
			storekit.Same(t, fmt.Sprintf("BelowByPath of %q in pages of %d", c.dir, size),
				below(c.want), byPath(c.dir, size))
		}
	}
	// A cursor from before the folder's range continues from its start.
	got, _, err := s.BelowByPath(ctx, "r1/a", kernel.Page{After: kernel.Cursor("0", "0"), Limit: 1})
	storekit.Must(t, err)
	storekit.Same(t, "a cursor before the range", 1, len(got))
	storekit.Same(t, "a cursor before the range", below("r1/a")[0],
		fmt.Sprintf("%s@%d", got[0].TrashPath(), got[0].ID))
	// The ID part is read where the path part matches an entry's: r/z is
	// the first entry's path.
	for _, after := range []string{"%%%", kernel.Cursor("a"), kernel.Cursor("r/z", "b")} {
		_, _, err := s.BelowByPath(ctx, "", kernel.Page{After: after})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}
}
