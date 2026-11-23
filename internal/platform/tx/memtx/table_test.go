package memtx_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// tagged fills a table with n rows whose single tag is their zero-padded
// index, so tag order is index order.
func tagged(t *testing.T, s *memtx.Store, n int) {
	t.Helper()
	mustDo(t, s, func() error {
		tab := memtx.TableOf[string, rec](s, "tagged")
		for i := range n {
			k := fmt.Sprintf("r%03d", i)
			tab.Put(k, rec{Tags: []string{k}, Attrs: map[string]string{"k": k}})
		}
		return nil
	})
}

func taggedT(s *memtx.Store) *memtx.Table[string, rec] {
	return memtx.TableOf[string, rec](s, "tagged")
}

func byFirstTag(x, y rec) int { return strings.Compare(x.Tags[0], y.Tags[0]) }

func byFirstTagDesc(x, y rec) int { return byFirstTag(y, x) }

func firstTagKey(r rec) []any { return []any{r.Tags[0]} }

// copies returns how many rows fn made memtx copy.
func copies(fn func()) int64 {
	before := memtx.Copied()
	fn()
	return memtx.Copied() - before
}

func TestUnitTablePageCopiesOnlyThePage(t *testing.T) {
	const n = 50
	s := memtx.New()
	tagged(t, s, n)
	want := make([]string, n)
	for i := range want {
		want[i] = fmt.Sprintf("r%03d", i)
	}
	descending := slices.Clone(want)
	slices.Reverse(descending)
	for _, c := range []struct {
		name string
		cmp  func(x, y rec) int
		desc bool
		want []string
	}{{"ascending", byFirstTag, false, want}, {"descending", byFirstTagDesc, true, descending}} {
		for _, limit := range []int{1, 3, n, n + 1} {
			var got []string
			p := kernel.Page{Limit: limit}
			for pages := 1; ; pages++ {
				if pages > n+1 {
					t.Fatalf("%s, limit %d: the cursors never end", c.name, limit)
				}
				var (
					page []rec
					next string
					err  error
				)
				copied := copies(func() {
					mustDo(t, s, func() error {
						page, next, err = taggedT(s).Page(nil, c.cmp, p, c.desc, firstTagKey)
						return nil
					})
				})
				if err != nil {
					t.Fatalf("%s, limit %d: Page: %v", c.name, limit, err)
				}
				// The acceptance bound of issue 1875 is Limit+1; Page
				// copies only what it returns, which is at most Limit.
				if copied > int64(limit) || copied != int64(len(page)) {
					t.Fatalf("%s, limit %d: a page of %d rows copied %d rows", c.name, limit, len(page), copied)
				}
				got = append(got, tags(page)...)
				if next == "" {
					break
				}
				p.After = next
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("%s, limit %d: pages = %v; want %v", c.name, limit, got, c.want)
			}
		}
	}

	// For contrast: All copies every match, which is what paging through
	// All cost a page.
	if copied := copies(func() {
		mustDo(t, s, func() error { taggedT(s).All(nil, byFirstTag); return nil })
	}); copied != n {
		t.Errorf("All copied %d rows; want %d", copied, n)
	}
}

func TestUnitTablePageFiltersAndKeepsAnswers(t *testing.T) {
	s := memtx.New()
	tagged(t, s, 10)
	even := func(_ string, r rec) bool { return r.Tags[0][len(r.Tags[0])-1]%2 == 0 }
	mustDo(t, s, func() error {
		tab := taggedT(s)
		page, next, err := tab.Page(even, byFirstTag, kernel.Page{Limit: 2}, false, firstTagKey)
		if err != nil || !slices.Equal(tags(page), []string{"r000", "r002"}) || next != kernel.Cursor("r002") {
			t.Errorf("Page(even, limit 2) = %v, %q, %v; want [r000 r002], the cursor at r002", tags(page), next, err)
		}

		// No match is nil, a cursor past the end an empty page: what
		// Paginate over All answered.
		none := func(string, rec) bool { return false }
		if page, next, err := tab.Page(none, byFirstTag, kernel.Page{}, false, firstTagKey); page != nil || next != "" || err != nil {
			t.Errorf("Page(no match) = %v, %q, %v; want nil, \"\", nil", page, next, err)
		}
		past := kernel.Page{After: kernel.Cursor("r999")}
		if page, next, err := tab.Page(nil, byFirstTag, past, false, firstTagKey); page == nil || len(page) != 0 || next != "" || err != nil {
			t.Errorf("Page(past the end) = %#v, %q, %v; want an empty page", page, next, err)
		}

		for name, p := range map[string]kernel.Page{
			"undecodable cursor":      {After: "%%%"},
			"wrong cursor part count": {After: kernel.Cursor("a", "b")},
		} {
			var err error
			if copied := copies(func() {
				_, _, err = tab.Page(nil, byFirstTag, p, false, firstTagKey)
			}); copied != 0 {
				t.Errorf("%s: Page copied %d rows", name, copied)
			}
			if err == nil {
				t.Errorf("%s: Page = nil error", name)
			}
		}
		_, _, err = tab.Page(nil, byFirstTag, kernel.Page{After: "%%%"}, false, firstTagKey)
		if !errors.Is(err, kernel.ErrInvalidCursor) {
			t.Errorf("Page(undecodable cursor) = %v; want %v", err, kernel.ErrInvalidCursor)
		}
		return nil
	})
}

