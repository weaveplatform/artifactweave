package replication_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

func assertReplicationScheduleUnchanged(t *testing.T, w *world, positions schedule.Store, ref schedule.Reference, target model.Replication, position schedule.State) {
	t.Helper()
	after, err := positions.Get(t.Context(), ref)
	must(t, err)
	if !reflect.DeepEqual(target, stored(t, w, target.RepoKey)) || !reflect.DeepEqual(position, after) {
		t.Fatal("refused schedule mutation changed target provenance or its durable position")
	}
}

func TestUnitReplicationScheduleActivationRecoversAfterAuthorAndPositionFailures(t *testing.T) {
	for _, stage := range []string{"author credential", "author storage", "position write"} {
		t.Run(stage, func(t *testing.T) {
			w, now, _, ref := scheduledWorld(t, "remote")
			must(t, w.service.SetEnabled(t.Context(), false, nil, nil))
			before := stored(t, w, "remote")
			positions := w.service.Schedules
			position, err := positions.Get(t.Context(), ref)
			must(t, err)
			author := w.service.ScheduleAuthor
			var want error = errInjected
			resolved := false
			switch stage {
			case "author credential", "author storage":
				if stage == "author credential" {
					want = kernel.ErrBadCredentials
				}
				w.service.ScheduleAuthor = func(context.Context) (kernel.CredentialReference, error) {
					resolved = true
					return kernel.CredentialReference{}, want
				}
			case "position write":
				w.service.Schedules = failedSchedulePositions{Store: positions}
			}
			input := replication.Input{Enabled: ptr(true), CronExp: ptr("0 */5 * * * ?")}
			err = w.service.Update(t.Context(), "remote", input)
			if !errors.Is(err, want) || stage != "position write" && !resolved {
				t.Fatal("activation bypassed the intended author or schedule-store refusal", err)
			}
			assertReplicationScheduleUnchanged(t, w, positions, ref, before, position)
			w.service.ScheduleAuthor, w.service.Schedules = author, positions
			must(t, w.service.Update(t.Context(), "remote", input))
			activated := stored(t, w, "remote")
			current, err := positions.Get(t.Context(), ref)
			must(t, err)
			if !activated.Enabled || activated.Revision == before.Revision || activated.ScheduleAuthor != before.ScheduleAuthor ||
				!current.Enabled || current.Cron != "0 */5 * * * ?" || current.Revision != activated.Revision || !current.Next.After(*now) {
				t.Fatal("healthy activation did not atomically restore author, target and next occurrence")
			}
		})
	}
}

func TestUnitReplicationMalformedLegacyCronCanBeDisabledButCannotBeEnabled(t *testing.T) {
	w, now, _, ref := scheduledWorld(t, "remote")
	positions := w.service.Schedules
	position, err := positions.Get(t.Context(), ref)
	must(t, err)
	legacy := stored(t, w, "remote")
	legacy.CronExp = "legacy-malformed-cron"
	must(t, w.db.Replications().Put(t.Context(), legacy))
	if err := w.service.SetEnabled(t.Context(), true, nil, nil); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("already-enabled legacy cron bypassed activation validation", err)
	}
	assertReplicationScheduleUnchanged(t, w, positions, ref, legacy, position)
	must(t, w.service.SetEnabled(t.Context(), false, nil, nil))
	disabled := stored(t, w, "remote")
	if disabled.Enabled || disabled.CronExp != legacy.CronExp || disabled.Key != legacy.Key || disabled.Revision == legacy.Revision ||
		disabled.ScheduleAuthor != legacy.ScheduleAuthor {
		t.Fatal("disabling legacy cron lost configuration identity or retained activation")
	}
	if _, err := positions.Get(t.Context(), ref); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("disabled malformed legacy cron retained its schedule position", err)
	}
	if err := w.service.SetEnabled(t.Context(), true, nil, nil); !errors.Is(err, kernel.ErrInvalid) || !reflect.DeepEqual(disabled, stored(t, w, "remote")) {
		t.Fatal("malformed disabled cron was reactivated or changed by refusal", err)
	}
	must(t, w.service.Update(t.Context(), "remote", replication.Input{Enabled: ptr(true), CronExp: ptr("0 * * * * ?")}))
	current, err := positions.Get(t.Context(), ref)
	must(t, err)
	if !current.Enabled || !current.Next.After(*now) || current.Revision != stored(t, w, "remote").Revision {
		t.Fatal("repairing legacy cron did not recover a valid schedule")
	}
}

