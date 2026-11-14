package trash

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Walk is Below read a store page at a time (decision 0043, "Streaming
// complete lists"): the same nodes in the same order, from the entries in
// path order (port.TrashStore.BelowByPath), never all of them at once.
// Start one with Service.Walk and read it with Page; it is not safe for
// concurrent use.
//
// Below sorts the nodes by path, byte by byte, and the entries arrive in
// that order, but a folder implied by what was trashed below it does not
// always sort next to that: "-", "." and every other byte before "/" put
// a/b-1.jar between a/b and a/b/c.jar. So before it yields an entry's node
// the walk asks whether each prefix the entry's path continues with such a
// byte is a folder, by looking for a visible entry below it, first in the
// page it has read ahead and else in the store. The answers for the
// current path's prefixes are kept, since the next paths mostly share them.
type Walk struct {
	s    *Service
	dir  string
	base string
	deep bool
	sees func(model.Item) bool
	// rows are the entries read and not yet walked, in path order; done
	// once the store has no more.
	rows  []model.TrashEntry
	pages *kernel.Pages[model.TrashEntry]
	done  bool
	// last is the path, relative to dir, of the last node yielded: every
	// node at or before it has been yielded.
	last  string
	known []holding
}

// holding is whether the caller sees an entry below rel.
type holding struct {
	rel   string
	holds bool
}

// Walk starts a walk of Below(dir, deep, sees)'s nodes.
func (s *Service) Walk(dir string, deep bool, sees func(model.Item) bool) *Walk {
	dir = strings.Trim(dir, "/")
	base := ""
	if dir != "" {
		base = dir + "/"
	}
	w := &Walk{s: s, dir: dir, base: base, deep: deep, sees: sees}
	w.pages = kernel.NewPages(
		func(ctx context.Context, p kernel.Page) ([]model.TrashEntry, string, error) {
			return s.Store.Trash().BelowByPath(ctx, dir, p) //nolint:wrapcheck // wrapped by read
		},
		s.pageSize(),
	)
	return w
}

func (s *Service) pageSize() int {
	if s.page > 0 {
		return s.page
	}
	return kernel.MaxLimit
}

// Page is the walk as a kernel.PageFunc: about p.Limit nodes, one entry's
// nodes never split, and a cursor while more may follow. The cursor only
// marks that more may follow; the walk keeps its own place.
func (w *Walk) Page(ctx context.Context, p kernel.Page) ([]Node, string, error) {
	var out []Node
	for len(out) < max(p.Limit, 1) {
		rel, e, ok, err := w.entry(ctx)
		if err != nil || !ok {
			return out, "", err
		}
		nodes, err := w.nodes(ctx, rel, e)
		if err != nil {
			return nil, "", err
		}
		out = append(out, nodes...)
	}
	return out, kernel.Cursor("more"), nil
}

// read reads the next store page once the rows read ahead are used up.
func (w *Walk) read(ctx context.Context) error {
	for len(w.rows) == 0 && !w.done {
		rows, ok, err := w.pages.Next(ctx)
		if err != nil {
			return fmt.Errorf("trash: list %s: %w", w.dir, err)
		}
		w.rows, w.done = rows, !ok
	}
	return nil
}

func (w *Walk) visible(e model.TrashEntry) bool {
	return w.sees == nil || w.sees(e.Record.Item)
}

// entry is the next path below dir where the caller sees an entry,
// relative to dir, and the newest such entry there, as Newest keeps it;
// false once there is none.
func (w *Walk) entry(ctx context.Context) (string, model.TrashEntry, bool, error) {
	for {
		if err := w.read(ctx); err != nil {
			return "", model.TrashEntry{}, false, err
		}
		if len(w.rows) == 0 {
			return "", model.TrashEntry{}, false, nil
		}
		at := w.rows[0].TrashPath()
		var newest model.TrashEntry
		found := false
		for {
			for len(w.rows) > 0 && w.rows[0].TrashPath() == at {
				e := w.rows[0]
				w.rows = w.rows[1:]
				if w.visible(e) && (!found || newer(e, newest)) {
					newest, found = e, true
				}
			}
			// One path's entries may run on into the next page.
			if len(w.rows) > 0 || w.done {
				break
			}
			if err := w.read(ctx); err != nil {
				return "", model.TrashEntry{}, false, err
			}
		}
		if found {
			return strings.TrimPrefix(at, w.base), newest, true, nil
		}
	}
}

// newer is Newest's order: the later deletion, then the higher ID.
func newer(e, than model.TrashEntry) bool {
	return e.DeletedAt.After(than.DeletedAt) ||
		(e.DeletedAt.Equal(than.DeletedAt) && e.ID > than.ID)
}

// nodes are the nodes yielded for the entry e at rel: the folders on the
// way to it not yet yielded, which a deep walk lists and a walk of direct
// children lists at the first level, then its own node where the walk lists
// it. Every node before rel is yielded by then: the folders below which a
// later entry lies and which sort before rel are prefixes of rel that rel
// continues with a byte before "/".
func (w *Walk) nodes(ctx context.Context, rel string, e model.TrashEntry) ([]Node, error) {
	w.known = keepPrefixes(w.known, rel)
	first := strings.IndexByte(rel, '/')
	if first < 0 {
		first = len(rel)
	}
	end := len(rel) - 1
	if !w.deep {
		end = min(end, first)
	}
	var out []Node
	for i := 1; i <= end; i++ {
		// A prefix ending in "/" names no folder: a segment that begins
		// with a byte before "/" is not cut there.
		if rel[i] > '/' || rel[i-1] == '/' || rel[:i] <= w.last {
			continue
		}
		if rel[i] < '/' {
			holds, err := w.holds(ctx, rel[:i])
			if err != nil {
				return nil, err
			}
			if !holds {
				continue
			}
		}
		out = append(out, Node{Path: w.path(rel[:i]), Folder: true})
		w.last = rel[:i]
	}
	if !w.deep && first < len(rel) {
		return out, nil
	}
	n := Node{Path: w.path(rel), Folder: e.Record.Item.Folder}
	if !n.Folder {
		holds, err := w.holds(ctx, rel)
		if err != nil {
			return nil, err
		}
		n.Folder = holds
	}
	// A file trashed where a folder later held trashed items gives way to
	// the folder, as in Below.
	if !n.Folder || e.Record.Item.Folder {
		n.Entry = &e
	}
	w.last = rel
	return append(out, n), nil
}

func (w *Walk) path(rel string) string { return w.base + rel }

// keepPrefixes keeps the answers about prefixes of rel: no later path can
// ask about the others, as the paths come in order.
func keepPrefixes(known []holding, rel string) []holding {
	out := known[:0]
	for _, k := range known {
		if strings.HasPrefix(rel, k.rel) {
			out = append(out, k)
		}
	}
	return out
}

// holds reports whether the caller sees an entry below rel, which lies
// after the walk's place: from the rows read ahead when they reach past
// rel's range, and else from the store.
func (w *Walk) holds(ctx context.Context, rel string) (bool, error) {
	for _, k := range w.known {
		if k.rel == rel {
			return k.holds, nil
		}
	}
	lo, hi := w.base+rel+"/", w.base+rel+"0"
	answer, sure := false, w.done
	for i := sort.Search(len(w.rows), func(i int) bool {
		return w.rows[i].TrashPath() >= lo
	}); i < len(w.rows); i++ {
		if w.rows[i].TrashPath() >= hi {
			sure = true
			break
		}
		if w.visible(w.rows[i]) {
			answer, sure = true, true
			break
		}
	}
	if !sure {
		var err error
		if answer, err = w.s.Holds(ctx, w.base+rel, w.sees); err != nil {
			return false, err
		}
	}
	w.known = append(w.known, holding{rel, answer})
	return answer, nil
}

// Holds reports whether the caller sees anything trashed below dir, which
// is whether Below(dir, ...) lists anything. It reads one entry when the
// first is visible, as it always is for a nil sees.
func (s *Service) Holds(ctx context.Context, dir string, sees func(model.Item) bool) (bool, error) {
	dir = strings.Trim(dir, "/")
	p := kernel.Page{Limit: 1}
	for {
		rows, next, err := s.Store.Trash().BelowByPath(ctx, dir, p)
		if err != nil {
			return false, fmt.Errorf("trash: list %s: %w", dir, err)
		}
		for _, e := range rows {
			if sees == nil || sees(e.Record.Item) {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		p = kernel.Page{After: next, Limit: s.pageSize()}
	}
}
