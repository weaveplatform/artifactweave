package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const transitiveRows = 10000

type transitiveMember struct {
	config    repo.Config
	routes    [][]repo.Config
	canSearch bool
}
type transitiveResult struct {
	result      source.Result
	paths       []types.Item
	isTruncated bool
}

type transitiveSearch struct {
	hits       []aql.Hit
	isTrimmed  bool
	revalidate func(context.Context) error
}

func (s artifactoryService) searchTransitiveAQL(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	p auth.Principal,
	q *aql.Query,
) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if timeout := s.aqlTimeout(r); timeout > 0 && timeout < time.Minute {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, timeout)
		defer stop()
	}
	found, err := s.transitiveAQL(ctx, r, st, p, q)
	if err != nil {
		writeErr(w, r, transitiveError(err))
		return
	}
	response := q.Response(found.hits)
	if found.isTrimmed {
		response.Notification = aqlTrimmed
	}
	body := renderAQL(r, response)
	if found.revalidate != nil {
		if err = found.revalidate(ctx); err != nil {
			writeErr(w, r, transitiveError(err))
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAQLBytes(w, body)
}

func (s artifactoryService) transitiveAQL(
	ctx context.Context,
	r *http.Request,
	st store.Store,
	p auth.Principal,
	q *aql.Query,
) (transitiveSearch, error) {
	key, err := q.TransitiveTarget()
	if err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive aql: %w", err)
	}
	if !p.Admin {
		if err = q.CheckRestricted(); err != nil {
			return transitiveSearch{}, fmt.Errorf("transitive aql: %w", err)
		}
	}
	configs, err := repoConfigs(ctx, st)
	if err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive repositories: %w", err)
	}
	targetConfig, exists := configs[key]
	if !exists || targetConfig.Class != repo.Remote && targetConfig.Class != repo.Virtual {
		return transitiveSearch{}, aql.ErrInvalid
	}
	members, err := transitiveMembers(configs, key)
	if err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive aql: %w", err)
	}
	budget := transitiveRows
	if s.cfg.AQL.MaxResults > 0 {
		budget = min(budget, s.cfg.AQL.MaxResults)
	}
	if q.HasLimit {
		factor := 1
		if !p.Admin {
			factor = max(1, scanFactor(r, s.cfg.AQL.ScanFactor))
		}
		if q.Limit <= budget/factor {
			budget = q.Limit * factor
		}
	}
	original := *q
	q.Resolve(aqlRepositories(configs))
	if budget == 0 {
		return transitiveSearch{hits: []aql.Hit{}}, nil
	}
	run := *q
	run.Transitive = false
	run.HasLimit = true
	run.Limit = budget + 1
	hits, err := st.Items().
		Search(ctx, run.ForNesting(), aql.Options{Now: time.Now().UTC(), Max: budget + 1})
	if err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive cached search: %w", err)
	}
	trimmed := len(hits) > budget
	remoteHits, fences, err := s.transitiveCollect(ctx, st, p, members, &original, budget+1)
	if err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive aql: %w", err)
	}
	hits = append(hits, remoteHits...)
	current, err := s.transitivePrincipal(ctx, st, p)
	if err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive aql: %w", err)
	}
	hits = filterTransitiveHits(p, members, hits)
	hits = filterTransitiveHits(current, members, hits)
	hits = deduplicateTransitive(hits)
	trimmed = trimmed || len(hits) > budget ||
		slices.ContainsFunc(fences, func(f transitiveResult) bool { return f.isTruncated })
	if len(hits) > budget {
		hits = hits[:budget]
	}
	if q.HasLimit && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	if q.VirtualRepos() {
		virtualRepos(configs, hits)
	}
	if err = ctx.Err(); err != nil {
		return transitiveSearch{}, fmt.Errorf("transitive aql: %w", err)
	}
	return transitiveSearch{
		hits:      hits,
		isTrimmed: trimmed,
		revalidate: func(current context.Context) error {
			return s.fenceTransitive(current, st, p, configs, members, fences, hits)
		},
	}, nil
}

