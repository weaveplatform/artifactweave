package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// runSchedules supervises discovery of durable Quartz occurrences. The shared
// queue owns actual execution and retry; a transient discovery error must not
// silently stop all future schedules until a process restart.
func runSchedules(ctx context.Context, scheduler *schedule.Service, log *slog.Logger) {
	for {
		if err := scheduler.Run(ctx, work.DefaultPoll, 100); err == nil || ctx.Err() != nil {
			return
		} else {
			log.Error("schedule discovery failed; retrying", "error", err)
		}
		timer := time.NewTimer(work.DefaultPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
