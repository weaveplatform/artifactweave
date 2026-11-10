package search

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// Upstream is the remote part of a module search: what a remote
// repository's upstream lists beyond its cache. A nil Upstream reads stored
// files only.
type Upstream interface {
	// Disclose reports whether the search may name fullPath, a file or,
	// with folder, a folder of the repository key, cached saying whether it
	// is the remote's cache, and records each path it allows so that the
	// adapter can recheck it before it answers.
	Disclose(key, fullPath string, folder bool, cached ...bool) bool
	// Versions lists the versions the upstreams hold of the module, as
	// ModuleVersions counts them from v and files. Its error is the
	// adapter's answer to an upstream that failed.
	Versions(ctx context.Context, v string, files bool) ([]VersionEntry, error)
}

// ModuleSearch checks a versions or latest version search's parameters: a
// group or a module, and remote 0 or 1.
func ModuleSearch[R ~int](g, a *string, remote *R) error {
	if deref(g) == "" && deref(a) == "" {
		return apierror.New(http.StatusBadRequest, "name the group (g), the module (a) or both")
	}
	if remote != nil && *remote != 0 && *remote != 1 {
		return apierror.New(http.StatusBadRequest, "remote must be 0 or 1")
	}
	return nil
}

// VersionEntry is one version a module search lists.
type VersionEntry struct {
	// V is the version as the search lists it.
	V string
	// IsIntegration reports whether V is an integration version, such as a
	// Maven snapshot.
	IsIntegration bool
}

// HasWildcard reports whether a v parameter is a pattern, which filters the
// listed versions, rather than the module version whose integration
// versions to list.
func HasWildcard(v string) bool { return strings.ContainsAny(v, "*?") }

// ModuleVersions lists, each once and newest first in Mercury order, the
// versions of the files in ls whose organisation and module match g and a.
// A v without wildcards lists only the integration file versions of the
// module version v, as 1.0-20260101.120000-1 is of 1.0-SNAPSHOT. Otherwise
// every version counts: an integration one by its file version when files
// is set, else by its module version, which groups a folder's unique
// snapshots as 1.0-SNAPSHOT. A v with wildcards is the caller's to apply.
func ModuleVersions(
	ctx context.Context,
	r *SearchRun,
	ls LayoutScope,
	g, a *string,
	v string,
	files bool,
	upstream Upstream,
) ([]VersionEntry, error) {
	exact := v != "" && !HasWildcard(v)
	gp, ap := Wildcard(g), Wildcard(a)
	found, err := ls.Find(ctx, r, nil, r.Capped(0), func(co layout.Coordinates) bool {
		return Matches(gp, co.Org) && Matches(ap, co.Module) &&
			(!exact || co.Integration() && co.Version() == v)
	})
	if err != nil {
		return nil, err
	}
	var out []VersionEntry
	seen := map[string]bool{}
	for _, f := range found {
		if upstream != nil &&
			!upstream.Disclose(f.Item.RepoKey, f.Item.FullPath(), false, f.Item.Cached) {
			continue
		}
		ver := f.Version()
		if f.Integration() && (files || exact) {
			ver = f.FileVersion()
		}
		if !seen[ver] {
			seen[ver] = true
			out = append(out, VersionEntry{ver, f.Integration()})
		}
	}
	if upstream != nil {
		remote, err := upstream.Versions(ctx, v, files)
		if err != nil {
			//nolint:wrapcheck // the adapter's answer to a failed upstream, which it renders as it is
			return nil, err
		}
		for _, entry := range remote {
			if !seen[entry.V] {
				seen[entry.V] = true
				out = append(out, entry)
			}
		}
	}
	slices.SortStableFunc(
		out,
		func(x, y VersionEntry) int { return model.CompareMercury(y.V, x.V) },
	)
	if limit := r.Capped(0); upstream != nil && limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ModuleScope resolves a versions or latest version search's repositories,
// virtual keys searching their members, to the layouts that read g and a.
func (r *SearchRun) ModuleScope(ctx context.Context, repos, g, a *string) (LayoutScope, error) {
	scope, _ := r.RepoScope(repos, true)
	return r.LayoutScope(ctx, scope, g, a)
}
