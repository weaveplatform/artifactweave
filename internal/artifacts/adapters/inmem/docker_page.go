package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// DockerPages returns the image layout's page reader, which scans the items
// table and orders as the SQL adapter's indexes do.
func (i items) DockerPages() port.DockerPageStore { return dockerPages(i) }

type dockerPages struct{ s *Store }

// storedManifest reports whether it is a local file named as a manifest is
// stored, the condition of the SQL adapter's partial indexes.
func storedManifest(it model.Item, repoKey string) bool {
	return it.RepoKey == repoKey && !it.Cached && !it.Folder &&
		(it.Name == "manifest.json" || it.Name == "list.manifest.json")
}

// firstN returns the first limit rows, all of them when limit is not
// positive.
func firstN[T any](rows []T, limit int) []T {
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func (p dockerPages) ManifestFolders(
	ctx context.Context, q port.DockerFolderQuery,
) ([]string, error) {
	type folder struct{ fold, name string }
	var out []folder
	prefix, depth := q.Image+"/", strings.Count(q.Image, "/")+3
	cursor := q.AfterFold != "" || q.After != ""
	err := p.s.Do(ctx, func() error {
		seen := map[string]bool{}
		for _, it := range items(p).t().Rows {
			name, below := strings.CutPrefix(it.Path, prefix)
			if !storedManifest(it, q.RepoKey) || !below || it.Depth != depth || seen[name] {
				continue
			}
			f := folder{port.DockerFold(name), name}
			if cursor && (f.fold < q.AfterFold || f.fold == q.AfterFold && name <= q.After) {
				continue
			}
			seen[name] = true
			out = append(out, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b folder) int {
		return cmp.Or(strings.Compare(a.fold, b.fold), strings.Compare(a.name, b.name))
	})
	names := make([]string, 0, len(out))
	for _, f := range firstN(out, q.Limit) {
		names = append(names, f.name)
	}
	return names, nil
}

func (p dockerPages) ImageManifests(
	ctx context.Context, q port.DockerImageQuery,
) ([]port.DockerImageManifest, error) {
	var out []port.DockerImageManifest
	err := p.s.Do(ctx, func() error {
		seen := map[port.DockerImageManifest]bool{}
		for _, it := range items(p).t().Rows {
			// Depth 3 or more leaves out a manifest in a top-level folder,
			// which has no image above it.
			if !storedManifest(it, q.RepoKey) || it.Depth < 3 {
				continue
			}
			cut := strings.LastIndex(it.Path, "/")
			m := port.DockerImageManifest{Image: it.Path[:cut], Folder: it.Path[cut+1:]}
			after := m.Image > q.After ||
				q.Within && m.Image == q.After && m.Folder > q.AfterFolder
			if after && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b port.DockerImageManifest) int {
		return cmp.Or(strings.Compare(a.Image, b.Image), strings.Compare(a.Folder, b.Folder))
	})
	return firstN(out, q.Limit), nil
}
