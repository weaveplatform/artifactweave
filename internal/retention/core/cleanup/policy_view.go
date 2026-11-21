package cleanup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// PolicyViewQuery narrows configuration metadata within one current context.
type PolicyViewQuery struct {
	ProjectKey, PolicyKey string
	ItemTypes             []model.CleanupItemType
}

// PolicyOverview keeps schedule and last-run metadata under current authority.
// A configured future policy family never gains a fabricated package run.
type PolicyOverview struct {
	Policy         model.CleanupPolicy
	DisplayProject string
	Last           *model.CleanupRunSnapshot
	Next           time.Time
}

// PolicyViews reads only metadata. Historical execution ownership is checked
// separately from the current policy, including after its key has been reused.
func (s Service) PolicyViews(ctx context.Context, q PolicyViewQuery) ([]PolicyOverview, error) {
	out := []PolicyOverview{}
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(q.ProjectKey) {
			return kernel.ErrForbidden
		}
		if len(q.ItemTypes) == 0 {
			return fmt.Errorf("itemTypes is required: %w", kernel.ErrInvalid)
		}
		seen := map[model.CleanupItemType]bool{}
		for _, kind := range q.ItemTypes {
			if seen[kind] || kind != model.CleanupPackage && kind != model.CleanupBuild && kind != model.CleanupReleaseBundle {
				return fmt.Errorf("invalid policy view item types: %w", kernel.ErrInvalid)
			}
			seen[kind] = true
		}
		scope := port.RunScope{ProjectKey: q.ProjectKey}
		if q.ProjectKey != "" && !authority.Allows("") {
			var err error
			scope.Incarnation, err = s.projectIncarnation(ctx, q.ProjectKey)
			if err != nil {
				return err
			}
		}
		policies, err := s.Store.Cleanup().Policies(ctx, port.PolicyFilter{ProjectKey: q.ProjectKey})
		if err != nil {
			return fmt.Errorf("read cleanup policy overview: %w", err)
		}
		for _, p := range policies {
			if p.ProjectKey != q.ProjectKey || q.PolicyKey != "" && p.Key != q.PolicyKey || !slices.Contains(q.ItemTypes, p.ItemType) {
				continue
			}
			if scope.Incarnation != "" && p.ProjectIncarnation != "" && p.ProjectIncarnation != scope.Incarnation {
				continue
			}
			view, err := s.policyOverview(ctx, p, scope)
			if err != nil {
				return err
			}
			out = append(out, view)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) policyOverview(ctx context.Context, p model.CleanupPolicy, scope port.RunScope) (PolicyOverview, error) {
	display, err := s.policyProjectDisplayName(ctx, p)
	if err != nil {
		return PolicyOverview{}, err
	}
	view := PolicyOverview{Policy: model.CleanupSnapshot(model.CleanupRun{}, p, display).Policy, DisplayProject: display}
	if p.ItemType != model.CleanupPackage {
		return view, nil
	}
	rows, err := s.Store.Cleanup().RunPage(ctx, port.RunListing{Scopes: []port.RunScope{scope}, PolicyKey: p.Key, Limit: 1, Order: port.RunOrderID, Descending: true})
	if err != nil {
		return PolicyOverview{}, fmt.Errorf("read policy last run: %w", err)
	}
	if len(rows.Runs) != 0 {
		view.Last = &rows.Runs[0]
	}
	if !p.Enabled {
		return view, nil
	}
	if s.Schedules == nil {
		return PolicyOverview{}, fmt.Errorf("cleanup schedules unavailable: %w", kernel.ErrStore)
	}
	position, err := s.Schedules.Get(ctx, schedule.Reference{Kind: ScheduleKind, Key: p.Key})
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return PolicyOverview{}, fmt.Errorf("read policy next run: %w", err)
	}
	if err == nil && position.Enabled && position.Revision == p.Revision {
		view.Next = position.Next
	}
	return view, nil
}
