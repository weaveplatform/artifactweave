package cleanup

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// RunViewQuery describes a public run window. Zero page/size use 1/25, size is
// capped at 500, and empty ordering means newest start first. Projects further
// narrows the explicit project context; it cannot widen administration scope.
type RunViewQuery struct {
	ProjectKey, PolicyKey string
	Projects              []string
	ItemTypes             []model.CleanupItemType
	Statuses              []api.RunStatus
	FromMillis, ToMillis  *int64
	Page, PageSize        int
	SortBy                port.RunOrder
	SortOrder             string
	ExcludeBlank          bool
}

// RunView contains a bounded page and counts over the same authorized history.
type RunView struct {
	Runs                                   []model.CleanupRunSnapshot
	CurrentPage, LastPage, PageSize, Total int64
}

// Runs authorizes context before validating filters or reading counts. Global
// context requires a current platform administrator. A project administrator
// sees only the current lifetime, including after its policies were deleted.
func (s Service) Runs(ctx context.Context, query RunViewQuery) (RunView, error) {
	var out RunView
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(query.ProjectKey) {
			return kernel.ErrForbidden
		}
		scope := port.RunScope{ProjectKey: query.ProjectKey}
		if query.ProjectKey != "" && !authority.Allows("") {
			var err error
			scope.Incarnation, err = s.projectIncarnation(ctx, query.ProjectKey)
			if err != nil {
				return err
			}
		}
		listing, page, err := runViewListing(query)
		if err != nil {
			return err
		}
		listing.Scopes = []port.RunScope{scope}
		if len(query.Projects) > 0 && !slices.Contains(query.Projects, query.ProjectKey) {
			listing.Scopes = nil
		}
		rows, err := s.Store.Cleanup().RunPage(ctx, listing)
		if err != nil {
			return fmt.Errorf("read cleanup run view: %w", err)
		}
		last := rows.Total / int64(listing.Limit)
		if rows.Total%int64(listing.Limit) != 0 {
			last++
		}
		out = RunView{Runs: rows.Runs, CurrentPage: int64(page), LastPage: last, PageSize: int64(listing.Limit), Total: rows.Total}
		return nil
	})
	if err != nil {
		return RunView{}, err
	}
	return out, nil
}

func runViewListing(q RunViewQuery) (port.RunListing, int, error) {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 25
	}
	q.PageSize = min(q.PageSize, 500)
	validWindow := q.Page > 0 && q.PageSize > 0
	validDirection := q.SortOrder == "" || q.SortOrder == "asc" || q.SortOrder == "desc"
	if !validWindow || !q.SortBy.Valid() || !validDirection {
		return port.RunListing{}, 0, fmt.Errorf("invalid cleanup run page or ordering: %w", kernel.ErrInvalid)
	}
	if int64(q.Page)-1 > math.MaxInt64/int64(q.PageSize) {
		return port.RunListing{}, 0, fmt.Errorf("cleanup run offset exceeds storage bounds: %w", kernel.ErrInvalid)
	}
	for _, item := range q.ItemTypes {
		if item != model.CleanupPackage && item != model.CleanupBuild && item != model.CleanupReleaseBundle {
			return port.RunListing{}, 0, fmt.Errorf("invalid cleanup item type: %w", kernel.ErrInvalid)
		}
	}
	for _, status := range q.Statuses {
		switch status {
		case api.RunInProgress, api.RunInProgressWithErrors, api.RunCompleted, api.RunCompletedWithErrors, api.RunStopped, api.RunStopping, api.RunFailed,
			api.RunPendingWarm, api.RunPartialWarmed, api.RunWarmed:
		default:
			return port.RunListing{}, 0, fmt.Errorf("invalid cleanup run status: %w", kernel.ErrInvalid)
		}
	}
	// Repeated query values describe a set, not extra SQL parameters. Keep
	// database work bounded by the vocabulary, without mutating caller slices.
	q.ItemTypes = slices.Clone(q.ItemTypes)
	slices.Sort(q.ItemTypes)
	q.ItemTypes = slices.Compact(q.ItemTypes)
	q.Statuses = slices.Clone(q.Statuses)
	slices.Sort(q.Statuses)
	q.Statuses = slices.Compact(q.Statuses)
	from, err := runMicros(q.FromMillis)
	if err != nil {
		return port.RunListing{}, 0, err
	}
	to, err := runMicros(q.ToMillis)
	if err != nil {
		return port.RunListing{}, 0, err
	}
	listing := port.RunListing{
		PolicyKey: q.PolicyKey, ItemTypes: q.ItemTypes, Statuses: q.Statuses, From: from, To: to, ExcludeBlank: q.ExcludeBlank,
		Order: q.SortBy, Descending: q.SortOrder != "asc", Offset: (int64(q.Page) - 1) * int64(q.PageSize), Limit: q.PageSize,
	}
	if err := listing.Validate(); err != nil {
		return port.RunListing{}, 0, fmt.Errorf("cleanup run filters: %w", err)
	}
	return listing, q.Page, nil
}

func runMicros(millis *int64) (*int64, error) {
	if millis == nil {
		return nil, nil //nolint:nilnil // an absent optional bound is valid
	}
	if *millis < math.MinInt64/1000 || *millis > math.MaxInt64/1000 {
		return nil, fmt.Errorf("cleanup run time exceeds storage bounds: %w", kernel.ErrInvalid)
	}
	micros := *millis * 1000
	return &micros, nil
}
