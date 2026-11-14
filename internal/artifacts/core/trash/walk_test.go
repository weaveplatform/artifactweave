package trash_test

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/trash"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// collectedBelow is Below as it was before it walked the store a page at a
// time (#1409): every entry below dir collected, filtered, reduced to the
// newest per path, and the tree derived from them in a map and sorted. The
// walk must give exactly its nodes in exactly its order.
func collectedBelow(
	ctx context.Context,
	s *trash.Service,
	dir string,
	deep bool,
	sees func(model.Item) bool,
) ([]trash.Node, error) {
	dir = strings.Trim(dir, "/")
	repoKey, prefix, _ := strings.Cut(dir, "/")
	entries, err := s.Entries(ctx, repoKey, prefix)
	if err != nil {
		return nil, err
	}
	visible := make([]model.TrashEntry, 0, len(entries))
	for _, e := range entries {
		if sees == nil || sees(e.Record.Item) {
			visible = append(visible, e)
		}
	}
	nodes := map[string]*trash.Node{}
	node := func(p string) *trash.Node {
		n, ok := nodes[p]
		if !ok {
			n = &trash.Node{Path: p}
			nodes[p] = n
		}
		return n
	}
	for _, e := range trash.Newest(visible) {
		rel := strings.Trim(e.Record.Item.RepoKey+"/"+e.Record.Item.FullPath(), "/")
		if dir != "" {
			cut, ok := strings.CutPrefix(rel, dir+"/")
			if !ok {
				continue
			}
			rel = cut
		}
		segs := strings.Split(rel, "/")
		for i := 1; i < len(segs) && (deep || i == 1); i++ {
			node(path.Join(dir, path.Join(segs[:i]...))).Folder = true
		}
		if !deep && len(segs) > 1 {
			continue
		}
		n := node(path.Join(dir, rel))
		n.Entry, n.Folder = &e, n.Folder || e.Record.Item.Folder
	}
	out := make([]trash.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Folder && n.Entry != nil && !n.Entry.Record.Item.Folder {
			n.Entry = nil
		}
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b trash.Node) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// nodeText renders nodes for comparison: path, folder and the entry's ID.
func nodeText(nodes []trash.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		id := int64(-1)
		if n.Entry != nil {
			id = n.Entry.ID
		}
		out = append(out, fmt.Sprintf("%s folder=%v entry=%d", n.Path, n.Folder, id))
	}
	return out
}

// walked reads a whole walk, asking for limit nodes a page.
func walked(
	ctx context.Context,
	w *trash.Walk,
	limit int,
) ([]trash.Node, error) {
	var out []trash.Node
	for {
		nodes, next, err := w.Page(ctx, kernel.Page{Limit: limit})
		if err != nil {
			return nil, err
		}
		out = append(out, nodes...)
		if next == "" {
			return out, nil
		}
	}
}

// Segments and repository keys that put bytes before "/" next to it: a
// folder a and a sibling a-b, a.b, "a b", a! or a- sort a/... after them.
var (
	walkSegments = []string{"a", "b", "a-b", "a.b", "a b", "a!", "a-", "-a", "ab", "a0", "Z", "é"}
	walkRepos    = []string{"r", "r-1", "r1", "r.x"}
)

// randomTrash fills a store with n entries at random paths, some trashed
// again at one path, some folders, with deletion times that tie.
func randomTrash(t *testing.T, rng *rand.Rand, n int) (*inmem.Store, []string) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var fulls []string
	for range n {
		r := walkRepos[rng.IntN(len(walkRepos))]
		var full string
		if len(fulls) > 0 && rng.IntN(5) == 0 {
			r, full, _ = strings.Cut(fulls[rng.IntN(len(fulls))], "/")
		} else {
			segs := make([]string, 1+rng.IntN(3))
			for i := range segs {
				segs[i] = walkSegments[rng.IntN(len(walkSegments))]
			}
			full = strings.Join(segs, "/")
		}
		e := model.TrashEntry{
			Record: model.TrashRecord{
				Item: model.Item{RepoKey: r, Name: full, Folder: rng.IntN(3) == 0},
			},
			DeletedBy: "alice",
		}
		if at := rng.IntN(3); at > 0 {
			e.DeletedAt = t0.Add(time.Duration(at) * time.Hour)
		}
		if _, err := db.Trash().Add(ctx, e); err != nil {
			t.Fatal(err)
		}
		fulls = append(fulls, r+"/"+full)
	}
	return db, fulls
}

// walkDirs are the folders to walk: the root, every repository and every
// prefix of a trashed path, and a few that hold nothing.
func walkDirs(fulls []string) []string {
	dirs := []string{"", "/", "none", "r/none", "r-1/a-b/"}
	dirs = append(dirs, walkRepos...)
	for _, f := range fulls {
		segs := strings.Split(f, "/")
		for i := 2; i <= len(segs); i++ {
			dirs = append(dirs, strings.Join(segs[:i], "/"))
		}
	}
	slices.Sort(dirs)
	return slices.Compact(dirs)
}

