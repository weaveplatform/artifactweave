package server

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
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

// A physical member may have several permitted routes. A global visited set
// would discard a later allowed route after encountering a restricted one.
func (u *upstreamSearch) prepareRoutes(requested *string) {
	u.routes = map[string][]searchRoute{}
	u.virtuals = map[string]repo.Config{}
	keys := strings.Split(deref(requested), ",")
	if requested == nil || strings.TrimSpace(*requested) == "" {
		keys = nil
		for key, c := range u.r.configs {
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

func (u *upstreamSearch) visitRoute(key string, route searchRoute, nodes *int) {
	*nodes++
	if *nodes > 1000 || len(route.virtuals) > 32 ||
		slices.ContainsFunc(route.virtuals, func(c repo.Config) bool { return c.Key == key }) {
		u.routeErr = invalidSearchMetadata()
		return
	}
	c, ok := u.r.configs[key]
	if !ok {
		// Explicit aliases retain cached authority and never initiate discovery.
		if base, alias := strings.CutSuffix(
			key,
			"-cache",
		); alias &&
			u.r.configs[base].Class == repo.Remote {
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
	if u.ls.layouts[key] == nil || !u.r.acc.visible(c) {
		return
	}
	u.routes[key] = append(u.routes[key], route)
	if c.Class == repo.Remote && !slices.Contains(u.keys, key) {
		u.keys = append(u.keys, key)
	}
}

func (u *upstreamSearch) routeKey(key, full string, folder bool, cached ...bool) string {
	for _, route := range u.routes[key] {
		if route.isCacheOnly && (len(cached) == 0 || !cached[0]) {
			continue
		}
		if !slices.ContainsFunc(route.virtuals, func(c repo.Config) bool {
			return c.BlackedOut != nil && *c.BlackedOut ||
				!authz.PatternsOf(c).Accepts(full, folder)
		}) {
			return route.name
		}
	}
	return ""
}

func (u *upstreamSearch) scopeAllowed(key string, scope remote.DiscoveryScope) bool {
	return scope.Allows(u.request(key)) && u.routeKey(key, scope.Path, true) != ""
}

func (u *upstreamSearch) disclose(key, full string, folder bool, cached ...bool) bool {
	c := u.r.configs[key]
	if u.routeKey(key, full, folder, cached...) == "" ||
		!authz.PatternsOf(c).
			Accepts(full, folder) ||
		!u.r.acc.item(c, full, folder, types.ActionRead) {
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

func (u *upstreamSearch) filterHits(hits []layoutHit) []layoutHit {
	out := hits[:0]
	for _, hit := range hits {
		if u.disclose(hit.Item.RepoKey, hit.Item.FullPath(), false, hit.Item.Cached) {
			out = append(out, hit)
		}
	}
	return out
}

// Virtual configuration has no source identity of its own. Fence the captured
// routes explicitly; otherwise removing a member while metadata is in flight
// would still return its files under the old virtual download URL.
func (u *upstreamSearch) checkRoutes(ctx context.Context) error {
	if u.routeErr != nil {
		return u.routeErr
	}
	if len(u.virtuals) == 0 {
		return nil
	}
	current, err := repoConfigs(ctx, u.r.st)
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

func (u *upstreamSearch) checkDisclosures(ctx context.Context) error {
	if len(u.disclosures) == 0 {
		return nil
	}
	if u.r.s.cfg.Auth == nil {
		return source.ErrDenied
	}
	current, err := u.r.s.cfg.Auth.Current(ctx, u.r.p, u.r.st.Tokens())
	if err != nil {
		return source.ErrDenied
	}
	current, err = auth.Authorize(
		ctx,
		authz.Sources{Permissions: u.r.st.Permissions(), Groups: u.r.st.Groups()},
		current,
	)
	if err != nil {
		return source.ErrDenied
	}
	for _, entry := range u.disclosures {
		allows := current.Access().Allows
		if entry.isFolder {
			allows = current.Access().AllowsFolder
		}
		if !allows(
			types.ResourceArtifact,
			target(u.r.configs[entry.key]),
			entry.path,
			types.ActionRead,
		) ||
			u.routeKey(entry.key, entry.path, entry.isFolder, entry.isCached) == "" {
			return source.ErrDenied
		}
	}
	return nil
}
