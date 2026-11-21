package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func (c cleanupStore) RunPage(ctx context.Context, q port.RunListing) (port.RunPage, error) {
	if err := q.Validate(); err != nil {
		return port.RunPage{}, fmt.Errorf("cleanup run page: %w", err)
	}
	out := port.RunPage{Runs: []model.CleanupRunSnapshot{}}
	err := c.s.Do(ctx, func() error {
		rows := []model.CleanupRunSnapshot{}
		for _, run := range c.runs().Rows {
			e, found := c.executions().Get(run.ID)
			if !found || !cleanupRunVisible(run, e, q) {
				continue
			}
			rows = append(rows, model.CleanupSnapshot(run, e.Policy, e.DisplayProject))
		}
		if err := c.runContent(rows); err != nil {
			return err
		}
		slices.SortFunc(rows, func(a, b model.CleanupRunSnapshot) int {
			order := compareCleanupRun(a, b, q.Order)
			if order == 0 {
				order = cmp.Compare(a.Run.ID, b.Run.ID)
			}
			if q.Descending {
				return -order
			}
			return order
		})
		out.Total = int64(len(rows))
		if q.Offset >= out.Total {
			return nil
		}
		size := min(int64(q.Limit), out.Total-q.Offset)
		out.Runs = rows[int(q.Offset):int(q.Offset+size)]
		return nil
	})
	if err != nil {
		return port.RunPage{}, err
	}
	return out, nil
}

func cleanupRunVisible(r model.CleanupRun, e model.CleanupExecution, q port.RunListing) bool {
	identity := e.RunID == r.ID && r.TriggerID == r.ID && e.Policy.Key == r.PolicyKey
	packageRun := r.ItemType == model.CleanupPackage && e.Policy.ItemType == model.CleanupPackage
	if !identity || !packageRun || !packageRunStatus(r.Status) {
		return false
	}
	visible := slices.ContainsFunc(q.Scopes, func(scope port.RunScope) bool {
		return scope.ProjectKey == e.Policy.ProjectKey && (scope.Incarnation == "" || scope.Incarnation == e.ProjectIncarnation)
	})
	if !visible || q.PolicyKey != "" && r.PolicyKey != q.PolicyKey || q.ExcludeBlank && r.TotalItems == 0 {
		return false
	}
	if len(q.ItemTypes) > 0 && !slices.Contains(q.ItemTypes, r.ItemType) || len(q.Statuses) > 0 && !slices.Contains(q.Statuses, r.Status) {
		return false
	}
	if q.From != nil || q.To != nil {
		if r.Started.IsZero() {
			return false
		}
		at := r.Started.UnixMicro()
		if q.From != nil && at < *q.From || q.To != nil && at >= *q.To {
			return false
		}
	}
	return true
}

func packageRunStatus(status api.RunStatus) bool {
	switch status {
	case api.RunInProgress, api.RunInProgressWithErrors, api.RunCompleted, api.RunCompletedWithErrors, api.RunStopping, api.RunStopped, api.RunFailed:
		return true
	default:
		return false
	}
}

func compareCleanupRun(a, b model.CleanupRunSnapshot, by port.RunOrder) int {
	x, y := a.Run, b.Run
	switch by {
	case port.RunOrderID:
		return cmp.Compare(x.ID, y.ID)
	case port.RunOrderItemType:
		return cmp.Compare(x.ItemType, y.ItemType)
	case port.RunOrderPolicy:
		return cmp.Compare(x.PolicyKey, y.PolicyKey)
	case port.RunOrderProject:
		return cmp.Compare(a.DisplayProject, b.DisplayProject)
	case port.RunOrderEnded:
		return compareRunTime(x.Ended, y.Ended)
	case port.RunOrderTotal:
		return cmp.Compare(x.TotalItems, y.TotalItems)
	case port.RunOrderCleanedSize:
		return cmp.Compare(a.CleanedContentBytes, b.CleanedContentBytes)
	case port.RunOrderCleanedItems:
		return cmp.Compare(x.CleanedItems, y.CleanedItems)
	case port.RunOrderStatus:
		return cmp.Compare(x.Status, y.Status)
	case port.RunOrderActor:
		return cmp.Compare(x.TriggeredBy, y.TriggeredBy)
	case port.RunOrderManual:
		return compareRunBool(x.Manual, y.Manual)
	case port.RunOrderDry:
		return compareRunBool(x.DryRun, y.DryRun)
	default:
		return compareRunTime(x.Started, y.Started)
	}
}

func compareRunBool(a, b bool) int {
	if a == b {
		return 0
	}
	if a {
		return 1
	}
	return -1
}

func compareRunTime(a, b time.Time) int {
	if a.IsZero() && !b.IsZero() {
		return -1
	}
	if b.IsZero() && !a.IsZero() {
		return 1
	}
	return a.Compare(b)
}
