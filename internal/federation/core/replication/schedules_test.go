package replication_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type scheduleBackend struct {
	port.ConfigurationStore
	positions schedule.Store
	queue     work.WorkQueue
}

func (s scheduleBackend) Schedules() schedule.Store { return s.positions }
func (s scheduleBackend) WorkQueue() work.WorkQueue { return s.queue }

func scheduledWorld(t *testing.T, key string) (*world, *time.Time, schedule.Service, schedule.Reference) {
	t.Helper()
	w := newWorld()
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	w.service.Now = func() time.Time { return now }
	in := replication.Input{CronExp: ptr("0 * * * * ?")}
	if key == "local" {
		in.URL = ptr("https://target.example/artifactory/destination")
	}
	must(t, w.service.Replace(t.Context(), key, in))
	target := stored(t, w, key)
	platform := platforminmem.New(w.db.Store)
	scheduler := schedule.Service{
		Store: scheduleBackend{ConfigurationStore: w.db, positions: w.service.Schedules, queue: platform.WorkQueue()},
		Now:   func() time.Time { return now },
	}
	return w, &now, scheduler, schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key}
}

func TestUnitReplicationSchedulesRetainLogicalFiresAndFenceConfigurationChanges(t *testing.T) {
	for _, key := range []string{"local", "remote"} {
		t.Run(key, func(t *testing.T) {
			w, now, scheduler, ref := scheduledWorld(t, key)
			original := stored(t, w, key)
			position, err := w.service.Schedules.Get(t.Context(), ref)
			must(t, err)
			if position.Revision != original.Revision || !position.Next.Equal(now.Add(time.Minute)) || original.ScheduleAuthor.Name != "schedule-author" {
				t.Fatal("schedule did not retain the current target/verified caller binding")
			}
			*now = position.Next
			count, err := scheduler.Tick(t.Context(), 1)
			must(t, err)
			if count != 1 {
				t.Fatal("logical fire was not queued")
			}
			count, err = scheduler.Tick(t.Context(), 1)
			must(t, err)
			if count != 0 {
				t.Fatal("repeated tick duplicated the same logical fire")
			}
			queue := platforminmem.New(w.db.Store).WorkQueue()
			items, err := queue.Lease(
				t.Context(),
				work.WorkLease{Holder: "replication-schedule-test", Kinds: []string{schedule.WorkKind}, Limit: 1, Now: *now, Until: now.Add(time.Minute)},
			)
			must(t, err)
			if len(items) != 1 {
				t.Fatal("logical occurrence was not durably leased")
			}
			before, err := w.service.Schedules.Get(t.Context(), ref)
			must(t, err)
			must(t, w.service.Update(t.Context(), key, replication.Input{}))
			must(t, w.service.SetEnabled(t.Context(), true, []string{original.URL}, nil))
			after, err := w.service.Schedules.Get(t.Context(), ref)
			must(t, err)
			if !reflect.DeepEqual(before, after) || stored(t, w, key).Revision != original.Revision {
				t.Fatal("identical configuration/enablement reset a queued logical fire")
			}
			admitted := 0
			handler := map[string]schedule.Handler{replication.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
				return w.service.WithTarget(ctx, trigger.Reference.Key, trigger.Revision, func(context.Context, model.Replication) error { admitted++; return nil })
			}}
			must(t, scheduler.Execute(t.Context(), items[0], handler))
			if admitted != 1 {
				t.Fatal("unchanged logical occurrence was not admitted")
			}
			must(t, w.service.Update(t.Context(), key, replication.Input{Password: ptr("changed-fixture-password")}))
			changed := stored(t, w, key)
			position, err = w.service.Schedules.Get(t.Context(), ref)
			must(t, err)
			if changed.Revision == original.Revision || position.Revision != changed.Revision {
				t.Fatal("credential change did not fence the old schedule generation")
			}
			must(t, scheduler.Execute(t.Context(), items[0], handler))
			if admitted != 1 {
				t.Fatal("superseded occurrence retained execution authority")
			}
		})
	}
}

func TestUnitReplicationScheduleDisableDeletionAndRepositoryLifetimeRemoveAuthority(t *testing.T) {
	for _, action := range []string{"disable", "remove-cron", "delete", "repository-reset"} {
		t.Run(action, func(t *testing.T) {
			w, _, _, ref := scheduledWorld(t, "local")
			before := stored(t, w, "local")
			switch action {
			case "disable":
				must(t, w.service.SetEnabled(t.Context(), false, nil, nil))
			case "remove-cron":
				must(t, w.service.Update(t.Context(), "local", replication.Input{CronExp: ptr("")}))
			case "delete":
				must(t, w.service.Delete(t.Context(), "local", ""))
			case "repository-reset":
				must(t, w.service.ResetRepository(t.Context(), "local"))
			}
			state, err := w.service.Schedules.Get(t.Context(), ref)
			if action == "disable" {
				must(t, err)
				if state.Enabled || !state.Next.IsZero() || state.Revision == before.Revision {
					t.Fatal("disabled schedule retained queued execution authority")
				}
			} else if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("schedule position outlived its target/configuration", err)
			}
		})
	}
}

