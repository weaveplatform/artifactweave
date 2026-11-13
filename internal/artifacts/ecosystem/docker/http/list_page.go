package dockerhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// listBuffer is how many names a tags list or catalog reads before it
// answers, and how many it reads from the store a query when the page is
// longer. A listing of up to this many names is read whole before its
// status is sent, so a store failure answers 500 as when the list was
// collected; a longer one is written as it is read, and a failure after
// its first bytes ends the connection without the closing brackets, so
// the client sees a broken body rather than a short list (decision 0043,
// "Streaming complete lists"; #1402).
const listBuffer = 1000

// names lists names in Compare's order, from after the last it was opened
// with (layout.TagPages, catalogMerge).
type names interface {
	Next(context.Context) (string, bool, error)
}

// listPage is what a listing answers: the names read before the answer
// starts, the Link to the next page, if any, and the names after them,
// which rest writes while the answer streams. rest is nil when first holds
// the whole page.
type listPage struct {
	first []string
	link  string
	rest  func(context.Context, func(string)) error
}

// readPage reads the page of names that the request's n and last select,
// as paginateHeaders cut it from a collected list: the names after last,
// at most n of them, with a Link while more follow. n of zero gives an
// empty page without a Link (end-8b); an n that is no count lists the rest.
// open opens the listing after a name, reading batch names a query. Only
// the page and the one name after it, which decides the Link, are read:
// the header has to be sent before the body, so a page longer than
// listBuffer is counted first and its names after the first listBuffer are
// read again while the body is written, up to the n-th name the count
// found, or to the end when no Link follows.
func readPage(r *http.Request, open func(last string, batch int) names) (listPage, error) {
	ctx := r.Context()
	values := r.URL.Query()
	last := values.Get("last")
	n, err := strconv.Atoi(values.Get("n"))
	if err != nil || n < 0 {
		return readRest(ctx, open(last, listBuffer))
	}
	var page listPage
	if n == 0 {
		return page, nil
	}
	batch := listBuffer
	if n < listBuffer {
		// n+1 only below the buffer: n may be as large as an int holds.
		batch = n + 1
	}
	src := open(last, batch)
	count, nth := 0, ""
	for count <= n {
		name, ok, err := src.Next(ctx)
		if err != nil {
			return listPage{}, err //nolint:wrapcheck // the listing names itself
		}
		if !ok {
			break
		}
		if count++; count == n {
			nth = name
		}
		if len(page.first) < listBuffer {
			page.first = append(page.first, name)
		}
	}
	more := count > n
	if more {
		page.link = nextLink(r, n, nth)
	}
	if size := min(count, n); len(page.first) >= size {
		page.first = page.first[:size]
		return page, nil
	}
	// The page is longer than the buffer: read its names after the
	// buffered ones again, up to the n-th, which the Link names as the next
	// page's last, or to the end when no Link follows. Without writes the
	// re-read finds the names the count did, in queries sized to them. A
	// name written since the count is listed too, even past n, and one
	// deleted since is not: stopping at n would leave the names between the
	// last one written and the Link's last out of both pages.
	from, want := page.first[len(page.first)-1], n-len(page.first)
	if !more {
		// One more, so that the last query comes back short and ends it.
		want = count - len(page.first) + 1
	}
	page.rest = func(ctx context.Context, emit func(string)) error {
		src := open(from, min(want, listBuffer))
		for {
			name, ok, err := src.Next(ctx)
			if err != nil || !ok {
				return err //nolint:wrapcheck // the listing names itself
			}
			if more && layout.Compare(name, nth) > 0 {
				return nil
			}
			emit(name)
			if more && name == nth {
				return nil
			}
		}
	}
	return page, nil
}

// readRest reads the first listBuffer names of a listing without n, and
// leaves the rest to stream from the same source.
func readRest(ctx context.Context, src names) (listPage, error) {
	var page listPage
	for len(page.first) < listBuffer {
		name, ok, err := src.Next(ctx)
		if err != nil {
			return listPage{}, err //nolint:wrapcheck // the listing names itself
		}
		if !ok {
			return page, nil
		}
		page.first = append(page.first, name)
	}
	page.rest = func(ctx context.Context, emit func(string)) error {
		for {
			name, ok, err := src.Next(ctx)
			if err != nil || !ok {
				return err //nolint:wrapcheck // the listing names itself
			}
			emit(name)
		}
	}
	return page, nil
}

// writePage answers 200 with head, the page's names as a JSON array and
// "}", byte for byte what json.Encoder wrote for the listing's map: its
// keys in order, each name escaped as json.Marshal escapes it, and a
// newline. The names are written as they are read, unflushed, so the
// server frames a short answer with its length, as it did.
func (g *Registry) writePage(w http.ResponseWriter, r *http.Request, head string, page listPage) {
	if page.link != "" {
		w.Header().Set("Link", page.link)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(head + "["))
	sep := ""
	emit := func(name string) {
		quoted, _ := json.Marshal(name)
		_, _ = w.Write(append([]byte(sep), quoted...))
		sep = ","
	}
	for _, name := range page.first {
		emit(name)
	}
	if page.rest != nil {
		if err := page.rest(r.Context(), emit); err != nil {
			if g.fault != nil {
				g.fault(r, err)
			}
			panic(http.ErrAbortHandler)
		}
	}
	_, _ = w.Write([]byte("]}\n"))
}