func (s artifactoryService) transitivePrincipal(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
) (auth.Principal, error) {
	if s.cfg.Auth == nil {
		return auth.Principal{}, source.ErrDenied
	}
	current, err := s.cfg.Auth.Current(ctx, p, st.Tokens())
	if err != nil {
		return current, source.ErrDenied
	}
	current, err = auth.Authorize(
		ctx,
		authz.Sources{Permissions: st.Permissions(), Groups: st.Groups()},
		current,
	)
	if err != nil {
		return current, source.ErrDenied
	}
	return current, nil
}

func transitiveVisible(p auth.Principal, m transitiveMember, it types.Item) bool {
	allows := p.Access().Allows
	if it.Folder {
		allows = p.Access().AllowsFolder
	}
	if !allows(types.ResourceArtifact, target(m.config), it.FullPath(), types.ActionRead) {
		return false
	}
	if p.Admin {
		return true
	}
	if !authz.PatternsOf(m.config).Accepts(it.FullPath(), it.Folder) {
		return false
	}
	for _, route := range m.routes {
		if !slices.ContainsFunc(
			route,
			func(c repo.Config) bool { return !authz.PatternsOf(c).Accepts(it.FullPath(), it.Folder) },
		) {
			return true
		}
	}
	return false
}

func filterTransitiveHits(p auth.Principal, members []transitiveMember, hits []aql.Hit) []aql.Hit {
	byKey := make(map[string]transitiveMember, len(members))
	for _, m := range members {
		byKey[m.config.Key] = m
	}
	out := hits[:0]
	for _, hit := range hits {
		m, ok := byKey[hit.Item.RepoKey]
		if ok && transitiveVisible(p, m, hit.Item) {
			out = append(out, hit)
		}
	}
	return out
}

func deduplicateTransitive(hits []aql.Hit) []aql.Hit {
	seen := make(map[string]bool)
	out := hits[:0]
	for _, hit := range hits {
		key := hit.Item.RepoKey + "/" + hit.Item.FullPath()
		if !seen[key] {
			seen[key] = true
			out = append(out, hit)
		}
	}
	return out
}

// Walk configured virtual membership in declaration order, retaining alternative
// routes for pattern checks. Only the first five distinct remote members search.
func transitiveMembers(configs map[string]repo.Config, key string) ([]transitiveMember, error) {
	var out []transitiveMember
	indexes := map[string]int{}
	remoteCount, nodes := 0, 0
	var visit func(string, []repo.Config, map[string]bool) error
	visit = func(key string, route []repo.Config, stack map[string]bool) error {
		nodes++
		if nodes > 256 || len(route) > 8 || stack[key] {
			return aql.ErrInvalid
		}
		c, ok := configs[key]
		if !ok {
			return aql.ErrInvalid
		}
		if c.Class == repo.Virtual {
			stack[key] = true
			defer delete(stack, key)
			for _, child := range c.Repositories {
				if err := visit(child, append(slices.Clone(route), c), stack); err != nil {
					return err
				}
			}
			return nil
		}
		if i, ok := indexes[key]; ok {
			out[i].routes = append(out[i].routes, slices.Clone(route))
			return nil
		}
		if c.Class == repo.Remote {
			remoteCount++
		}
		indexes[key] = len(out)
		out = append(
			out,
			transitiveMember{
				config:    c,
				routes:    [][]repo.Config{slices.Clone(route)},
				canSearch: c.Class == repo.Remote && remoteCount <= 5,
			},
		)
		return nil
	}
	if err := visit(key, nil, map[string]bool{}); err != nil {
		return nil, err
	}
	return out, nil
}

func transitiveError(err error) error {
	switch {
	case errors.Is(err, aql.ErrInvalid),
		errors.Is(err, aql.ErrTooLong),
		errors.Is(err, aql.ErrSyntax),
		errors.Is(err, aql.ErrRestricted),
		errors.Is(err, aql.ErrUnsupported),
		errors.Is(err, source.ErrInvalidRequest),
		errors.Is(err, source.ErrUnsupported):
		return badRequestf("unsupported transitive AQL query or source")
	case errors.Is(err, source.ErrDenied):
		return apierror.New(http.StatusForbidden, "transitive AQL access denied")
	case errors.Is(err, context.DeadlineExceeded):
		return apierror.New(http.StatusRequestTimeout, "transitive AQL timed out")
	default:
		return apierror.New(http.StatusBadGateway, "transitive AQL source unavailable")
	}
}