type failedSchedulePositions struct {
	schedule.Store
	remove bool
}

func (s failedSchedulePositions) Put(ctx context.Context, state schedule.State) error {
	if !s.remove {
		return errInjected
	}
	return s.Store.Put(ctx, state)
}

func (s failedSchedulePositions) Delete(ctx context.Context, ref schedule.Reference) error {
	if s.remove {
		return errInjected
	}
	return s.Store.Delete(ctx, ref)
}

func TestUnitReplicationScheduleFailuresRollBackTargetAndAuthorChanges(t *testing.T) {
	for _, action := range []string{"update", "disable", "source-refresh", "delete", "repository-reset"} {
		t.Run(action, func(t *testing.T) {
			w, _, _, ref := scheduledWorld(t, "remote")
			before := stored(t, w, "remote")
			positions := w.service.Schedules
			position, err := positions.Get(t.Context(), ref)
			must(t, err)
			w.service.Schedules = failedSchedulePositions{Store: positions, remove: action == "delete" || action == "repository-reset"}
			switch action {
			case "update":
				err = w.service.Update(t.Context(), "remote", replication.Input{Password: ptr("changed")})
			case "disable":
				err = w.service.SetEnabled(t.Context(), false, nil, nil)
			case "source-refresh":
				config := w.repos.values["remote"]
				config.URL = "https://replacement.example/artifactory/source"
				w.repos.values["remote"] = config
				err = w.service.RefreshRepository(t.Context(), "remote")
			case "delete":
				err = w.service.Delete(t.Context(), "remote", "")
			case "repository-reset":
				err = w.service.ResetRepository(t.Context(), "remote")
			}
			if !errors.Is(err, errInjected) || stored(t, w, "remote") != before {
				t.Fatalf("schedule failure half-published target mutation: %v", err)
			}
			after, err := positions.Get(t.Context(), ref)
			must(t, err)
			if !reflect.DeepEqual(after, position) {
				t.Fatal("failed mutation moved the schedule")
			}
		})
	}
}

func TestUnitReplicationSchedulesNeverInventAnAuthorOrExposeTheReference(t *testing.T) {
	w, _, _, ref := scheduledWorld(t, "remote")
	target := stored(t, w, "remote")
	encoded, err := json.Marshal(target)
	must(t, err)
	if strings.Contains(string(encoded), target.ScheduleAuthor.Name) || strings.Contains(string(encoded), target.ScheduleAuthor.TokenID) {
		t.Fatal("public model JSON exposed a private schedule reference")
	}
	// A legacy row has no recoverable author. Source refresh must not create
	// a new system identity or activate its cron in the background.
	target.ScheduleAuthor = kernel.CredentialReference{}
	must(t, w.db.Replications().Put(t.Context(), target))
	config := w.repos.values["remote"]
	config.URL = "https://replacement.example/artifactory/source"
	w.repos.values["remote"] = config
	must(t, w.service.RefreshRepository(t.Context(), "remote"))
	if _, err = w.service.Schedules.Get(t.Context(), ref); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("legacy schedule acquired fabricated execution authority", err)
	}
	must(t, w.service.SetEnabled(t.Context(), true, nil, nil))
	activated := stored(t, w, "remote")
	state, err := w.service.Schedules.Get(t.Context(), ref)
	must(t, err)
	if activated.ScheduleAuthor.Name == "" || !state.Enabled || state.Revision != activated.Revision {
		t.Fatal("authenticated enablement did not bind a fresh schedule author")
	}
}

func TestUnitReplicationScheduleRefusesUnknownAndRegistryAuthorsBeforeActivation(t *testing.T) {
	for _, test := range []struct {
		name   string
		author kernel.CredentialReference
	}{
		{"missing", kernel.CredentialReference{}},
		{"anonymous", kernel.CredentialReference{Name: "anonymous", Anonymous: true, Password: true}},
		{"registry", kernel.CredentialReference{Name: "registry-caller", TokenID: "reference", Registry: &kernel.RegistryReference{}}},
		{"no-verified-provenance", kernel.CredentialReference{Name: "unverified"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld()
			must(t, w.service.Replace(t.Context(), "remote", replication.Input{}))
			before := stored(t, w, "remote")
			w.service.ScheduleAuthor = func(context.Context) (kernel.CredentialReference, error) { return test.author, nil }
			err := w.service.Update(t.Context(), "remote", replication.Input{CronExp: ptr("0 * * * * ?")})
			if !errors.Is(err, kernel.ErrForbidden) || stored(t, w, "remote") != before {
				t.Fatalf("invalid caller activated or changed schedule: %v", err)
			}
		})
	}
}
