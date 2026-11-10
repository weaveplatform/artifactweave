package artifactshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// AQLLimits bounds POST /api/search/aql (decision 0012).
type AQLLimits struct {
	// MaxResults caps the rows of every query, its own limit included; zero
	// means no cap.
	MaxResults int
	// ScanFactor multiplies a non-admin query's limit, so that limit can
	// still be met once rows the caller may not read are dropped. Below 1
	// is 1. A request's scan-factor header above 1 overrides it.
	ScanFactor int
	// Timeout cancels a query's database work after this long, and
	// StreamTimeout one sent with a stream: true header; zero means no
	// timeout. The answer is then 408.
	Timeout, StreamTimeout time.Duration
}

// aqlBodyLimit bounds the body read: aql.MaxLength characters of at most
// four bytes each. Parse enforces the character count itself.
const aqlBodyLimit = 4*aql.MaxLength + 1

// aqlTrimmed is the range.notification of a result the cap trimmed, word for
// word as Artifactory writes it (research 22 §1.2).
const aqlTrimmed = "AQL query reached the search hard limit, results are trimmed. " +
	"Please address configurable limit."

// searchAQL answers POST /api/search/aql. Every JFrog CLI file command
// resolves its file specs through it (research 09). Callers must be
// authenticated; repository keys resolve against the configured
// repositories (aql.Query.Resolve); non-admin queries must return repo, path
// and name, and their rows are limited to what the caller may read and the
// repository's include and exclude patterns accept. items.delete and
// properties.update run here too (aqlAction).
func (s ArtifactoryAPI) searchAQL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if p, ok := kernel.FromContext(ctx); ok && p.Anonymous {
		// Artifactory refuses anonymous queries with 403, even with anonymous
		// access on (research 22 §1.3); with it off the request never gets
		// here, and the middleware's 401 asks for credentials.
		httpkit.WriteErr(
			w,
			r,
			apierror.New(http.StatusForbidden, "AQL queries need an authenticated user"),
		)
		return
	}
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, aqlBodyLimit))
	if err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("reading the query: %v", err))
		return
	}
	// At debug level the queries clients send are kept: the acceptance
	// suite captures the JFrog CLI's from here as
	// migration/artifacts/core/aql fixtures.
	s.Log().
		DebugContext(ctx, "aql query", "request_id", httpkit.RequestIDFrom(ctx), "query", string(body))
	q, err := aql.Parse(string(body))
	if err != nil {
		httpkit.WriteErr(w, r, apierror.New(http.StatusBadRequest, aqlMessage(err)))
		return
	}
	if s.dispatchAlternateAQL(w, r, st, p, q) {
		return
	}
	configs, err := search.RepoConfigs(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "aql repositories", err))
		return
	}
	q.Resolve(aql.AQLRepositories(configs))
	run, scaled := q.ForNesting(), false
	if !p.Admin {
		if err := q.CheckRestricted(); err != nil {
			httpkit.WriteErr(w, r, apierror.New(http.StatusBadRequest, err.Error()))
			return
		}
		if f := scanFactor(r, s.cfg.AQL.ScanFactor); f > 1 && q.HasLimit {
			scan := *run
			scan.Limit = math.MaxInt
			if q.Limit <= math.MaxInt/f {
				scan.Limit = q.Limit * f
			}
			run, scaled = &scan, true
		}
	}
	// One row past the cap tells a trimmed result from one that fits.
	limit := s.cfg.AQL.MaxResults
	o := aql.Options{Now: time.Now().UTC()}
	if limit > 0 {
		o.Max = limit + 1
	}
	timeout := s.aqlTimeout(r)
	dbctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if timeout > 0 {
		var stop context.CancelFunc
		dbctx, stop = context.WithTimeout(dbctx, timeout)
		defer stop()
	}
	hits, err := st.Items().Search(dbctx, run, o)
	if err != nil {
		httpkit.WriteErr(w, r, s.aqlFailure(ctx, dbctx.Err(), timeout, "aql search", err))
		return
	}
	trimmed := limit > 0 && len(hits) > limit
	if trimmed {
		hits = hits[:limit]
	}
	switch {
	case q.Action != aql.Find:
		if hits, err = s.aqlAction(ctx, st, p, q, configs, hits); err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, q.Action.String(), err))
			return
		}
	case !p.Admin:
		if hits, err = readable(ctx, s.cfg.Access.Authorizer, p, configs, hits); err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "aql permissions", err))
			return
		}
		if scaled && len(hits) > q.Limit {
			hits = hits[:q.Limit]
		}
	}
	if q.VirtualRepos() {
		virtualRepos(configs, hits)
	}
	var related aql.Related
	if q.Relates() {
		n, err := nesting(ctx, s.cfg.Access.Authorizer, p, configs, o.Now)
		if err == nil {
			related, err = q.Nest(dbctx, aql.AQLSource(st.Builds(), st.Items()), n, hits)
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.aqlFailure(ctx, dbctx.Err(), timeout, "aql related rows", err))
			return
		}
	}
	res := q.Response(hits).WithRelated(related)
	if trimmed {
		res.Notification = aqlTrimmed
	}
	writeAQL(w, r, res)
}

