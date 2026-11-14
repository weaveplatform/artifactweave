package kernel

import "context"

// PageFunc reads one keyset page of a list: at most p.Limit rows after
// p.After, in the list's order, and the cursor of the next page, "" after
// the last. Every store List method with a Page has this shape once its
// other arguments are bound.
type PageFunc[T any] func(ctx context.Context, p Page) ([]T, string, error)

// Pages walks a PageFunc's pages in order, so that a caller writing a list
// as it reads it holds one page and never the whole list (decision 0043,
// "Streaming complete lists"). The rows come in exactly the order the
// PageFunc gives, the order a caller that collects every page sees. Build
// one with NewPages; it is not safe for concurrent use.
type Pages[T any] struct {
	read PageFunc[T]
	page Page
	done bool
}

// NewPages starts a walk of read's list with pages of limit rows, under
// Page's default and cap.
func NewPages[T any](read PageFunc[T], limit int) *Pages[T] {
	return &Pages[T]{read: read, page: Page{Limit: limit}}
}

// Next reads the next page. It reports false, with no rows, once the list
// has ended. A failed read leaves the walk where it was.
func (p *Pages[T]) Next(ctx context.Context) ([]T, bool, error) {
	if p.done {
		return nil, false, nil
	}
	rows, next, err := p.read(ctx, p.page)
	if err != nil {
		return nil, false, err
	}
	p.page.After, p.done = next, next == ""
	return rows, true, nil
}
