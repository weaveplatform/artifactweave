package inmem

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var (
	_ port.RepositoryIndexPages = repos{}
	_ port.PackageIndexPages    = packages{}
)

// indexFirst retains only the requested page plus its continuation witness;
// table iteration must not clone/sort every version's opaque metadata.
func indexFirst[T any](rows []T, value T, limit int, compare func(T, T) int) []T {
	at, _ := slices.BinarySearchFunc(rows, value, compare)
	if at >= limit {
		return rows
	}
	rows = slices.Insert(rows, at, value)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (r repos) IndexRepository(ctx context.Context, key string) (api.IndexRepository, error) {
	var out api.IndexRepository
	err := r.s.Do(ctx, func() error {
		value, ok := r.t().Get(key)
		if !ok {
			return kernel.ErrNotFound
		}
		out = api.IndexRepository{
			Key: value.Key, Project: value.ProjectKey, Class: value.Class,
			PackageType: value.PackageType, Environments: slices.Clone(value.Environments),
		}
		return nil
	})
	return out, err
}

func indexPatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		quoted := regexp.QuoteMeta(pattern)
		quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
		quoted = strings.ReplaceAll(quoted, `\?`, `.`)
		out = append(out, regexp.MustCompile("^(?:"+quoted+")$"))
	}
	return out
}

func indexMatches(patterns []*regexp.Regexp, value string) bool {
	return slices.ContainsFunc(patterns, func(pattern *regexp.Regexp) bool { return pattern.MatchString(value) })
}

func (r repos) IndexRepositories(ctx context.Context, query api.IndexRepositoryQuery, page kernel.Page) ([]api.IndexRepository, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index repositories page: %w", err)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("index repositories cursor: %w", err)
	}
	include, exclude := indexPatterns(query.Repositories), indexPatterns(query.ExcludedRepositories)
	var out []api.IndexRepository
	err = r.s.Do(ctx, func() error {
		for _, value := range r.t().Rows {
			if value.Class != repo.Local && value.Class != repo.Remote || !slices.Contains(query.PackageTypes, value.PackageType) ||
				query.Class != "" && query.Class != value.Class ||
				!query.AllProjects && !slices.Contains(query.Projects, value.ProjectKey) ||
				len(after) > 0 && value.Key <= after[0] || !indexMatches(include, value.Key) || indexMatches(exclude, value.Key) {
				continue
			}
			if query.Environments != nil && !slices.ContainsFunc(value.Environments, func(environment string) bool { return slices.Contains(query.Environments, environment) }) {
				continue
			}
			entry := api.IndexRepository{
				Key: value.Key, Project: value.ProjectKey, Class: value.Class,
				PackageType: value.PackageType, Environments: slices.Clone(value.Environments),
			}
			out = indexFirst(out, entry, page.Limit+1, func(a, b api.IndexRepository) int { return strings.Compare(a.Key, b.Key) })
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexRepository) []string { return []string{entry.Key} })
	return out, next, nil
}

func (p packages) indexRepositoryCurrent(ref api.IndexRepository) bool {
	value, ok := (repos{p.s}).t().Get(ref.Key)
	return ok && value.Class == ref.Class && value.ProjectKey == ref.Project && value.PackageType == ref.PackageType
}

func (p packages) indexPackageCurrent(ref api.IndexPackage) bool {
	if !p.indexRepositoryCurrent(ref.Repository) {
		return false
	}
	value, ok := p.t().Get(ref.ID)
	return ok && value.RepoKey == ref.Repository.Key && value.Type == ref.Repository.PackageType && value.Name == ref.Name
}

func (p packages) IndexPackages(ctx context.Context, ref api.IndexRepository, page kernel.Page) ([]api.IndexPackage, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index packages page: %w", err)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("index packages cursor: %w", err)
	}
	var out []api.IndexPackage
	err = p.s.Do(ctx, func() error {
		if !p.indexRepositoryCurrent(ref) {
			return nil
		}
		for _, value := range p.t().Rows {
			if value.RepoKey != ref.Key || value.Type != ref.PackageType || len(after) > 0 && value.Name <= after[0] {
				continue
			}
			entry := api.IndexPackage{Repository: kernel.Clone(ref), ID: value.ID, Name: value.Name}
			out = indexFirst(out, entry, page.Limit+1, func(a, b api.IndexPackage) int { return strings.Compare(a.Name, b.Name) })
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexPackage) []string { return []string{entry.Name} })
	return out, next, nil
}

