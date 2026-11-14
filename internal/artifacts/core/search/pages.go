package search

import (
	"context"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Pages walks the hits Collect returns a store query at a time, so a search
// can write its results as it reads them (decision 0043, "Streaming complete
// lists"): the same queries of SearchPage rows in item order, the caller's
// Readable filter and keep on each, and no more than want hits in all (-1
// for no cap). A page may hold fewer hits than its query read, or none. A
// failed query or permission read is Internal's error. The walk keeps its
// offset and count itself, so it serves one search; its cursors only mark
// that more follows.
func (r *SearchRun) Pages(
	criteria aql.Node,
	include []string,
	want int,
	keep func(aql.Hit) bool,
) *kernel.Pages[aql.Hit] {
	offset, kept := 0, 0
	return kernel.NewPages(func(ctx context.Context, _ kernel.Page) ([]aql.Hit, string, error) {
		q := &aql.Query{
			Criteria: criteria, Include: include,
			Offset: offset, HasOffset: true, Limit: SearchPage, HasLimit: true,
		}
		q.Resolve(r.Repos)
		hits, err := r.st.Items().Search(ctx, q, aql.Options{Now: r.Now})
		if err != nil {
			return nil, "", r.internal(ctx, "search", err)
		}
		read := len(hits)
		if !r.Principal.Admin {
			if hits, err = r.access.Readable(ctx, r.Configs, hits); err != nil {
				return nil, "", r.internal(ctx, "search permissions", err)
			}
		}
		out := make([]aql.Hit, 0, len(hits))
		for _, h := range hits {
			if keep != nil && !keep(h) {
				continue
			}
			out = append(out, h)
			if kept++; kept == want {
				return out, "", nil
			}
		}
		if read < SearchPage {
			return out, "", nil
		}
		offset += SearchPage
		return out, kernel.Cursor(strconv.Itoa(offset)), nil
	}, SearchPage)
}
