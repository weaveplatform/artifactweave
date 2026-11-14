package build

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The build lists a page at a time (decision 0043, "Streaming complete
// lists"): the legacy build names and build numbers lists write each store
// page as they read it, and Names and Runs collect the same pages, so the
// two answer the same rows in the same order.

// NamePages walks the build names of a project a store page at a time, in
// the store's name order. keep is the caller's filter: a name is listed
// when the caller may read a run of the build.
func (s *Service) NamePages(
	projectKey string,
	a Access,
) (*kernel.Pages[model.BuildName], func(model.BuildName) bool) {
	projectKey = ProjectKey(projectKey)
	repoKey := model.BuildInfoRepo(projectKey)
	pages := kernel.NewPages(
		func(ctx context.Context, p kernel.Page) ([]model.BuildName, string, error) {
			names, next, err := s.Store.Builds().Names(ctx, projectKey, p)
			if err != nil {
				return nil, "", fmt.Errorf("build: names: %w", err)
			}
			return names, next, nil
		},
		s.pageSize(),
	)
	return pages, func(n model.BuildName) bool {
		return a.Build(repoKey, model.EncodeBuildInfoSegment(n.Name), true, api.ActionRead)
	}
}

// RunPages walks the runs of a build a store page at a time, newest first.
// keep is the caller's filter: a run is listed when the caller may read its
// document. A walk that keeps nothing answers NoRuns.
func (s *Service) RunPages(
	projectKey, name string,
	a Access,
) (*kernel.Pages[model.BuildRun], func(model.BuildRun) bool) {
	projectKey = ProjectKey(projectKey)
	repoKey := model.BuildInfoRepo(projectKey)
	pages := kernel.NewPages(
		func(ctx context.Context, p kernel.Page) ([]model.BuildRun, string, error) {
			runs, next, err := s.Store.Builds().Runs(ctx, projectKey, name, p)
			if err != nil {
				return nil, "", fmt.Errorf("build: runs of %s: %w", name, err)
			}
			return runs, next, nil
		},
		s.pageSize(),
	)
	return pages, func(r model.BuildRun) bool {
		return a.Build(repoKey, documentPath(r), false, api.ActionRead)
	}
}

// NoRuns is the error of a build whose runs the caller may read none of:
// ErrNotFound when the walk saw no run at all, ErrForbidden when it saw
// runs the caller may not read.
func NoRuns(name string, seen bool) error {
	if seen {
		return ErrForbidden
	}
	return fmt.Errorf("build: runs of %s: %w", name, ErrNotFound)
}

// pageSize is the store page a listing reads: Page, or kernel.MaxLimit.
func (s *Service) pageSize() int {
	if s.Page > 0 {
		return s.Page
	}
	return kernel.MaxLimit
}

// kept collects the rows of every page that keep accepts, and reports
// whether any page held a row at all.
func kept[T any](
	ctx context.Context,
	pages *kernel.Pages[T],
	keep func(T) bool,
) ([]T, bool, error) {
	var (
		out  []T
		seen bool
	)
	for {
		rows, ok, err := pages.Next(ctx)
		if err != nil {
			return nil, false, err //nolint:wrapcheck // the page functions above wrap
		}
		if !ok {
			return out, seen, nil
		}
		seen = seen || len(rows) > 0
		for _, r := range rows {
			if keep(r) {
				out = append(out, r)
			}
		}
	}
}
