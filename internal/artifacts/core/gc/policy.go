package gc

import (
	"context"
	"errors"
	"fmt"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/trash"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

const (
	// ScheduleKind names the collector's schedule on the shared Quartz
	// scheduler; app binds the collector's Scheduled handler to it.
	ScheduleKind = "storage:gc"
	// DefaultCron is Artifactory's default gcConfig.cronExp, every four
	// hours, as its shipped artifactory.config.xml sets it.
	DefaultCron = "0 0 /4 * * ?"
)

// scheduleRef is the one schedule every node shares. Its row is the
// authority for the expression: settings and schedule cannot disagree when
// there is only one copy, and a restart or a repeated apply finds the same
// row rather than adding a second.
var scheduleRef = schedule.Reference{Kind: ScheduleKind, Key: "gcConfig"}

// earliestRetentionStart is Artifactory's floor on a retention period, which
// refuses one reaching back before it with "Illegal retention date.
// Retention period goes back too far." (6.20 TrashServiceImpl
// .validateRetentionPeriodTimestamp, checked on every save of the
// configuration). The constant is its 1044106642176 ms.
var earliestRetentionStart = time.UnixMilli(1044106642176)

// PolicyStore is what the storage policy reads and writes: the trash can
// settings, the collector's schedule and the queue the shared scheduler
// needs, over one unit of work. Each composite store in app is one.
type PolicyStore interface {
	tx.Runner
	Settings() settings.SettingsStore
	Schedules() schedule.Store
	WorkQueue() work.WorkQueue
}

// Policies is the artifacts module's StoragePolicies: the trash can's
// settings, which the item and trash services read on every deletion and
// purge, and the collector's Quartz schedule. It holds no state, so every
// node sees a change at once.
type Policies struct{ Store PolicyStore }

var _ artifactsapi.StoragePolicies = Policies{}

// Current returns the effective policy.
func (p Policies) Current(ctx context.Context) (artifactsapi.StoragePolicy, error) {
	t, err := trash.Settings(ctx, p.Store)
	if err != nil {
		return artifactsapi.StoragePolicy{}, err //nolint:wrapcheck // the trash service names its own
	}
	state, err := p.Store.Schedules().Get(ctx, scheduleRef)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		state.Cron = DefaultCron
	case err != nil:
		return artifactsapi.StoragePolicy{}, fmt.Errorf("gc: schedule: %w", err)
	}
	return artifactsapi.StoragePolicy{
		GCCron: state.Cron, TrashEnabled: t.Enabled, TrashRetentionDays: t.RetentionDays,
	}, nil
}

// Plan checks change against the current policy and says what it would
// alter, writing nothing.
func (p Policies) Plan(
	ctx context.Context,
	change artifactsapi.StoragePolicyChange,
	now time.Time,
) (artifactsapi.StoragePolicyResult, error) {
	_, _, res, err := p.plan(ctx, change, now)
	return res, err
}

func (p Policies) plan(
	ctx context.Context,
	change artifactsapi.StoragePolicyChange,
	now time.Time,
) (artifactsapi.StoragePolicy, string, artifactsapi.StoragePolicyResult, error) {
	var res artifactsapi.StoragePolicyResult
	cur, err := p.Current(ctx)
	if err != nil {
		return cur, "", res, err
	}
	next := cur
	cron := ""
	if change.GCCron != nil {
		e, err := schedule.Parse(*change.GCCron, "")
		if err != nil {
			return cur, "", res, invalid(artifactsapi.StoragePolicyGCCron, "is not a valid Quartz cron expression")
		}
		// Quartz refuses to schedule a trigger that will never fire, so
		// Artifactory cannot save one either; accepting it would leave
		// storage never collected while the answer said the schedule took.
		if _, err := e.Next(now); err != nil {
			return cur, "", res, invalid(artifactsapi.StoragePolicyGCCron, "never fires again")
		}
		next.GCCron, cron = e.String(), *change.GCCron
	}
	if change.TrashEnabled != nil {
		next.TrashEnabled = *change.TrashEnabled
	}
	if change.TrashRetentionDays != nil {
		days := *change.TrashRetentionDays
		limit := int(now.Sub(earliestRetentionStart) / (24 * time.Hour))
		switch {
		case days < 0:
			return cur, "", res, invalid(artifactsapi.StoragePolicyTrashRetentionDays, "must not be negative")
		case days > limit:
			return cur, "", res, invalid(artifactsapi.StoragePolicyTrashRetentionDays,
				"goes back too far: a retention period may not reach back before February 2003")
		}
		next.TrashRetentionDays = days
	}
	res.GCChanged = next.GCCron != cur.GCCron
	res.TrashChanged = next.TrashEnabled != cur.TrashEnabled || next.TrashRetentionDays != cur.TrashRetentionDays
	return next, cron, res, nil
}

