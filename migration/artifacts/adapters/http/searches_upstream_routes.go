package artifactshttp

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
)

type searchRoute struct {
	name        string
	virtuals    []repo.Config
	isCacheOnly bool
}

type searchDisclosure struct {
	key, path          string
	isFolder, isCached bool
}

// searchRoutes is the route and disclosure policy of an upstream search
// (Routes): the routes from the keys the search named to the remote
// repositories it asks, through virtual repositories, and the paths it
// disclosed, which CheckDisclosures rechecks against the caller's current
// permissions.
type searchRoutes struct {
	r           *searchRun
	ls          search.LayoutScope
	keys        []string
	routes      map[string][]searchRoute
	virtuals    map[string]repo.Config
	routeErr    error
	disclosures []searchDisclosure
}

// upstreamSearch starts the upstream part of a layout search over ls, when
// enabled and the server reads remote sources, with its routes from the keys
// in repos; both are nil otherwise.
func (r *searchRun) upstreamSearch(
	ls search.LayoutScope,
	repos, g, a *string,
	enabled bool,
) (*search.UpstreamSearch, *searchRoutes) {
	if !enabled || r.s.cfg.RemoteSources == nil {
		return nil, nil
	}
	routes := &searchRoutes{r: r, ls: ls}
	routes.prepareRoutes(repos)
	return search.NewUpstreamSearch(r.SearchRun, ls, g, a, search.UpstreamDeps{
		Sources: r.s.cfg.RemoteSources,
		Items:   &item.Service{Store: r.st},
		Routes:  routes,
		Fail: func(err error) error {
			//nolint:wrapcheck // typed HTTP response
			return pkgroute.SourceError(err, r.Principal.Anonymous)
		},
	}), routes
}

// Keys are the remote repositories the routes reach, sorted.
func (u *searchRoutes) Keys() []string { return u.keys }

// Request is the source request for key, as the caller.
func (u *searchRoutes) Request(key string) source.Request {
	c := u.r.Configs[key]
	return source.Request{
		Repo:    c,
		Subject: accessapi.SubjectOf(u.r.Principal),
		Access:  u.r.acc.Decider,
		Project: c.ProjectKey,
	}
}

// Allowed reports whether a route may name full in key, its patterns accept
// it and the caller may read it.
func (u *searchRoutes) Allowed(key, full string, folder bool) bool {
	c := u.r.Configs[key]
	return u.RouteKey(key, full, folder) != "" && repo.PatternsOf(c).Accepts(full, folder) &&
		u.r.acc.Item(c, full, folder, accessapi.ActionRead)
}

// Current JFrog documents specific=true for remote GAVC, while older callers
// send remote=0/1. Keep that explicit override without synthesizing storage
// records for bytes that have never been downloaded.
func gavcRemote(ctx context.Context, specific bool, g, a *string) (bool, error) {
	query, err := rawQuery(httpkit.RawSearchQuery(ctx))
	if err != nil {
		return false, err
	}
	remote := query.Get("remote")
	if remote != "" && remote != "0" && remote != "1" {
		return false, httpkit.BadRequestf("remote must be 0 or 1")
	}
	if remote == "1" && !specific {
		return false, httpkit.BadRequestf("remote GAVC search requires specific=true")
	}
	enabled := specific && remote != "0"
	if enabled &&
		(httpkit.Deref(g) == "" || httpkit.Deref(a) == "" || search.HasWildcard(httpkit.Deref(g)+httpkit.Deref(a))) {
		if remote == "1" {
			return false, httpkit.BadRequestf("remote GAVC search requires exact g and a")
		}
		return false, nil
	}
	return enabled, nil
}

