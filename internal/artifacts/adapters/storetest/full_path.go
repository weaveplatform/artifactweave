package storetest

import (
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

// fullPathWorld holds names whose full path order differs from the (path,
// name) order Descendants reads: "-", " " and "!" sort before "/", so a-b,
// "a b" and a/b! come between a and a/b's children; é and Z sort as bytes.
var fullPathWorld = []struct {
	full   string
	folder bool
}{
	{"a", true},
	{"a/b", true},
	{"a/b/c", true},
	{"a-b", true},
	{"a b", true},
	{"é", true},
	{"a/x.txt", false},
	{"a/b/c/d.bin", false},
	{"a/b/c.jar", false},
	{"a/b!", false},
	{"a/b-c", false},
	{"a-b/y", false},
	{"a b/q", false},
	{"a.txt", false},
	{"a0", false},
	{"ab", false},
	{"Z", false},
	{"é/z", false},
}

// testItemsByFullPath: DescendantsByFullPath lists a folder's descendants in
// full path byte order, in pages of any size, leaving out the root's record,
// other repositories and the other side of the cache; and Children's name
// order is that order one level down, which the storage API's file list
// merges by (#1409).
func testItemsByFullPath(t *testing.T, s port.ItemStore) {
	ctx := context.Background()
	for i, w := range fullPathWorld {
		it := fileItem("fp", w.full, i)
		if w.folder {
			it = folderItem("fp", w.full)
		}
		storekit.Get[model.Item](t)(s.Put(ctx, it))
	}
	cached := fileItem("fp", "a/cached.txt", 1)
	cached.Cached = true
	storekit.Get[model.Item](t)(s.Put(ctx, cached))
	storekit.Get[model.Item](t)(s.Put(ctx, fileItem("fp-other", "a/other.txt", 2)))
	storekit.Get[model.Item](t)(s.EnsureRoot(ctx, model.Item{RepoKey: "fp"}))

	below := func(folder string) []string {
		var out []string
		for _, w := range fullPathWorld {
			if folder == "" || strings.HasPrefix(w.full, folder+"/") {
				out = append(out, w.full)
			}
		}
		slices.Sort(out)
		return out
	}
	byFullPath := func(folder string, size int) []string {
		t.Helper()
		return itemPaths(storekit.Pages(t, size, func(p kernel.Page) ([]model.Item, string, error) {
			return s.DescendantsByFullPath(ctx, "fp", false, folder, p)
		}))
	}
	for _, c := range []struct{ folder, want string }{
		{"", ""},
		{".", ""},
		{"/", ""},
		{"a", "a"},
		{"a/", "a"},
		{"/a/b", "a/b"},
		{"a/b/c", "a/b/c"},
		{"é", "é"},
		{"none", "none"},
		{"a/x.txt", "a/x.txt"},
	} {
		for _, size := range []int{1, 2, 5, kernel.MaxLimit} {
			storekit.Same(
				t,
				fmt.Sprintf("DescendantsByFullPath of %q in pages of %d", c.folder, size),
				below(c.want),
				byFullPath(c.folder, size),
			)
		}
	}
	storekit.Same(t, "DescendantsByFullPath of the cache", []string{"a/cached.txt"},
		itemPaths(storekit.Pages(t, 2, func(p kernel.Page) ([]model.Item, string, error) {
			return s.DescendantsByFullPath(ctx, "fp", true, "", p)
		})))
	// A cursor from before the folder's range continues from its start.
	got, _, err := s.DescendantsByFullPath(ctx, "fp", false, "a/b",
		kernel.Page{After: kernel.Cursor("0"), Limit: 1})
	storekit.Must(t, err)
	storekit.Same(t, "a cursor before the range", below("a/b")[:1], itemPaths(got))
	for _, after := range []string{"%%%", kernel.Cursor("a", "b")} {
		_, _, err := s.DescendantsByFullPath(ctx, "fp", false, "", kernel.Page{After: after})
		storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	}

	for _, folder := range []string{"", "a", "a/b"} {
		want := slices.DeleteFunc(below(folder), func(p string) bool {
			if folder != "" {
				p = strings.TrimPrefix(p, folder+"/")
			}
			return strings.Contains(p, "/")
		})
		storekit.Same(t, fmt.Sprintf("Children of %q in full path order", folder), want,
			itemPaths(storekit.Pages(t, 2, func(p kernel.Page) ([]model.Item, string, error) {
				return s.Children(ctx, "fp", false, folder, p)
			})))
	}
}