func (p packages) IndexVersions(ctx context.Context, ref api.IndexPackage, keep int, page kernel.Page) ([]api.IndexVersion, string, error) {
	return p.indexVersions(ctx, ref, keep, page, nil)
}

func (p packages) IndexVersion(ctx context.Context, ref api.IndexVersion, keep int) (api.IndexVersion, error) {
	rows, _, err := p.indexVersions(ctx, ref.Package, keep, kernel.Page{Limit: 1}, &ref)
	if err != nil {
		return api.IndexVersion{}, err
	}
	if len(rows) == 0 {
		return api.IndexVersion{}, kernel.ErrNotFound
	}
	return rows[0], nil
}

func (p packages) indexVersions(ctx context.Context, ref api.IndexPackage, keep int, page kernel.Page, selected *api.IndexVersion) ([]api.IndexVersion, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index versions page: %w", err)
	}
	if keep < 0 {
		return nil, "", fmt.Errorf("negative version retention: %w", kernel.ErrInvalid)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("index versions cursor: %w", err)
	}
	var out []api.IndexVersion
	err = p.s.Do(ctx, func() error {
		if !p.indexPackageCurrent(ref) {
			return nil
		}
		for _, value := range p.versions().Rows {
			if value.PackageID != ref.ID || len(after) > 0 && value.Version <= after[0] {
				continue
			}
			if selected != nil && (value.ID != selected.ID || value.Version != selected.Version) {
				continue
			}
			entry := api.IndexVersion{
				Package: kernel.Clone(ref), ID: value.ID, Version: value.Version,
				OrderKey: value.OrderKey, Created: value.Created, LastDownloaded: value.LastDownloaded,
			}
			out = indexFirst(out, entry, page.Limit+1, func(a, b api.IndexVersion) int { return strings.Compare(a.Version, b.Version) })
		}
		// Only page entries need protection facts; keeping an entire sorted
		// version set would copy unbounded metadata in this in-memory adapter.
		for i := range out {
			newer := 0
			for _, value := range p.versions().Rows {
				if value.PackageID == ref.ID && (value.OrderKey > out[i].OrderKey || value.OrderKey == out[i].OrderKey && value.Version > out[i].Version) {
					newer++
					if newer >= max(1, keep) {
						break
					}
				}
			}
			out[i].Protected = newer < max(1, keep)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexVersion) []string { return []string{entry.Version} })
	return out, next, nil
}

func (p packages) IndexFiles(ctx context.Context, ref api.IndexVersion, properties map[string][]string, page kernel.Page) ([]api.IndexFile, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index files page: %w", err)
	}
	parts, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("index files cursor: %w", err)
	}
	var after int64
	if len(parts) > 0 {
		after, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("index files cursor: %w: %w", kernel.ErrInvalidCursor, err)
		}
	}
	var out []api.IndexFile
	err = p.s.Do(ctx, func() error {
		if !p.indexPackageCurrent(ref.Package) {
			return nil
		}
		version, ok := p.versions().Get(ref.ID)
		if !ok || version.PackageID != ref.Package.ID || version.Version != ref.Version {
			return nil
		}
		i := items{p.s}
		for link := range p.files().Rows {
			if link.VersionID != ref.ID || link.ItemID <= after {
				continue
			}
			value, ok := i.t().Get(link.ItemID)
			if !ok || value.Folder || value.RepoKey != ref.Package.Repository.Key || value.Cached != (ref.Package.Repository.Class == repo.Remote) {
				continue
			}
			entry := api.IndexFile{
				Version: kernel.Clone(ref), ID: value.ID, Path: value.FullPath(), Cached: value.Cached,
				Size: value.Size, Actual: value.Actual, Created: value.Created, Modified: value.Modified, Updated: value.Updated, PropertiesModified: value.PropertiesModified,
			}
			stats, _ := i.stats().Get(value.ID)
			entry.LastDownloaded = stats.LastDownloaded
			out = indexFirst(out, entry, page.Limit+1, func(a, b api.IndexFile) int { return cmp.Compare(a.ID, b.ID) })
		}
		for at := range out {
			stored, _ := i.props().Get(out[at].ID)
			for key, wanted := range properties {
				for _, value := range stored[key] {
					if slices.Contains(wanted, value) {
						if out[at].Properties == nil {
							out[at].Properties = make(map[string][]string)
						}
						out[at].Properties[key] = append(out[at].Properties[key], value)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexFile) []string { return []string{strconv.FormatInt(entry.ID, 10)} })
	return out, next, nil
}