func TestUnitTablePageAndFirstReturnCopies(t *testing.T) {
	s := memtx.New()
	tagged(t, s, 3)
	mustDo(t, s, func() error {
		tab := taggedT(s)
		page, _, _ := tab.Page(nil, byFirstTag, kernel.Page{Limit: 1}, false, firstTagKey)
		first := tab.First(nil, byFirstTag, 1)
		// Change the copies' sort fields: neither the stored rows nor the
		// order a later list sorts by may see it.
		page[0].Tags[0], page[0].Attrs["k"] = "zzz", "caller"
		first[0].Tags[0] = "zzz"
		stored, _ := tab.Get("r000")
		if stored.Tags[0] != "r000" || stored.Attrs["k"] != "r000" {
			t.Errorf("Page or First returned the stored memory: %v %v", stored.Tags, stored.Attrs)
		}
		if again, _, _ := tab.Page(nil, byFirstTag, kernel.Page{Limit: 3}, false, firstTagKey); !slices.Equal(tags(again), []string{"r000", "r001", "r002"}) {
			t.Errorf("Page after the caller changed its copy = %v; want [r000 r001 r002]", tags(again))
		}
		return nil
	})
}

func TestUnitTableFirstAndSorted(t *testing.T) {
	s := memtx.New()
	tagged(t, s, 5)
	mustDo(t, s, func() error {
		tab := taggedT(s)
		for _, c := range []struct {
			n    int
			want []string
		}{
			{2, []string{"r000", "r001"}},
			{5, []string{"r000", "r001", "r002", "r003", "r004"}},
			{9, []string{"r000", "r001", "r002", "r003", "r004"}},
			{0, []string{}},
			{-1, []string{}},
		} {
			var got []rec
			if copied := copies(func() { got = tab.First(nil, byFirstTag, c.n) }); copied != int64(len(c.want)) {
				t.Errorf("First(%d) copied %d rows; want %d", c.n, copied, len(c.want))
			}
			// Empty, not nil, as truncating All's answer gave.
			if got == nil || !slices.Equal(tags(got), c.want) {
				t.Errorf("First(%d) = %#v; want %v", c.n, got, c.want)
			}
		}
		odd := func(_ string, r rec) bool { return r.Tags[0][len(r.Tags[0])-1]%2 == 1 }
		var sorted []rec
		if copied := copies(func() { sorted = tab.Sorted(odd, byFirstTagDesc) }); copied != 0 {
			t.Errorf("Sorted copied %d rows; want none", copied)
		}
		if !slices.Equal(tags(sorted), []string{"r003", "r001"}) {
			t.Errorf("Sorted(odd, descending) = %v; want [r003 r001]", tags(sorted))
		}
		if n := len(tab.Sorted(nil, nil)); n != 5 {
			t.Errorf("Sorted(nil, nil) returned %d rows; want 5", n)
		}
		return nil
	})
}

// TestUnitTablePageConcurrent pages while other goroutines replace rows and
// change the copies they were given; the race detector checks that a page
// shares no memory with the store or with another caller's page.
func TestUnitTablePageConcurrent(t *testing.T) {
	s := memtx.New()
	tagged(t, s, 20)
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := range 20 {
				var page []rec
				err := s.Do(ctx, func() error {
					var err error
					page, _, err = taggedT(s).Page(nil, byFirstTag, kernel.Page{Limit: 3}, false, firstTagKey)
					return err
				})
				if err != nil {
					t.Errorf("Page: %v", err)
					return
				}
				for j := range page {
					page[j].Attrs["k"] = fmt.Sprint(w, i)
				}
				k := fmt.Sprintf("r%03d", i)
				err = s.Tx(ctx, func(ctx context.Context) error {
					return s.Do(ctx, func() error {
						taggedT(s).Put(k, rec{Tags: []string{k}, Attrs: map[string]string{"k": fmt.Sprint(w)}})
						return nil
					})
				})
				if err != nil {
					t.Errorf("Tx: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}