func TestUnitReplicationRefreshRollsBackUpstreamChangeWhenRetainedScheduleIsInvalid(t *testing.T) {
	for _, invalid := range []string{"malformed enabled cron", "unrestorable retained author"} {
		t.Run(invalid, func(t *testing.T) {
			w, now, _, ref := scheduledWorld(t, "remote")
			original := stored(t, w, "remote")
			positions := w.service.Schedules
			position, err := positions.Get(t.Context(), ref)
			must(t, err)
			retained := original
			var want error = kernel.ErrInvalid
			if invalid == "malformed enabled cron" {
				retained.CronExp = "legacy-malformed-cron"
			} else {
				retained.ScheduleAuthor = kernel.CredentialReference{Name: "unrestorable-author"}
				want = kernel.ErrForbidden
			}
			must(t, w.db.Replications().Put(t.Context(), retained))
			upstream := w.repos.values["remote"]
			upstream.URL = "https://changed.example/artifactory/source"
			w.repos.values["remote"] = upstream
			if err := w.service.RefreshRepository(t.Context(), "remote"); !errors.Is(err, want) {
				t.Fatal("upstream refresh accepted an invalid retained schedule", err)
			}
			assertReplicationScheduleUnchanged(t, w, positions, ref, retained, position)
			must(t, w.db.Replications().Put(t.Context(), original))
			must(t, w.service.RefreshRepository(t.Context(), "remote"))
			refreshed := stored(t, w, "remote")
			current, err := positions.Get(t.Context(), ref)
			must(t, err)
			if refreshed.URL != upstream.URL || refreshed.Key != original.Key || refreshed.Password != original.Password || refreshed.ScheduleAuthor != original.ScheduleAuthor ||
				refreshed.Revision == original.Revision || current.Revision != refreshed.Revision || !current.Enabled || !current.Next.After(*now) {
				t.Fatal("healthy source refresh did not atomically fence target and schedule revision")
			}
		})
	}
}

func TestUnitReplicationExpiredFiniteCronRefusesUpdateWithoutLosingFutureSchedule(t *testing.T) {
	w, now, _, ref := scheduledWorld(t, "remote")
	before := stored(t, w, "remote")
	positions := w.service.Schedules
	position, err := positions.Get(t.Context(), ref)
	must(t, err)
	const expired = "0 0 0 1 1 ? 2020"
	expression, err := schedule.Parse(expired, "UTC")
	must(t, err)
	if _, err := expression.Next(*now); !errors.Is(err, schedule.ErrNoFuture) {
		t.Fatal("fixture was not a valid exhausted finite Quartz schedule", err)
	}
	if err := w.service.Update(t.Context(), "remote", replication.Input{CronExp: ptr(expired)}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("expired finite cron replaced the retained future schedule", err)
	}
	assertReplicationScheduleUnchanged(t, w, positions, ref, before, position)
	must(t, w.service.Update(t.Context(), "remote", replication.Input{CronExp: ptr("0 0 0 1 1 ? 2027")}))
	current, err := positions.Get(t.Context(), ref)
	must(t, err)
	if !current.Next.Equal(time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)) || current.Revision != stored(t, w, "remote").Revision || current.Revision == before.Revision {
		t.Fatal("healthy finite schedule did not publish its real next occurrence")
	}
}
