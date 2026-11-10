package artifactshttp

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// aqlBuildsRestricted is the 400 for a non-admin caller whose token has no
// build scope: without one, Artifactory lets non-admins query items only
// (research 22 §1.3).
const aqlBuildsRestricted = "non-admin queries may use only the items domain; " +
	"builds and build.promotions need a token scoped build:<repository>/<path>:r"

// searchBuildsAQL answers a builds or build.promotions query (decision
// 0012). Administrators see every run. Anyone else needs a token scoped
// build:<pattern>:r, a builds query must include name, number and repo, and
// the rows are the runs, or the promotions of runs, the build permission
// lets the caller read on the run's document path, as the Builds API
// checks it. The limit, scan factor and result cap work as for items.
func (s ArtifactoryAPI) searchBuildsAQL(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	p kernel.Principal,
	q *aql.Query,
) {
	ctx := r.Context()
	run := q
	if !p.Admin {
		if !buildScoped(p.Scope) {
			httpkit.WriteErr(w, r, apierror.New(http.StatusBadRequest, aqlBuildsRestricted))
			return
		}
		if err := q.CheckRestricted(); err != nil {
			httpkit.WriteErr(w, r, apierror.New(http.StatusBadRequest, err.Error()))
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
	timeout := s.aqlTimeout(r)
	dbctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if timeout > 0 {
		var stop context.CancelFunc
		dbctx, stop = context.WithTimeout(dbctx, timeout)
		defer stop()
	}
	hits, err := st.Builds().Search(dbctx, run, o)
	if err != nil {
		httpkit.WriteErr(w, r, s.aqlFailure(ctx, dbctx.Err(), timeout, "aql build search", err))
		return
	}
	trimmed := limit > 0 && len(hits) > limit
	if trimmed {
		hits = hits[:limit]
	}
	if !p.Admin {
		d, _ := accessapi.DeciderFrom(ctx)
		acc, err := itemkit.AccessFor(ctx, s.cfg.Access.Authorizer, p, d)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "aql permissions", err))
			return
		}
		hits = slices.DeleteFunc(hits, func(h aql.BuildHit) bool { return !readsRun(acc, h.Build) })
		if run != q && len(hits) > q.Limit {
			hits = hits[:q.Limit]
		}
	}
	var related aql.Related
	if q.Relates() {
		related, err = s.nestBuilds(dbctx, st, p, q, o.Now, hits)
		if err != nil {
			httpkit.WriteErr(w, r, s.aqlFailure(ctx, dbctx.Err(), timeout, "aql related rows", err))
			return
		}
	}
	res := q.BuildResponse(hits).WithRelated(related)
	if trimmed {
		res.Notification = aqlTrimmed
	}
	writeAQL(w, r, res)
}

// nestBuilds finds the rows of the related domains q's include names, as
// p may see them (nesting). The configured repositories are read only
// then, for the items such an include reaches.
func (s ArtifactoryAPI) nestBuilds(
	ctx context.Context,
	st Store,
	p kernel.Principal,
	q *aql.Query,
	now time.Time,
	hits []aql.BuildHit,
) (aql.Related, error) {
	configs, err := search.RepoConfigs(ctx, st)
	if err != nil {
		return aql.Related{}, err
	}
	n, err := nesting(ctx, s.cfg.Access.Authorizer, p, configs, now)
	if err != nil {
		return aql.Related{}, err
	}
	related, err := q.NestBuilds(ctx, aql.AQLSource(st.Builds(), st.Items()), n, hits)
	if err != nil {
		return related, fmt.Errorf("nest related rows: %w", err)
	}
	return related, nil
}

// buildScoped reports whether the token's scope grants reading builds.
func buildScoped(sc types.Scope) bool {
	return slices.ContainsFunc(sc.Entries, func(e types.ScopeEntry) bool {
		return e.Kind == types.ScopeBuild && e.Actions.Covers(types.ScopeRead)
	})
}

// readsRun reports whether a may read run b: the build resource on the
// build-info repository, at the run's document path.
func readsRun(a itemkit.Access, b types.Build) bool {
	repoKey, path := b.DocumentItem()
	return a.Decider.Allows(
		types.ResourceBuild,
		accessapi.Repo{Key: repoKey, Class: string(repo.Local)},
		path,
		types.ActionRead,
	)
}