func (s artifactoryService) transitiveCollect(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	members []transitiveMember,
	original *aql.Query,
	limit int,
) ([]aql.Hit, []transitiveResult, error) {
	var fences []transitiveResult
	var hits []aql.Hit
	for _, member := range members {
		if member.config.Class != repo.Remote || !member.canSearch {
			continue
		}
		if !p.Access().
			AllowsFolder(types.ResourceArtifact, target(member.config), "", types.ActionRead) {
			continue
		}
		if member.config.ContentSynchronisation == nil ||
			!member.config.ContentSynchronisation.Enabled {
			return nil, nil, aql.ErrInvalid
		}
		found, result, e := s.transitivePeer(ctx, p, member.config, original, limit)
		if e != nil {
			return nil, nil, fmt.Errorf("transitive peer: %w", e)
		}
		kept := found[:0]
		for _, hit := range found {
			if transitiveVisible(p, member, hit.Item) {
				kept = append(kept, hit)
			}
		}
		paths := make([]types.Item, 0, len(kept))
		for _, hit := range kept {
			if !p.Admin && !hit.Item.Folder {
				if e = result.CheckRead(ctx, hit.Item.FullPath()); errors.Is(e, source.ErrDenied) {
					continue
				} else if e != nil {
					return nil, nil, fmt.Errorf("transitive peer: %w", e)
				}
			}
			// Actual cached bytes take precedence even if their current metadata
			// did not match the query. Advertising peer checksums for that path
			// would make an ordinary remote download return a different object.
			if _, e = st.Items().Get(ctx, member.config.Key, true, hit.Item.FullPath()); e == nil {
				continue
			} else if !errors.Is(e, store.ErrNotFound) {
				return nil, nil, fmt.Errorf("transitive peer: %w", e)
			}
			paths = append(paths, hit.Item)
			hits = append(hits, hit)
		}
		fences = append(
			fences,
			transitiveResult{result: result, paths: paths, isTruncated: len(found) >= limit},
		)
	}
	return hits, fences, nil
}

func checkTransitivePaths(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	fence transitiveResult,
) error {
	for _, it := range fence.paths {
		if !p.Admin && !it.Folder {
			if err := fence.result.CheckRead(ctx, it.FullPath()); err != nil {
				return fmt.Errorf("transitive delivery permission: %w", err)
			}
		}
		if _, err := st.Items().Get(ctx, it.RepoKey, true, it.FullPath()); err == nil {
			return source.ErrSuperseded
		} else if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("transitive cache precedence: %w", err)
		}
	}
	return nil
}

func (s artifactoryService) fenceTransitive(
	ctx context.Context,
	st store.Store,
	p auth.Principal,
	configs map[string]repo.Config,
	members []transitiveMember,
	fences []transitiveResult,
	hits []aql.Hit,
) error {
	latest, err := repoConfigs(ctx, st)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(configs, latest) {
		return source.ErrSuperseded
	}
	for _, fence := range fences {
		if err = fence.result.Revalidate(ctx); err != nil {
			return fmt.Errorf("transitive source fence: %w", err)
		}
		if err = checkTransitivePaths(ctx, st, p, fence); err != nil {
			return err
		}
	}
	current, err := s.transitivePrincipal(ctx, st, p)
	if err != nil {
		return err
	}
	// Use the new authority snapshot for every delivered cached/local/remote row,
	// including folders reached through narrow descendant grants.
	for _, hit := range hits {
		for _, member := range members {
			if member.config.Key == hit.Item.RepoKey &&
				!transitiveVisible(current, member, hit.Item) {
				return source.ErrDenied
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("transitive delivery: %w", err)
	}
	return nil
}
