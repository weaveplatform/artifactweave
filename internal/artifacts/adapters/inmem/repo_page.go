package inmem

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// Page is the SQL store's Page over the in-memory table: the same filter,
// visibility, key order, window and count. The table is a map, so the
// matches are gathered and sorted before the window is cut, which the SQL
// store's index does instead.
func (r repos) Page(ctx context.Context, l port.RepoListing) (port.RepoPage, error) {
	var out port.RepoPage
	err := r.s.Do(ctx, func() error {
		all := r.t().All(func(_ string, c repo.Config) bool { return listed(l, c) }, compareRepos)
		if l.Count {
			out.Total = len(all)
		}
		if l.Limit > 0 {
			start := min(l.Offset, len(all))
			end := min(start+l.Limit, len(all))
			out.More = end < len(all)
			all = all[start:end]
		}
		for _, c := range all {
			out.Repos = append(out.Repos, port.RepoSummary{
				Key: c.Key, Class: c.Class, PackageType: c.PackageType, Description: c.Description,
			})
		}
		return nil
	})
	return out, err
}

// listed reports whether l lists c.
func listed(l port.RepoListing, c repo.Config) bool {
	if l.Class != "" && c.Class != l.Class ||
		l.ProjectKey != "" && c.ProjectKey != l.ProjectKey ||
		l.PackageTypes != nil && !slices.Contains(l.PackageTypes, c.PackageType) {
		return false
	}
	for _, s := range l.Visible {
		if !s.Every && !slices.Contains(s.Classes, c.Class) && !slices.Contains(s.Keys, c.Key) {
			return false
		}
	}
	return true
}
