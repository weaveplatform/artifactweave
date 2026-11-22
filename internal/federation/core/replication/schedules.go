package replication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

// ScheduleKind binds ordinary single-replication occurrences to CLN-01's
// shared durable Quartz dispatcher, independently of advanced event channels.
const ScheduleKind = "replication:single"

func validScheduleAuthor(author kernel.CredentialReference) bool {
	return author.Name != "" && len(author.Name) <= 256 && utf8.ValidString(author.Name) &&
		!strings.ContainsRune(author.Name, 0) && utf8.ValidString(author.TokenID) &&
		!strings.ContainsRune(author.TokenID, 0) && !author.Anonymous && author.Registry == nil &&
		(author.Password || author.TokenID != "")
}

func (s Service) bindScheduleAuthor(ctx context.Context, target *model.Replication) error {
	if target.CronExp == "" {
		target.ScheduleAuthor = kernel.CredentialReference{}
		return nil
	}
	if !target.Enabled {
		return nil
	}
	var author kernel.CredentialReference
	var err error
	if s.ScheduleAuthor != nil {
		author, err = s.ScheduleAuthor(ctx)
	} else {
		var principal kernel.Principal
		principal, err = kernel.RequireAuthenticated(ctx)
		if err == nil {
			author = principal.CredentialReference()
		}
	}
	if err != nil {
		return fmt.Errorf("replication schedule author: %w", err)
	}
	if !validScheduleAuthor(author) {
		return fmt.Errorf("replication schedule needs an authenticated ordinary caller: %w", kernel.ErrForbidden)
	}
	target.ScheduleAuthor = author
	return nil
}

func (s Service) scheduleNow() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) publishSchedule(ctx context.Context, target model.Replication) error {
	if target.CronExp == "" || target.ScheduleAuthor == (kernel.CredentialReference{}) {
		return s.removeSchedule(ctx, target.Key)
	}
	if s.Schedules == nil {
		return fmt.Errorf("replication scheduling store is unavailable: %w", kernel.ErrStore)
	}
	expression, err := schedule.Parse(target.CronExp, s.Zone)
	if err != nil {
		if !target.Enabled {
			return s.removeSchedule(ctx, target.Key)
		}
		return fmt.Errorf("replication Quartz schedule: %w", err)
	}
	if !validScheduleAuthor(target.ScheduleAuthor) {
		return fmt.Errorf("replication schedule author is invalid: %w", kernel.ErrForbidden)
	}
	state := schedule.State{
		Reference: schedule.Reference{Kind: ScheduleKind, Key: target.Key},
		Revision:  target.Revision, Cron: expression.String(), Zone: expression.Zone(), Enabled: target.Enabled,
	}
	if target.Enabled {
		state.Next, err = expression.Next(s.scheduleNow())
		if err != nil {
			return fmt.Errorf("replication schedule has no future occurrence: %w", kernel.ErrInvalid)
		}
	}
	if err = s.Schedules.Put(ctx, state); err != nil {
		return fmt.Errorf("publish replication schedule: %w", err)
	}
	return nil
}

func (s Service) removeSchedule(ctx context.Context, targetKey string) error {
	if s.Schedules == nil {
		// A configuration-only service with no cron cannot create a position.
		// Application composition always supplies the shared schedule store.
		return nil
	}
	if err := s.Schedules.Delete(ctx, schedule.Reference{Kind: ScheduleKind, Key: targetKey}); err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("remove replication schedule: %w", err)
	}
	return nil
}
