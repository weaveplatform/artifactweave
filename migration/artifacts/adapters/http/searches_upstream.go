package artifactshttp

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The listing budget applies across repositories and folders. Metadata has a
// separate bound because an exact Maven search needs no directory listings.
const upstreamMetadataLimit = 100

// Routes is the route and disclosure policy the adapter applies to an
// upstream search: which remote repositories it asks, under which key a
// virtual route may name what they hold, what the caller may read, and the
// fences that recheck both before the search answers.
type Routes interface {
	// Keys are the remote repositories the search asks, sorted.
	Keys() []string
	// Request is the source request for the remote repository key, with
	// the caller's identity and access.
	Request(key string) source.Request
	// RouteKey is the key, as the search's repos parameter named it, under
	// which the search may name full, a file or, with folder, a folder of
	// the repository key, cached saying whether it is the remote's cache;
	// empty when no route may name it.
	RouteKey(key, full string, folder bool, cached ...bool) string
	// ScopeAllowed reports whether the caller's request may read scope of
	// key and a route may name its folder.
	ScopeAllowed(key string, scope remote.DiscoveryScope) bool
	// Allowed reports whether a route may name full in key, the
	// repository's include and exclude patterns accept it and the caller
	// may read it, without recording it as disclosed.
	Allowed(key, full string, folder bool) bool
	// Disclose is Allowed for a path that cached says may be the remote's
	// cache, and records each path it allows for CheckDisclosures.
	Disclose(key, full string, folder bool, cached ...bool) bool
	// CheckRoutes fails when the routes could not be built or have changed
	// since the search captured them.
	CheckRoutes(ctx context.Context) error
	// CheckDisclosures fails when the caller may no longer read a path the
	// search disclosed, or under the route that named it.
	CheckDisclosures(ctx context.Context) error
}

// UpstreamDeps is what the adapter supplies to an upstream search.
type UpstreamDeps struct {
	// Sources reads the remote repositories' upstreams.
	Sources *source.Service
	// Items reads the repository layouts that a remote's discovery maps
	// between.
	Items *item.Service
	// Routes is the adapter's route and disclosure policy.
	Routes Routes
	// Fail turns an upstream failure that the search does not pass over
	// into the adapter's answer.
	Fail func(err error) error
}

// UpstreamSearch is the upstream part of a layout search (decision 0012,
// docs/operations/upstream-search.md): what the remote repositories'
// upstreams hold of a module beyond their caches, read from authoritative
// Maven metadata or bounded folder listings. It is not safe for concurrent
// use: it counts its reads and records the checks Revalidate repeats.
type UpstreamSearch struct {
	r        *search.SearchRun
	ls       search.LayoutScope
	g, a     string
	budget   remote.ListingBudget
	reads    int
	entries  int
	checks   []func(context.Context) error
	mappings map[string]*remote.Discovery
	sources  *source.Service
	items    *item.Service
	routes   Routes
	fail     func(err error) error
}

// NewUpstreamSearch starts the upstream part of r for the organisation g and
// the module a, over the layouts of ls, through d.
func NewUpstreamSearch(
	r *search.SearchRun,
	ls search.LayoutScope,
	g, a *string,
	d UpstreamDeps,
) *UpstreamSearch {
	return &UpstreamSearch{
		r: r, ls: ls, g: httpkit.Deref(g), a: httpkit.Deref(a),
		sources: d.Sources, items: d.Items, routes: d.Routes, fail: d.Fail,
	}
}

// Revalidate rechecks, before the search answers, every upstream read and
// listing it relied on, then the routes and the disclosures. A nil u, a
// search without an upstream part, has nothing to recheck. Its error is
// UpstreamDeps.Fail's answer.
func (u *UpstreamSearch) Revalidate(ctx context.Context) error {
	if u == nil {
		return nil
	}
	for _, check := range u.checks {
		if err := check(ctx); err != nil {
			return u.fail(err)
		}
	}
	if err := u.routes.CheckRoutes(ctx); err != nil {
		return u.fail(err)
	}
	if err := u.routes.CheckDisclosures(ctx); err != nil {
		return u.fail(err)
	}
	return nil
}