// walkFilters are the callers: an administrator, and callers that see
// only some of what was trashed.
var walkFilters = map[string]func(model.Item) bool{
	"admin":    nil,
	"no r1":    func(it model.Item) bool { return it.RepoKey != "r1" },
	"no b":     func(it model.Item) bool { return !strings.Contains(it.FullPath(), "b") },
	"a third":  func(it model.Item) bool { return hashOf(it.RepoKey+"/"+it.FullPath())%3 != 0 },
	"folders":  func(it model.Item) bool { return it.Folder },
	"nobody":   func(model.Item) bool { return false },
	"two of 5": func(it model.Item) bool { return hashOf(it.FullPath())%5 < 2 },
}

func hashOf(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// The walk yields exactly the nodes the collected Below did, in its order,
// for every folder, depth and caller, whatever the store's page size and
// the nodes asked for a page; and Holds says whether there are any.
func TestUnitWalkMatchesCollected(t *testing.T) {
	ctx := context.Background()
	for seed := range uint64(24) {
		rng := rand.New(rand.NewPCG(seed, 1409))
		db, fulls := randomTrash(t, rng, 5+rng.IntN(60))
		ref := &trash.Service{Store: db}
		for _, page := range []int{1, 3, 0} {
			svc := &trash.Service{Store: db}
			trash.SetPage(svc, page)
			for _, dir := range walkDirs(fulls) {
				for name, sees := range walkFilters {
					for _, deep := range []bool{false, true} {
						want, err := collectedBelow(ctx, ref, dir, deep, sees)
						if err != nil {
							t.Fatal(err)
						}
						got, err := walked(ctx, svc.Walk(dir, deep, sees), 1+rng.IntN(4))
						if err != nil {
							t.Fatal(err)
						}
						if !slices.Equal(nodeText(got), nodeText(want)) {
							t.Fatalf("seed %d, page %d, %s, Walk(%q, %v):\n got %q\nwant %q",
								seed, page, name, dir, deep, nodeText(got), nodeText(want))
						}
						if deep {
							continue
						}
						holds, err := svc.Holds(ctx, dir, sees)
						if err != nil || holds != (len(want) > 0) {
							t.Fatalf("seed %d, page %d, %s, Holds(%q) = %v, %v; nodes %q",
								seed, page, name, dir, holds, err, nodeText(want))
						}
					}
				}
			}
		}
	}
}

// failAfter is a store whose trash fails BelowByPath once it has answered
// ok calls.
type failAfter struct {
	*inmem.Store
	ok *int
}

func (f failAfter) Trash() port.TrashStore { return failAfterTrash{f.Store.Trash(), f.ok} }

type failAfterTrash struct {
	port.TrashStore
	ok *int
}

func (f failAfterTrash) BelowByPath(
	ctx context.Context,
	dir string,
	p kernel.Page,
) ([]model.TrashEntry, string, error) {
	if *f.ok == 0 {
		return nil, "", errBoom
	}
	*f.ok--
	return f.TrashStore.BelowByPath(ctx, dir, p) //nolint:wrapcheck // test double
}

// A failed store read fails the walk wherever it falls: the first page, a
// later one, a path's entries running on into the next page, or a look
// past the page read ahead for a folder; and Holds.
func TestUnitWalkFailures(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for _, p := range []string{"r/a/x", "r/a/x", "r/a-b", "r/a-c", "r/a/y", "r/b"} {
		r, full, _ := strings.Cut(p, "/")
		if _, err := db.Trash().Add(ctx, model.TrashEntry{
			Record: model.TrashRecord{Item: model.Item{RepoKey: r, Name: full}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	want, err := collectedBelow(ctx, &trash.Service{Store: db}, "", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for ok := 0; ; ok++ {
		left := ok
		svc := &trash.Service{Store: failAfter{db, &left}}
		trash.SetPage(svc, 1)
		got, err := walked(ctx, svc.Walk("", true, nil), 2)
		if err == nil {
			if !slices.Equal(nodeText(got), nodeText(want)) {
				t.Fatalf("after %d reads: %q, want %q", ok, nodeText(got), nodeText(want))
			}
			calls = ok
			break
		}
		if !errors.Is(err, errBoom) {
			t.Fatalf("read %d failing: %v", ok, err)
		}
	}
	if calls < 6 {
		t.Fatalf("the walk read %d pages; the failures were not each reached", calls)
	}
	none := 0
	if _, err := (&trash.Service{Store: failAfter{db, &none}}).Holds(
		ctx,
		"r",
		nil,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("Holds: %v", err)
	}
	one := 1
	if _, err := (&trash.Service{Store: failAfter{db, &one}}).Holds(
		ctx,
		"r",
		func(model.Item) bool { return false },
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("Holds past its first page: %v", err)
	}
}
