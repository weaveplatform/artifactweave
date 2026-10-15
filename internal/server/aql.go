package server

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
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
// repository's include and exclude patterns accept.
func (s artifactoryService) searchAQL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if p, ok := auth.FromContext(ctx); ok && p.Anonymous {
		// Artifactory refuses anonymous queries with 403, even with anonymous
		// access on (research 22 §1.3); with it off the request never gets
		// here, and the middleware's 401 asks for credentials.
		writeErr(w, r, apierror.New(http.StatusForbidden, "AQL queries need an authenticated user"))
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
	configs, err := repoConfigs(ctx, st)
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "aql repositories", err))
		return
	}
	q.Resolve(aqlRepositories(configs))
	run := q
	if !p.Admin {
		if err := q.CheckRestricted(); err != nil {
			writeErr(w, r, apierror.New(http.StatusBadRequest, err.Error()))
			return
		}
		if f := scanFactor(r, s.cfg.AQL.ScanFactor); f > 1 && q.HasLimit {
			scan := *q
			scan.Limit = math.MaxInt
			if q.Limit <= math.MaxInt/f {
				scan.Limit = q.Limit * f
			}
			run = &scan
		}
	}
	// One row past the cap tells a trimmed result from one that fits.
	limit := s.cfg.AQL.MaxResults
	o := aql.Options{Now: time.Now().UTC()}
	if limit > 0 {
		o.Max = limit + 1
	}
	hits, err := st.Items().Search(ctx, run, o)
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "aql search", err))
		return
	}
	trimmed := limit > 0 && len(hits) > limit
	if trimmed {
		hits = hits[:limit]
	}
	if !p.Admin {
		if hits, err = readable(ctx, st, p, configs, hits); err != nil {
			writeErr(w, r, s.internalError(ctx, "aql permissions", err))
			return
		}
		if run != q && len(hits) > q.Limit {
			hits = hits[:q.Limit]
		}
	}
	if q.VirtualRepos() {
		virtualRepos(configs, hits)
	}
	res := q.Response(hits)
	if trimmed {
		res.Notification = aqlTrimmed
	}
	writeAQL(w, r, res)
}

// aqlMessage is the client-facing text of a parse error. The length error's
// text is Artifactory's own; the others carry the parser's position.
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
// for ?compact=true. A stream: true header changes nothing: Artifactory's
// streaming changes how it reads the database, not what it answers.
func writeAQL(w http.ResponseWriter, r *http.Request, res aql.Response) {
	// The response renders valid JSON by hand, so neither step can fail.
	b, _ := json.Marshal(res)
	if compact, _ := strconv.ParseBool(r.URL.Query().Get("compact")); !compact {
		var out bytes.Buffer
		_ = json.Indent(&out, b, "", "  ")
		b = out.Bytes()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func repoConfigs(ctx context.Context, st store.Store) (map[string]repo.Config, error) {
	configs, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	byKey := make(map[string]repo.Config, len(configs))
	for _, c := range configs {
		byKey[c.Key] = c
	}
	return byKey, nil
}

// aqlRepositories is what a repo criterion resolves keys against: remote and
// virtual repositories stand for their caches and members, and every other
// class holds its own items.
func aqlRepositories(configs map[string]repo.Config) aql.Repositories {
	out := make(aql.Repositories, len(configs))
	for key, c := range configs {
		switch c.Class {
		case repo.Remote:
			out[key] = aql.Repository{Class: aql.RepoRemote}
		case repo.Virtual:
			out[key] = aql.Repository{Class: aql.RepoVirtual, Members: c.Repositories}
		default:
			out[key] = aql.Repository{Class: aql.RepoLocal}
		}
	}
	return out
}

// readable keeps the rows p may read, by repository and path, that the
// repository's include and exclude patterns accept. The filter runs after
// offset and limit, so a page can come back short unless the scan factor
// reads ahead.
func readable(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	configs map[string]repo.Config,
	hits []aql.Hit,
) ([]aql.Hit, error) {
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, err
	}
	patterns := map[string]repoPatterns{}
	out := hits[:0]
	for _, h := range hits {
		c, ok := configs[h.Item.RepoKey]
		if !ok {
			continue
		}
		pat, ok := patterns[c.Key]
		if !ok {
			pat = patternsOf(c)
			patterns[c.Key] = pat
		}
		full := h.Item.FullPath()
		if pat.accepts(full, h.Item.Folder) && acc.item(c, full, h.Item.Folder, types.ActionRead) {
			out = append(out, h)
		}
	}
	return out, nil
}

// repoPatterns are a repository's include and exclude patterns: Ant
// patterns, comma-separated in the configuration.
type repoPatterns struct {
	include, exclude []authz.Pattern
}

func patternsOf(c repo.Config) repoPatterns {
	include := compilePatterns(c.IncludesPattern)
	if len(include) == 0 {
		include = []authz.Pattern{authz.CompilePattern(repo.DefaultIncludesPattern)}
	}
	return repoPatterns{include: include, exclude: compilePatterns(c.ExcludesPattern)}
}

func compilePatterns(s *string) []authz.Pattern {
	if s == nil {
		return nil
	}
	var out []authz.Pattern
	for _, p := range strings.Split(*s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, authz.CompilePattern(p))
		}
	}
	return out
}

// accepts reports whether the patterns admit path. A folder is admitted
// when an include could match something below it, so the folders leading to
// included files stay visible.
func (p repoPatterns) accepts(path string, folder bool) bool {
	for _, x := range p.exclude {
		if x.Match(path) {
			return false
		}
	}
	for _, in := range p.include {
		if in.Match(path) || (folder && in.MatchBelow(path)) {
			return true
		}
	}
	return false
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