// Apply checks change as Plan does and writes what it alters: the trash can
// settings, and the collector's schedule, which takes a fresh revision so
// an occurrence queued under the old expression finishes without running.
func (p Policies) Apply(
	ctx context.Context,
	change artifactsapi.StoragePolicyChange,
	now time.Time,
) (artifactsapi.StoragePolicyResult, error) {
	var res artifactsapi.StoragePolicyResult
	err := p.Store.Tx(ctx, func(ctx context.Context) error {
		next, cron, planned, err := p.plan(ctx, change, now)
		if err != nil {
			return err
		}
		res = planned
		if res.TrashChanged {
			if err := p.Store.Settings().Put(ctx, settings.SettingTrashcan, model.TrashcanSettings{
				Enabled: next.TrashEnabled, RetentionDays: next.TrashRetentionDays,
			}); err != nil {
				return fmt.Errorf("gc: trash can settings: %w", err)
			}
		}
		if res.GCChanged {
			if _, err := p.scheduler(now).Configure(ctx, scheduleRef, cron, "", true); err != nil {
				return fmt.Errorf("gc: schedule: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return artifactsapi.StoragePolicyResult{}, err //nolint:wrapcheck // the steps name their own
	}
	return res, nil
}

// EnsureSchedule creates the collector's schedule with DefaultCron when no
// descriptor has set one, so collection runs from the first start. An
// existing schedule keeps its expression, position and revision, so a
// restart neither adds a schedule nor re-times one.
func (p Policies) EnsureSchedule(ctx context.Context, now time.Time) error {
	err := p.Store.Tx(ctx, func(ctx context.Context) error {
		_, err := p.Store.Schedules().Get(ctx, scheduleRef)
		if !errors.Is(err, kernel.ErrNotFound) {
			return err //nolint:wrapcheck // wrapped below
		}
		_, err = p.scheduler(now).Configure(ctx, scheduleRef, DefaultCron, "", true)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("gc: ensure schedule: %w", err)
	}
	return nil
}

func (p Policies) scheduler(now time.Time) *schedule.Service {
	return &schedule.Service{Store: p.Store, Now: func() time.Time { return now }}
}

func invalid(field, reason string) error {
	return &artifactsapi.StoragePolicyInvalid{Field: field, Reason: reason}
}

// Scheduled is the handler of ScheduleKind: one pass per fire, reported to
// onRun when it is set. A failed pass is reported, not retried, as the next
// fire runs another; a pass cut short by shutdown returns its error, so the
// queue hands the occurrence to another node or the next start.
//
// The shared scheduler catches up every fire missed while no node ran, and
// each pass walks the whole blob store, so after a long stop the passes
// would run back to back for nothing. A fire is skipped when a later one is
// already queued: missed fires run once, as Quartz's default cron misfire
// instruction (MISFIRE_INSTRUCTION_FIRE_ONCE_NOW) runs them in Artifactory.
// The test is what has been queued, not what is due by the clock: with a
// frequent expression the next fire is always due by the time a queued one
// runs, and a clock test would skip every pass.
func (c *Collector) Scheduled(schedules interface{ Schedules() schedule.Store }, onRun func(Report, error)) schedule.Handler {
	return func(ctx context.Context, trigger schedule.Trigger) error {
		later, err := c.superseded(ctx, schedules.Schedules(), trigger)
		if err != nil || later {
			return err
		}
		rep, err := c.Run(ctx)
		if ctx.Err() != nil {
			return fmt.Errorf("gc: scheduled pass: %w", ctx.Err())
		}
		if onRun != nil {
			onRun(rep, err)
		}
		return nil
	}
}

// superseded reports whether the fire after trigger's has been queued. The
// scheduler queues the fire at its stored position and then advances the
// position to the fire after it, so a position past trigger's successor
// means that successor is queued and will run the pass. The scheduler
// admitted trigger under the schedule's current revision, so the stored
// expression is the one that produced it.
func (c *Collector) superseded(ctx context.Context, schedules schedule.Store, trigger schedule.Trigger) (bool, error) {
	state, err := schedules.Get(ctx, trigger.Reference)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("gc: read schedule: %w", err)
	}
	// Admission already parsed the stored expression. Were it unreadable,
	// the zero Expression's Next fails and the pass runs, the safe answer.
	e, _ := schedule.Parse(state.Cron, state.Zone)
	next, err := e.Next(trigger.Fire)
	return err == nil && next.Before(state.Next), nil
}