// Only availability failures permit a partial result. Auth, integrity, SSRF,
// changed source and unsupported protocol errors must not become empty success.
func (u *UpstreamSearch) failure(key string, err error) error {
	if err == nil || errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	c := u.r.Configs[key]
	var failure *source.HTTPError
	if (c.HardFail == nil || !*c.HardFail) && errors.As(err, &failure) &&
		(failure.Kind == source.FailureKindConnectivity || failure.Kind == source.FailureKindTimeout ||
			failure.Kind == source.FailureKindHTTP && (failure.Status == 429 || failure.Status >= 500)) {
		return nil
	}
	return u.fail(err)
}

// Versions lists the versions the upstreams hold of the module, from Maven
// metadata where the upstream layout is Maven's and from bounded listings
// where the repository lists remote folders, as search.ModuleVersions counts
// them from v and files. A repository that does not hard-fail leaves its
// versions out when its upstream is unavailable; any other failure is
// UpstreamDeps.Fail's answer.
func (u *UpstreamSearch) Versions(
	ctx context.Context,
	v string,
	files bool,
) ([]search.VersionEntry, error) {
	if err := u.routes.CheckRoutes(ctx); err != nil {
		return nil, u.failure("", err)
	}
	var out []search.VersionEntry
	for _, key := range u.routes.Keys() {
		if _, err := u.mapping(ctx, key); err != nil {
			return nil, u.failure(key, err)
		}
		var found []search.VersionEntry
		var err error
		if maven.IsMavenLayout(u.mappings[key].Upstream()) {
			found, err = u.mavenVersions(ctx, key, v, files)
		} else if c := u.r.Configs[key]; c.ListRemoteFolderItems.Bool() {
			var hits []search.LayoutHit
			hits, err = u.listFiles(ctx, key)
			for _, hit := range hits {
				if v != "" && !search.HasWildcard(v) && (!hit.Integration() || hit.Version() != v) {
					continue
				}
				version := hit.Version()
				if hit.Integration() && (files || v != "" && !search.HasWildcard(v)) {
					version = hit.FileVersion()
				}
				found = append(
					found,
					search.VersionEntry{V: version, IsIntegration: hit.Integration()},
				)
			}
		}
		if err != nil {
			if failure := u.failure(key, err); failure != nil {
				return nil, failure
			}
			continue
		}
		out = append(out, found...)
	}
	return out, nil
}

// Gavc lists the files the upstreams' bounded listings show of the module
// whose version and classifier match v and c, each with the coordinates its
// repository's layout reads; failures are passed over or answered as
// Versions does.
func (u *UpstreamSearch) Gavc(ctx context.Context, v, c *string) ([]search.LayoutHit, error) {
	if err := u.routes.CheckRoutes(ctx); err != nil {
		return nil, u.failure("", err)
	}
	var out []search.LayoutHit
	for _, key := range u.routes.Keys() {
		if _, err := u.mapping(ctx, key); err != nil {
			return nil, u.failure(key, err)
		}
		found, err := u.listFiles(ctx, key)
		if err != nil {
			if failure := u.failure(key, err); failure != nil {
				return nil, failure
			}
			continue
		}
		vp, cp := search.Wildcard(v), search.Wildcard(c)
		for _, hit := range found {
			if search.Matches(vp, hit.Version()) && search.Matches(cp, hit.Classifier) {
				out = append(out, hit)
			}
		}
	}
	return out, nil
}

// Disclose reports whether the search may name full, as Routes.Disclose
// decides and records it, which makes an UpstreamSearch a search.Upstream.
func (u *UpstreamSearch) Disclose(key, full string, folder bool, cached ...bool) bool {
	return u.routes.Disclose(key, full, folder, cached...)
}

