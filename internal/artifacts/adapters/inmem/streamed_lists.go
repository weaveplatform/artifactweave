package inmem

import (
	"cmp"
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// DescendantsByFullPath is the SQL store's over the in-memory table:
// Descendants' items, ordered and paged by full path, which cmp.Compare
// orders byte by byte as COLLATE "C" and SQLite's BINARY do.
func (i items) DescendantsByFullPath(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	folder = folderPath(folder)
	return i.page(
		ctx,
		p,
		func(it model.Item) bool {
			return it.RepoKey == repoKey && it.Cached == cached && below(it.Path, folder) &&
				!it.IsRoot()
		},
		func(a, b model.Item) int { return cmp.Compare(a.FullPath(), b.FullPath()) },
		func(it model.Item) []any { return []any{it.FullPath()} },
	)
}

// DependencyPage pages FindDependencies' order by each row's run, module
// and file position, the SQL store's cursor.
func (b buildStore) DependencyPage(
	ctx context.Context,
	c kernel.Checksums,
	p kernel.Page,
) ([]model.BuildFileRef, string, error) {
	if err := validChecksums(c); err != nil {
		return nil, "", err
	}
	type row struct {
		ref           model.BuildFileRef
		module, place int64
	}
	var (
		out  []model.BuildFileRef
		next string
	)
	err := b.s.Do(ctx, func() error {
		var rows []row
		runs := b.runs().All(nil, func(x, y model.Build) int { return cmp.Compare(x.ID, y.ID) })
		for _, r := range runs {
			for mi, m := range r.Modules {
				for fi, f := range m.Dependencies {
					if checksumsMatch(c, f.Checksums) {
						rows = append(rows, row{
							model.BuildFileRef{Run: r.Run(), Module: m.ID, File: f},
							int64(mi), int64(fi),
						})
					}
				}
			}
		}
		page, cursor, err := memtx.Paginate(rows, p, false, func(x row) []any {
			return []any{x.ref.Run.ID, x.module, x.place}
		})
		for _, x := range page {
			out = append(out, x.ref)
		}
		next = cursor
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}
