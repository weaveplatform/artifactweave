package item

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// removeFiles is format.Env.Remove: it deletes each file at paths in c with
// Delete, so every hook and the trash can apply, then prunes the folders
// the deletes left empty, deepest first, up to the repository root, as
// Artifactory's cleanup prunes the folders of the snapshots it undeploys.
// An empty folder goes past the trash can: it holds nothing to restore,
// and a restore recreates the folders of what it brings back.
func (s *Service) removeFiles(ctx context.Context, c repo.Config, paths []string, by string) error {
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		parents := map[string]bool{}
		for _, p := range paths {
			p = strings.Trim(p, "/")
			_, err := s.Delete(ctx, Target{Repo: c, Cached: c.Class == repo.Remote, Path: p}, by)
			switch {
			case errors.Is(err, store.ErrNotFound):
				continue
			case err != nil:
				return err
			}
			if dir := path.Dir(p); dir != "." {
				parents[dir] = true
			}
		}
		deepest := slices.SortedFunc(maps.Keys(parents), func(a, b string) int {
			return cmp.Or(
				cmp.Compare(strings.Count(b, "/"), strings.Count(a, "/")),
				strings.Compare(a, b),
			)
		})
		for _, dir := range deepest {
			if err := s.pruneEmpty(ctx, c, dir); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// pruneEmpty removes dir while it is an empty folder, then its parent, and
// so on up to the repository root, which stays.
func (s *Service) pruneEmpty(ctx context.Context, c repo.Config, dir string) error {
	items := s.Store.Items()
	for ; dir != "." && dir != ""; dir = path.Dir(dir) {
		children, _, err := items.Children(
			ctx,
			c.Key,
			c.Class == repo.Remote,
			dir,
			store.Page{Limit: 1},
		)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Pruned already, from a deeper folder's walk.
			continue
		case err != nil:
			return fmt.Errorf("item: list %s: %w", dir, err)
		case len(children) > 0:
			return nil
		}
		it, err := items.Get(ctx, c.Key, c.Class == repo.Remote, dir)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return fmt.Errorf("item: get %s: %w", dir, err)
		case !it.Folder:
			return nil
		}
		if _, err := items.Delete(ctx, c.Key, c.Class == repo.Remote, dir); err != nil {
			return fmt.Errorf("item: prune %s: %w", dir, err)
		}
	}
	return nil
}