// aqlTimeout is how long a query's database work may run, zero for no
// limit: the configured timeout, or the streamed one for a stream: true
// request, as Artifactory's artifactory.aql.query.timeout.seconds and
// artifactory.aql.streamed.query.timeout do (aql-performance). Its
// deadline cancels the statement in the database through the context. It
// covers the reads only: an action's deletes and updates, once started,
// run to the end rather than stop half done.
func (s ArtifactoryAPI) aqlTimeout(r *http.Request) time.Duration {
	if stream, _ := strconv.ParseBool(r.Header.Get("stream")); stream {
		return s.cfg.AQL.StreamTimeout
	}
	return s.cfg.AQL.Timeout
}

// aqlFailure is the answer to a query that failed while it read: 408 when
// its timeout cancelled it, which deadline, the reads' context's error,
// says, 400 for an include that reaches too many related rows, and
// otherwise the internal error op names. A client that went away has
// cancelled ctx, the request's context, which tells it from the timeout.
func (s ArtifactoryAPI) aqlFailure(
	ctx context.Context,
	deadline error,
	timeout time.Duration,
	op string,
	err error,
) error {
	switch {
	case timeout > 0 && errors.Is(deadline, context.DeadlineExceeded) && ctx.Err() == nil:
		return apierror.New(http.StatusRequestTimeout, fmt.Sprintf(
			"The AQL query exceeded its timeout of %s seconds and was cancelled",
			strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64)))
	case errors.Is(err, aql.ErrInvalid):
		return apierror.New(http.StatusBadRequest, err.Error())
	}
	return s.InternalError(ctx, op, err)
}

// nesting is how the rows of related domains render for p: everything for
// an administrator, and for anyone else the items they may read that their
// repository's patterns accept and the runs they may read, as the found
// rows are filtered.
func nesting(
	ctx context.Context,
	az accessapi.Authorizer,
	p kernel.Principal,
	configs map[string]repo.Config,
	now time.Time,
) (aql.Nesting, error) {
	n := aql.Nesting{Now: now}
	if p.Admin {
		return n, nil
	}
	d, _ := accessapi.DeciderFrom(ctx)
	acc, err := itemkit.AccessFor(ctx, az, p, d)
	if err != nil {
		return n, err
	}
	n.ItemVisible = func(it model.Item) bool {
		return len(readableRows(acc, configs, []aql.Hit{{Item: it}}, 0)) == 1
	}
	n.RunVisible = func(b model.Build) bool { return readsRun(acc, b) }
	return n, nil
}

// aqlMessage is the client-facing text of a parse error. The length
// error's text is Artifactory's own, naming the limit; the others carry
// the parser's position.
func aqlMessage(err error) string {
	if errors.Is(err, aql.ErrTooLong) {
		return aql.ErrTooLong.Error()
	}
	return err.Error()
}