// A physical member may have several permitted routes. A global visited set
// would discard a later allowed route after encountering a restricted one.
func (u *searchRoutes) prepareRoutes(requested *string) {
	u.routes = map[string][]searchRoute{}
	u.virtuals = map[string]repo.Config{}
	keys := strings.Split(httpkit.Deref(requested), ",")
	if requested == nil || strings.TrimSpace(*requested) == "" {
		keys = nil
		for key, c := range u.r.Configs {
			if c.Class != repo.Virtual {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
	}
	nodes := 0
	for _, key := range keys {
		key = strings.TrimSpace(key)
		u.visitRoute(key, searchRoute{name: key}, &nodes)
	}
	slices.Sort(u.keys)
}

func (u *searchRoutes) visitRoute(key string, route searchRoute, nodes *int) {
	*nodes++
	if *nodes > 1000 || len(route.virtuals) > 32 ||
		slices.ContainsFunc(route.virtuals, func(c repo.Config) bool { return c.Key == key }) {
		u.routeErr = search.InvalidSearchMetadata()
		return
	}
	c, ok := u.r.Configs[key]
	if !ok {
		// Explicit aliases retain cached authority and never initiate discovery.
		if base, alias := strings.CutSuffix(
			key,
			"-cache",
		); alias &&
			u.r.Configs[base].Class == repo.Remote {
			route.isCacheOnly = true
			u.routes[base] = append(u.routes[base], route)
		}
		return
	}
	if c.Class == repo.Virtual {
		u.virtuals[key] = c
		route.virtuals = append(slices.Clone(route.virtuals), c)
		for _, child := range c.Repositories {
			u.visitRoute(child, route, nodes)
			if u.routeErr != nil {
				return
			}
		}
		return
	}
	if u.ls.Layouts[key] == nil || !u.r.acc.Visible(c) {
		return
	}
	u.routes[key] = append(u.routes[key], route)
	if c.Class == repo.Remote && !slices.Contains(u.keys, key) {
		u.keys = append(u.keys, key)
	}
}

// RouteKey is the name of the first route to key that may name full. A
// route through a -cache key names cached paths only, and a route through
// virtual repositories none that one of them blacks out or rejects by its
// patterns.
func (u *searchRoutes) RouteKey(key, full string, folder bool, cached ...bool) string {
	for _, route := range u.routes[key] {
		if route.isCacheOnly && (len(cached) == 0 || !cached[0]) {
			continue
		}
		if !slices.ContainsFunc(route.virtuals, func(c repo.Config) bool {
			return c.BlackedOut != nil && *c.BlackedOut ||
				!repo.PatternsOf(c).Accepts(full, folder)
		}) {
			return route.name
		}
	}
	return ""
}

// ScopeAllowed reports whether the caller's request may read scope and a
// route may name its folder.
func (u *searchRoutes) ScopeAllowed(key string, scope remote.DiscoveryScope) bool {
	return scope.Allows(u.Request(key)) && u.RouteKey(key, scope.Path, true) != ""
}

// Disclose reports whether a route may name full, the repository's patterns
// accept it and the caller may read it, and records it for
// CheckDisclosures when they do.
func (u *searchRoutes) Disclose(key, full string, folder bool, cached ...bool) bool {
	c := u.r.Configs[key]
	if u.RouteKey(key, full, folder, cached...) == "" ||
		!repo.PatternsOf(c).
			Accepts(full, folder) ||
		!u.r.acc.Item(c, full, folder, accessapi.ActionRead) {
		return false
	}
	u.disclosures = append(
		u.disclosures,
		searchDisclosure{
			key:      key,
			path:     full,
			isFolder: folder,
			isCached: len(cached) > 0 && cached[0],
		},
	)
	return true
}

func (u *searchRoutes) filterHits(hits []search.LayoutHit) []search.LayoutHit {
	out := hits[:0]
	for _, hit := range hits {
		if u.Disclose(hit.Item.RepoKey, hit.Item.FullPath(), false, hit.Item.Cached) {
			out = append(out, hit)
		}
	}
	return out
}

// Virtual configuration has no source identity of its own. Fence the captured
// routes explicitly; otherwise removing a member while metadata is in flight
// would still return its files under the old virtual download URL.
func (u *searchRoutes) CheckRoutes(ctx context.Context) error {
	if u.routeErr != nil {
		return u.routeErr
	}
	if len(u.virtuals) == 0 {
		return nil
	}
	current, err := search.RepoConfigs(ctx, u.r.st)
	if err != nil {
		return fmt.Errorf("read search routes: %w", err)
	}
	for key, original := range u.virtuals {
		if !reflect.DeepEqual(original, current[key]) {
			return source.ErrSuperseded
		}
	}
	return nil
}

// CheckDisclosures fails with source.ErrDenied when the caller, as the
// authenticator reads them now, may no longer read a path the search
// disclosed, or no route names it any more; without an authenticator it
// cannot tell, so it fails too.
func (u *searchRoutes) CheckDisclosures(ctx context.Context) error {
	if len(u.disclosures) == 0 {
		return nil
	}
	api := u.r.s.cfg.Access
	if api.Authenticator == nil {
		return source.ErrDenied
	}
	current, err := api.Authenticator.Current(ctx, u.r.Principal)
	if err != nil {
		return source.ErrDenied
	}
	_, d, err := api.Authorizer.Authorize(ctx, current, false)
	if err != nil {
		return source.ErrDenied
	}
	for _, entry := range u.disclosures {
		allows := d.Allows
		if entry.isFolder {
			allows = d.AllowsFolder
		}
		if !allows(
			accessapi.ResourceArtifact,
			itemkit.Target(u.r.Configs[entry.key]),
			entry.path,
			accessapi.ActionRead,
		) ||
			u.RouteKey(entry.key, entry.path, entry.isFolder, entry.isCached) == "" {
			return source.ErrDenied
		}
	}
	return nil
}
