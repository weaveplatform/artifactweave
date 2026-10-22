package server

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The listing budget applies across repositories and folders. Metadata has a
// separate bound because an exact Maven search needs no directory listings.
const upstreamMetadataLimit = 100

type upstreamSearch struct {
	r           *searchRun
	ls          layoutScope
	g, a        string
	keys        []string
	budget      remote.ListingBudget
	reads       int
	entries     int
	checks      []func(context.Context) error
	mappings    map[string]*remote.Discovery
	routes      map[string][]searchRoute
	virtuals    map[string]repo.Config
	routeErr    error
	disclosures []searchDisclosure
}

func (r *searchRun) upstreamSearch(
	ls layoutScope,
	repos, g, a *string,
	enabled bool,
) *upstreamSearch {
	if !enabled || r.s.cfg.RemoteSources == nil {
		return nil
	}
	u := &upstreamSearch{r: r, ls: ls, g: deref(g), a: deref(a)}
	u.prepareRoutes(repos)
	return u
}

func (u *upstreamSearch) request(key string) source.Request {
	c := u.r.configs[key]
	return source.Request{
		Repo:    c,
		Subject: u.r.p.Subject(),
		Access:  u.r.acc.d,
		Project: c.ProjectKey,
	}
}

func (u *upstreamSearch) revalidate(ctx context.Context) error {
	if u == nil {
		return nil
	}
	for _, check := range u.checks {
		if err := check(ctx); err != nil {
			//nolint:wrapcheck // typed HTTP response
			return pkgroute.SourceError(
				err,
				u.r.p.Anonymous,
			)
		}
	}
	if err := u.checkRoutes(ctx); err != nil {
		return pkgroute.SourceError(err, u.r.p.Anonymous) //nolint:wrapcheck // typed HTTP response
	}
	if err := u.checkDisclosures(ctx); err != nil {
		return pkgroute.SourceError(err, u.r.p.Anonymous) //nolint:wrapcheck // typed HTTP response
	}
	return nil
}

// Only availability failures permit a partial result. Auth, integrity, SSRF,
// changed source and unsupported protocol errors must not become empty success.
func (u *upstreamSearch) failure(key string, err error) error {
	if err == nil || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	c := u.r.configs[key]
	var failure *source.HTTPError
	if (c.HardFail == nil || !*c.HardFail) && errors.As(err, &failure) &&
		(failure.Kind == source.ConnectivityFailure || failure.Kind == source.TimeoutFailure ||
			failure.Kind == source.HTTPFailure && (failure.Status == 429 || failure.Status >= 500)) {
		return nil
	}
	return pkgroute.SourceError(err, u.r.p.Anonymous) //nolint:wrapcheck // typed HTTP response
}

func (u *upstreamSearch) versions(
	ctx context.Context,
	v string,
	files bool,
) ([]versionEntry, error) {
	if err := u.checkRoutes(ctx); err != nil {
		return nil, u.failure("", err)
	}
	var out []versionEntry
	for _, key := range u.keys {
		if _, err := u.mapping(ctx, key); err != nil {
			return nil, u.failure(key, err)
		}
		var found []versionEntry
		var err error
		if maven.IsMavenLayout(u.mappings[key].Upstream()) {
			found, err = u.mavenVersions(ctx, key, v, files)
		} else if c := u.r.configs[key]; c.ListRemoteFolderItems.Bool() {
			var hits []layoutHit
			hits, err = u.listFiles(ctx, key)
			for _, hit := range hits {
				if v != "" && !hasWildcard(v) && (!hit.Integration() || hit.Version() != v) {
					continue
				}
				version := hit.Version()
				if hit.Integration() && (files || v != "" && !hasWildcard(v)) {
					version = hit.FileVersion()
				}
				found = append(found, versionEntry{version, hit.Integration()})
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

func (u *upstreamSearch) gavc(ctx context.Context, v, c *string) ([]layoutHit, error) {
	if err := u.checkRoutes(ctx); err != nil {
		return nil, u.failure("", err)
	}
	var out []layoutHit
	for _, key := range u.keys {
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
		vp, cp := wildcard(v), wildcard(c)
		for _, hit := range found {
			if matches(vp, hit.Version()) && matches(cp, hit.Classifier) {
				out = append(out, hit)
			}
		}
	}
	return out, nil
}

func (u *upstreamSearch) mapping(ctx context.Context, key string) (*remote.Discovery, error) {
	if d := u.mappings[key]; d != nil {
		return d, nil
	}
	d, err := remote.NewDiscovery(ctx, &item.Service{Store: u.r.st}, u.r.configs[key])
	if err != nil {
		return nil, fmt.Errorf("prepare search mapping: %w", err)
	}
	if d.Canonical().Definition() != u.ls.layouts[key].Definition() {
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
	if g != "" && !hasWildcard(g) {
		values[layout.TokenOrg] = g
	}
	if a != "" && !hasWildcard(a) {
		values[layout.TokenModule] = a
	}
	return values
}

func (u *upstreamSearch) allowed(key, full string, folder bool) bool {
	c := u.r.configs[key]
	return u.routeKey(key, full, folder) != "" && authz.PatternsOf(c).Accepts(full, folder) &&
		u.r.acc.item(c, full, folder, types.ActionRead)
}

// Current JFrog documents specific=true for remote GAVC, while older callers
// send remote=0/1. Keep that explicit override without synthesizing storage
// records for bytes that have never been downloaded.
func gavcRemote(ctx context.Context, specific bool, g, a *string) (bool, error) {
	query, err := rawQuery(rawSearchQuery(ctx))
	if err != nil {
		return false, err
	}
	remote := query.Get("remote")
	if remote != "" && remote != "0" && remote != "1" {
		return false, badRequest("remote must be 0 or 1")
	}
	if remote == "1" && !specific {
		return false, badRequest("remote GAVC search requires specific=true")
	}
	enabled := specific && remote != "0"
	if enabled && (deref(g) == "" || deref(a) == "" || hasWildcard(deref(g)+deref(a))) {
		if remote == "1" {
			return false, badRequest("remote GAVC search requires exact g and a")
		}
		return false, nil
	}
	return enabled, nil
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

func (u *upstreamSearch) walk(
	ctx context.Context, key string, documents bool, consume func(string) (bool, error),
) error {
	d, err := u.mapping(ctx, key)
	if err != nil {
		return err
	}
	for _, scope := range d.Scopes(searchValues(u.g, u.a)) {
		if !u.scopeAllowed(key, scope) {
			continue
		}
		if err = u.walkScope(ctx, key, d, scope, documents, consume); err != nil {
			return err
		}
	}
	return nil
}

func (u *upstreamSearch) walkScope(
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
			return &source.HTTPError{Status: 502, Kind: source.ProtocolFailure}
		}
		if err := u.checkRoutes(ctx); err != nil {
			return err
		}
		listing, err := remote.ListMappedFolder(
			ctx,
			u.r.s.cfg.RemoteSources,
			u.request(key),
			d,
			scope,
			folder,
			&u.budget,
		)
		if errors.Is(err, store.ErrNotFound) {
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

func (u *upstreamSearch) listFiles(ctx context.Context, key string) ([]layoutHit, error) {
	var out []layoutHit
	gp, ap := wildcard(&u.g), wildcard(&u.a)
	err := u.walk(ctx, key, false, func(full string) (bool, error) {
		co, ok := u.ls.layouts[key].Read(full)
		if ok && matches(gp, co.Org) && matches(ap, co.Module) && u.disclose(key, full, false) {
			dir, name := path.Split(full)
			out = append(out, layoutHit{
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