// scanFactor is the request's scan-factor header when it is an integer above
// 1, which is when Artifactory honours it, and otherwise the configured one.
func scanFactor(r *http.Request, configured int) int {
	if n, err := strconv.Atoi(r.Header.Get("scan-factor")); err == nil && n > 1 {
		return n
	}
	return configured
}

// writeAQL writes the response indented, as Artifactory does, or minified
// for ?compact=true. A stream: true header changes only the timeout
// (aqlDeadline): Artifactory's streaming changes how it reads the
// database, not what it answers.
func writeAQL(w http.ResponseWriter, r *http.Request, res aql.Response) {
	writeAQLBytes(w, renderAQL(r, res))
}

func renderAQL(r *http.Request, res aql.Response) []byte {
	// The response renders valid JSON by hand, so neither step can fail.
	b, _ := json.Marshal(res)
	if compact, _ := strconv.ParseBool(r.URL.Query().Get("compact")); !compact {
		var out bytes.Buffer
		_ = json.Indent(&out, b, "", "  ")
		b = out.Bytes()
	}
	return b
}

func writeAQLBytes(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// readable keeps the rows p may read, by repository and path, that the
// repository's include and exclude patterns accept. The filter runs after
// offset and limit, so a page can come back short unless the scan factor
// reads ahead.
func readable(
	ctx context.Context,
	az accessapi.Authorizer,
	p kernel.Principal,
	configs map[string]repo.Config,
	hits []aql.Hit,
) ([]aql.Hit, error) {
	d, _ := accessapi.DeciderFrom(ctx)
	acc, err := itemkit.AccessFor(ctx, az, p, d)
	if err != nil {
		return nil, err
	}
	return readableRows(acc, configs, hits, 0), nil
}

// readableRows keeps the hits a may read, and also act on with want when it
// names any action, that their repository's include and exclude patterns
// accept.
func readableRows(
	a itemkit.Access,
	configs map[string]repo.Config,
	hits []aql.Hit,
	want accessapi.Actions,
) []aql.Hit {
	patterns := map[string]repo.RepoPatterns{}
	out := hits[:0]
	for _, h := range hits {
		c, ok := configs[h.Item.RepoKey]
		if !ok {
			continue
		}
		pat, ok := patterns[c.Key]
		if !ok {
			pat = repo.PatternsOf(c)
			patterns[c.Key] = pat
		}
		full := h.Item.FullPath()
		if pat.Accepts(full, h.Item.Folder) &&
			a.Item(c, full, h.Item.Folder, accessapi.ActionRead) &&
			(want == 0 || a.File(c, full, want)) {
			out = append(out, h)
		}
	}
	return out
}

// virtualRepos fills each hit with the virtual repositories, sorted, whose
// members include its repository, directly or through nested virtual
// repositories. A cached item counts under its remote, which is the key a
// virtual repository lists.
func virtualRepos(configs map[string]repo.Config, hits []aql.Hit) {
	in := map[string][]string{}
	for _, c := range configs {
		if c.Class != repo.Virtual {
			continue
		}
		reached := map[string]bool{}
		reach(configs, c.Key, reached)
		for key := range reached {
			if key != c.Key {
				in[key] = append(in[key], c.Key)
			}
		}
	}
	for _, keys := range in {
		slices.Sort(keys)
	}
	for n := range hits {
		hits[n].VirtualRepos = in[hits[n].Item.RepoKey]
	}
}

// reach marks key and every repository a virtual key leads to.
func reach(configs map[string]repo.Config, key string, reached map[string]bool) {
	if reached[key] {
		return
	}
	reached[key] = true
	if c := configs[key]; c.Class == repo.Virtual {
		for _, m := range c.Repositories {
			reach(configs, m, reached)
		}
	}
}

func (s ArtifactoryAPI) dispatchAlternateAQL(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	p kernel.Principal,
	q *aql.Query,
) bool {
	if q.Transitive {
		s.searchTransitiveAQL(w, r, st, p, q)
		return true
	}
	if q.Domain != aql.DomainItems {
		s.searchBuildsAQL(w, r, st, p, q)
		return true
	}
	return false
}