func (u *UpstreamSearch) mapping(ctx context.Context, key string) (*remote.Discovery, error) {
	if d := u.mappings[key]; d != nil {
		return d, nil
	}
	d, err := remote.NewDiscovery(ctx, u.items, u.r.Configs[key])
	if err != nil {
		return nil, fmt.Errorf("prepare search mapping: %w", err)
	}
	if d.Canonical().Definition() != u.ls.Layouts[key].Definition() {
		return nil, source.ErrSuperseded
	}
	if u.mappings == nil {
		u.mappings = map[string]*remote.Discovery{}
	}
	u.mappings[key] = d
	u.checks = append(u.checks, d.Revalidate)
	return d, nil
}

func searchValues(g, a string) layout.Values {
	values := layout.Values{}
	if g != "" && !search.HasWildcard(g) {
		values[layout.TokenOrg] = g
	}
	if a != "" && !search.HasWildcard(a) {
		values[layout.TokenModule] = a
	}
	return values
}

// Prefixes may stop in the middle of a filename or contain coordinate globs.
// Start at the last complete literal folder and let layout.Read decide matches.
func searchFolders(l *layout.Layout, g, a string) []string {
	values := layout.Values{layout.TokenOrg: g, layout.TokenModule: a}
	var out []string
	for _, prefix := range l.Prefixes(values) {
		if i := strings.IndexAny(prefix, "*?"); i >= 0 {
			prefix = prefix[:i]
		}
		folder := ""
		if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
			folder = prefix[:i]
		}
		if !slices.Contains(out, folder) {
			out = append(out, folder)
		}
	}
	return out
}

func (u *UpstreamSearch) walk(
	ctx context.Context, key string, documents bool, consume func(string) (bool, error),
) error {
	d, err := u.mapping(ctx, key)
	if err != nil {
		return err
	}
	for _, scope := range d.Scopes(searchValues(u.g, u.a)) {
		if !u.routes.ScopeAllowed(key, scope) {
			continue
		}
		if err = u.walkScope(ctx, key, d, scope, documents, consume); err != nil {
			return err
		}
	}
	return nil
}

func (u *UpstreamSearch) walkScope(
	ctx context.Context,
	key string,
	d *remote.Discovery,
	scope remote.DiscoveryScope,
	documents bool,
	consume func(string) (bool, error),
) error {
	queue := searchFolders(d.Upstream(), u.g, u.a)
	seen := map[string]bool{}
	for len(queue) > 0 {
		folder := queue[0]
		queue = queue[1:]
		if seen[folder] {
			continue
		}
		seen[folder] = true
		if strings.Count(folder, "/") >= 32 {
			return &source.HTTPError{Status: 502, Kind: source.FailureKindProtocol}
		}
		if err := u.routes.CheckRoutes(ctx); err != nil {
			//nolint:wrapcheck // the routes' fence errors, which failure classifies as they are
			return err
		}
		listing, err := remote.ListMappedFolder(
			ctx,
			u.sources,
			u.routes.Request(key),
			d,
			scope,
			folder,
			&u.budget,
		)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("search mapped upstream folder: %w", err)
		}
		u.checks = append(u.checks, listing.Revalidate)
		var paths []string
		if documents {
			paths = listing.Documents
		} else {
			for _, entry := range listing.Files {
				paths = append(paths, entry.Path)
			}
		}
		stop := false
		for _, full := range paths {
			matched, err := consume(full)
			if err != nil {
				return err
			}
			stop = stop || matched
		}
		if !stop {
			queue = append(queue, listing.Folders...)
		}
	}
	return nil
}

func (u *UpstreamSearch) listFiles(ctx context.Context, key string) ([]search.LayoutHit, error) {
	var out []search.LayoutHit
	gp, ap := search.Wildcard(&u.g), search.Wildcard(&u.a)
	err := u.walk(ctx, key, false, func(full string) (bool, error) {
		co, ok := u.ls.Layouts[key].Read(full)
		if ok && search.Matches(gp, co.Org) && search.Matches(ap, co.Module) &&
			u.Disclose(key, full, false) {
			dir, name := path.Split(full)
			out = append(out, search.LayoutHit{
				Hit: aql.Hit{
					Item: types.Item{RepoKey: key, Path: strings.TrimSuffix(dir, "/"), Name: name},
				},
				Coordinates: co,
			})
		}
		return false, nil
	})
	return out, err
}
