package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// aqlHardLimit caps a query that sets no limit of its own (decision 0012);
// the response then says so in range.notification. A variable so tests can
// reach it.
var aqlHardLimit = 50_000

// aqlBodyLimit bounds the body read: aql.MaxLength characters of at most
// four bytes each. Parse enforces the character count itself.
const aqlBodyLimit = 4*aql.MaxLength + 1

// searchAQL answers POST /api/search/aql. Every JFrog CLI file command
// resolves its file specs through it (research 09). Callers must be
// authenticated; non-admin queries must return repo, path and name, and
// their rows are limited to repositories the caller may read.
func (s artifactoryService) searchAQL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, aqlBodyLimit))
	if err != nil {
		writeErr(w, r, badRequest("reading the query: %v", err))
		return
	}
	// At debug level the queries clients send are kept: the acceptance
	// suite captures the JFrog CLI's from here as pkg/aql fixtures.
	s.log().DebugContext(ctx, "aql query", "request_id", requestIDFrom(ctx), "query", string(body))
	q, err := aql.Parse(string(body))
	if err != nil {
		writeErr(w, r, apierror.New(http.StatusBadRequest, aqlMessage(err)))
		return
	}
	if !p.Admin {
		if err := q.CheckRestricted(); err != nil {
			writeErr(w, r, apierror.New(http.StatusBadRequest, err.Error()))
			return
		}
	}
	hits, err := st.Items().Search(ctx, q, aql.Options{Now: time.Now().UTC(), Limit: aqlHardLimit})
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "aql search", err))
		return
	}
	capped := !q.HasLimit && len(hits) == aqlHardLimit
	if !p.Admin {
		if hits, err = readable(r, st, p, hits); err != nil {
			writeErr(w, r, s.internalError(ctx, "aql permissions", err))
			return
		}
	}
	res := q.Response(hits)
	if capped {
		res.Notification = fmt.Sprintf(
			"AQL search results are limited to %d results. Use limit and offset to page through them.",
			aqlHardLimit,
		)
	}
	writeJSON(w, http.StatusOK, res)
}

// aqlMessage is the client-facing text of a parse error. The length error's
// text is Artifactory's own; the others carry the parser's position.
func aqlMessage(err error) string {
	if errors.Is(err, aql.ErrTooLong) {
		return aql.ErrTooLong.Error()
	}
	return err.Error()
}

// readable keeps the rows p may read, by repository and path. The filter
// runs after offset and limit, so a page can come back short.
func readable(
	r *http.Request,
	st store.Store,
	p auth.Principal,
	hits []aql.Hit,
) ([]aql.Hit, error) {
	ctx := r.Context()
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, err
	}
	configs, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	byKey := make(map[string]repo.Config, len(configs))
	for _, c := range configs {
		byKey[c.Key] = c
	}
	out := hits[:0]
	for _, h := range hits {
		c, ok := byKey[h.Item.RepoKey]
		if ok && acc.item(c, h.Item.FullPath(), h.Item.Folder, types.ActionRead) {
			out = append(out, h)
		}
	}
	return out, nil
}
