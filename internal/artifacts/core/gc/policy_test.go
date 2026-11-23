package gc_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/trash"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// policyStore is what the policy effects run on: the collector's store, the
// item service's, and the schedule the policy writes.
type policyStore interface {
	item.Store
	gc.Store
	gc.PolicyStore
}

var policyStores = map[string]func(t *testing.T) policyStore{
	"inmem": func(*testing.T) policyStore { return inmem.New() },
	"sqlite": func(t *testing.T) policyStore {
		t.Helper()
		st, err := appstoretest.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "aw.db"), appstoretest.SQLiteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	},
	"postgres": func(t *testing.T) policyStore {
		t.Helper()
		st, err := appstoretest.OpenPostgres(t.Context(), sqltest.ScratchDSN(t, sqltest.PostgresDSN(t)), appstoretest.PostgresOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	},
}

func ptr[T any](v T) *T { return &v }

var policyStart = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func TestUnitPolicyDefaults(t *testing.T) {
	db := inmem.New()
	got, err := gc.Policies{Store: db}.Current(t.Context())
	want := artifactsapi.StoragePolicy{GCCron: gc.DefaultCron, TrashEnabled: true, TrashRetentionDays: trash.DefaultRetentionDays}
	if err != nil || got != want {
		t.Fatalf("defaults %+v, %v", got, err)
	}
}

func TestUnitPolicyEnsureSchedule(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	p := gc.Policies{Store: db}
	if err := p.EnsureSchedule(ctx, policyStart); err != nil {
		t.Fatal(err)
	}
	first := gcSchedule(t, db)
	if first.Cron != gc.DefaultCron || !first.Enabled || !first.Next.Equal(policyStart.Add(4*time.Hour)) {
		t.Fatalf("default schedule %+v", first)
	}
	// A restart, later, keeps the position rather than re-timing it.
	if err := p.EnsureSchedule(ctx, policyStart.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again := gcSchedule(t, db); again != first {
		t.Fatalf("restart changed the schedule: %+v, was %+v", again, first)
	}
	if _, err := p.Apply(ctx, artifactsapi.StoragePolicyChange{GCCron: ptr("0 0 3 * * ?")}, policyStart); err != nil {
		t.Fatal(err)
	}
	applied := gcSchedule(t, db)
	if err := p.EnsureSchedule(ctx, policyStart); err != nil {
		t.Fatal(err)
	}
	if again := gcSchedule(t, db); again != applied || again.Cron != "0 0 3 * * ?" {
		t.Fatalf("restart replaced a configured schedule: %+v", again)
	}
}

func TestUnitPolicyApplyIsIdempotent(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	p := gc.Policies{Store: db}
	change := artifactsapi.StoragePolicyChange{
		GCCron: ptr("0 0 /12 * * ?"), TrashEnabled: ptr(false), TrashRetentionDays: ptr(0),
	}
	planned, err := p.Plan(ctx, change, policyStart)
	if err != nil || !planned.GCChanged || !planned.TrashChanged {
		t.Fatalf("plan %+v, %v", planned, err)
	}
	if got, _ := p.Current(ctx); got.GCCron != gc.DefaultCron || !got.TrashEnabled {
		t.Fatalf("a plan wrote %+v", got)
	}
	res, err := p.Apply(ctx, change, policyStart)
	if err != nil || res != planned {
		t.Fatalf("apply %+v, %v", res, err)
	}
	want := artifactsapi.StoragePolicy{GCCron: "0 0 /12 * * ?", TrashEnabled: false, TrashRetentionDays: 0}
	if got, err := p.Current(ctx); err != nil || got != want {
		t.Fatalf("current %+v, %v", got, err)
	}
	first := gcSchedule(t, db)
	// The same policy spelled differently changes nothing, and keeps the
	// schedule's revision, so a queued occurrence still runs.
	res, err = p.Apply(ctx, artifactsapi.StoragePolicyChange{
		GCCron: ptr(" 0 0 /12  * * ? "), TrashEnabled: ptr(false), TrashRetentionDays: ptr(0),
	}, policyStart.Add(time.Hour))
	if err != nil || res.GCChanged || res.TrashChanged {
		t.Fatalf("reapply %+v, %v", res, err)
	}
	if again := gcSchedule(t, db); again != first {
		t.Fatalf("reapply changed the schedule: %+v", again)
	}
	// One half alone leaves the other.
	res, err = p.Apply(ctx, artifactsapi.StoragePolicyChange{TrashRetentionDays: ptr(7)}, policyStart)
	if err != nil || res.GCChanged || !res.TrashChanged {
		t.Fatalf("retention alone %+v, %v", res, err)
	}
	if got, _ := p.Current(ctx); got.TrashEnabled || got.TrashRetentionDays != 7 || got.GCCron != want.GCCron {
		t.Fatalf("after retention alone %+v", got)
	}
}

func TestUnitPolicyRefusals(t *testing.T) {
	limit := int(policyStart.Sub(time.UnixMilli(1044106642176)) / (24 * time.Hour))
	for _, tc := range []struct {
		name   string
		change artifactsapi.StoragePolicyChange
		field  string
	}{
		{"five fields", artifactsapi.StoragePolicyChange{GCCron: ptr("0 */4 * * *")}, artifactsapi.StoragePolicyGCCron},
		{"both days", artifactsapi.StoragePolicyChange{GCCron: ptr("0 0 0 * * *")}, artifactsapi.StoragePolicyGCCron},
		{"never again", artifactsapi.StoragePolicyChange{GCCron: ptr("0 0 0 1 1 ? 2025")}, artifactsapi.StoragePolicyGCCron},
		{"negative", artifactsapi.StoragePolicyChange{TrashRetentionDays: ptr(-1)}, artifactsapi.StoragePolicyTrashRetentionDays},
		{"too far", artifactsapi.StoragePolicyChange{TrashRetentionDays: ptr(limit + 1)}, artifactsapi.StoragePolicyTrashRetentionDays},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := inmem.New()
			p := gc.Policies{Store: db}
			for _, run := range []func(context.Context, artifactsapi.StoragePolicyChange, time.Time) (artifactsapi.StoragePolicyResult, error){p.Plan, p.Apply} {
				_, err := run(t.Context(), tc.change, policyStart)
				var refused *artifactsapi.StoragePolicyInvalid
				if !errors.As(err, &refused) || refused.Field != tc.field || !errors.Is(err, kernel.ErrInvalid) ||
					!strings.HasPrefix(err.Error(), tc.field+" ") {
					t.Fatalf("refusal %v", err)
				}
			}
			if got, _ := p.Current(t.Context()); got.GCCron != gc.DefaultCron || got.TrashRetentionDays != trash.DefaultRetentionDays {
				t.Fatalf("a refusal wrote %+v", got)
			}
		})
	}
	// The boundary itself is Artifactory's last accepted period.
	if _, err := (gc.Policies{Store: inmem.New()}).Apply(t.Context(),
		artifactsapi.StoragePolicyChange{TrashRetentionDays: ptr(limit)}, policyStart); err != nil {
		t.Fatalf("boundary refused: %v", err)
	}
}

// failingPolicyStore fails the named store call.
type failingPolicyStore struct {
	*inmem.Store
	fail string
}

var errPolicyBoom = errors.New("policy store failure")

func (f failingPolicyStore) Settings() settings.SettingsStore {
	return failingSettings{f.Store.Settings(), f.fail}
}

func (f failingPolicyStore) Schedules() schedule.Store {
	return failingSchedules{f.Store.Schedules(), f.fail}
}

type failingSettings struct {
	settings.SettingsStore
	fail string
}

func (f failingSettings) Get(ctx context.Context, key string, v any) error {
	if f.fail == "settings.get" {
		return errPolicyBoom
	}
	return f.SettingsStore.Get(ctx, key, v) //nolint:wrapcheck // the fault passes through
}

func (f failingSettings) Put(ctx context.Context, key string, v any) error {
	if f.fail == "settings.put" {
		return errPolicyBoom
	}
	return f.SettingsStore.Put(ctx, key, v) //nolint:wrapcheck // the fault passes through
}

type failingSchedules struct {
	schedule.Store
	fail string
}

func (f failingSchedules) Get(ctx context.Context, r schedule.Reference) (schedule.State, error) {
	if f.fail == "schedules.get" {
		return schedule.State{}, errPolicyBoom
	}
	return f.Store.Get(ctx, r) //nolint:wrapcheck // the fault passes through
}

func (f failingSchedules) Put(ctx context.Context, s schedule.State) error {
	if f.fail == "schedules.put" {
		return errPolicyBoom
	}
	return f.Store.Put(ctx, s) //nolint:wrapcheck // the fault passes through
}

func TestUnitPolicyStoreFailures(t *testing.T) {
	change := artifactsapi.StoragePolicyChange{GCCron: ptr("0 0 3 * * ?"), TrashEnabled: ptr(false)}
	for _, fail := range []string{"settings.get", "settings.put", "schedules.get", "schedules.put"} {
		t.Run(fail, func(t *testing.T) {
			db := inmem.New()
			p := gc.Policies{Store: failingPolicyStore{db, fail}}
			_, err := p.Apply(t.Context(), change, policyStart)
			if !errors.Is(err, errPolicyBoom) {
				t.Fatalf("apply: %v", err)
			}
			// The settings and the schedule commit together or not at all.
			if got, _ := (gc.Policies{Store: db}).Current(t.Context()); !got.TrashEnabled || got.GCCron != gc.DefaultCron {
				t.Fatalf("a failed apply left %+v", got)
			}
			if fail == "schedules.get" || fail == "schedules.put" {
				if err := p.EnsureSchedule(t.Context(), policyStart); !errors.Is(err, errPolicyBoom) {
					t.Fatalf("ensure: %v", err)
				}
			}
			if fail == "settings.get" || fail == "schedules.get" {
				if _, err := p.Current(t.Context()); !errors.Is(err, errPolicyBoom) {
					t.Fatalf("current: %v", err)
				}
			}
		})
	}
}

func TestUnitScheduledStopsOnShutdown(t *testing.T) {
	w := newWorld(t)
	c, err := gc.New(w.db, w.blobs, gc.Options{Now: w.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := (gc.Policies{Store: w.db}).EnsureSchedule(t.Context(), w.now()); err != nil {
		t.Fatal(err)
	}
	trigger := schedule.Trigger{Reference: gcSchedule(t, w.db).Reference, Fire: w.now()}
	reported := 0
	run := c.Scheduled(w.db, func(gc.Report, error) { reported++ })
	if err := run(t.Context(), trigger); err != nil || reported != 1 {
		t.Fatalf("pass: %v, reported %d", err, reported)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(ctx, trigger); !errors.Is(err, context.Canceled) || reported != 1 {
		t.Fatalf("a shutdown before the pass must hand back its occurrence: %v, reported %d", err, reported)
	}
	// The schedule read succeeds and the pass itself is cut short.
	cut := c.Scheduled(detachedSchedules{w.db}, func(gc.Report, error) { reported++ })
	if err := cut(ctx, trigger); !errors.Is(err, context.Canceled) || reported != 1 {
		t.Fatalf("a pass cut short must hand back its occurrence: %v, reported %d", err, reported)
	}
	if err := c.Scheduled(w.db, nil)(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	// A fire whose successor is already due is skipped: missed fires run once.
	w.advance(4 * time.Hour)
	if err := run(t.Context(), trigger); err != nil || reported != 1 {
		t.Fatalf("superseded fire: %v, reported %d", err, reported)
	}
	// A schedule removed since admission still gets its pass, and a store
	// that cannot be read fails the occurrence so the queue retries it.
	if err := run(t.Context(), schedule.Trigger{Reference: schedule.Reference{Kind: gc.ScheduleKind, Key: "gone"}, Fire: w.now()}); err != nil || reported != 2 {
		t.Fatalf("removed schedule: %v, reported %d", err, reported)
	}
	failing := c.Scheduled(failingPolicyStore{w.db, "schedules.get"}, nil)
	if err := failing(t.Context(), trigger); !errors.Is(err, errPolicyBoom) {
		t.Fatalf("unreadable schedule: %v", err)
	}
}

// detachedSchedules reads the schedule whatever the context, so a
// cancelled context reaches the pass rather than stopping at the read.
type detachedSchedules struct {
	db interface{ Schedules() schedule.Store }
}

func (d detachedSchedules) Schedules() schedule.Store { return detachedStore{d.db.Schedules()} }

type detachedStore struct{ schedule.Store }

func (d detachedStore) Get(ctx context.Context, ref schedule.Reference) (schedule.State, error) {
	return d.Store.Get(context.WithoutCancel(ctx), ref) //nolint:wrapcheck // a test double
}

func gcSchedule(t *testing.T, db interface{ Schedules() schedule.Store }) schedule.State {
	t.Helper()
	s, err := db.Schedules().Get(context.WithoutCancel(t.Context()), schedule.Reference{Kind: gc.ScheduleKind, Key: "gcConfig"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// policyWorld drives the collector through the shared scheduler on a
// controlled clock, as the runtime does: a tick queues each fire, the queue
// leases it and the scheduler admits it before the pass runs.
type policyWorld struct {
	db        policyStore
	blobs     *fs.Store
	items     *item.Service
	trash     *trash.Service
	policies  gc.Policies
	scheduler *schedule.Service
	handlers  map[string]schedule.Handler
	libs      repo.Config
	clock     time.Time
	passes    []gc.Report
}

func newPolicyWorld(t *testing.T, db policyStore) *policyWorld {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &policyWorld{db: db, blobs: blobs, clock: policyStart, policies: gc.Policies{Store: db}}
	now := func() time.Time { return w.clock }
	w.items = &item.Service{Store: db, Blobs: blobs, Now: now, Formats: ecosystem.For}
	w.trash = &trash.Service{Store: db, Items: w.items, Now: now}
	w.scheduler = &schedule.Service{Store: db, Now: now}
	c, err := gc.New(db, blobs, gc.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	w.handlers = map[string]schedule.Handler{gc.ScheduleKind: c.Scheduled(db, func(r gc.Report, err error) {
		if err != nil {
			t.Errorf("scheduled pass: %v", err)
		}
		w.passes = append(w.passes, r)
	})}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	w.libs, _ = db.Repos().Get(t.Context(), "libs")
	return w
}

func (w *policyWorld) deploy(t *testing.T, p, body string) string {
	t.Helper()
	it, err := w.items.Deploy(t.Context(), item.Deploy{Target: item.Target{Repo: w.libs, Path: p}, Body: strings.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	return it.Actual.SHA256
}

func (w *policyWorld) remove(t *testing.T, p string) {
	t.Helper()
	if _, err := w.items.Delete(t.Context(), item.Target{Repo: w.libs, Path: p}, "alice"); err != nil {
		t.Fatal(err)
	}
}

func (w *policyWorld) apply(t *testing.T, change artifactsapi.StoragePolicyChange) artifactsapi.StoragePolicyResult {
	t.Helper()
	res, err := w.policies.Apply(t.Context(), change, w.clock)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// queue ticks until nothing more is due and leases what it queued.
func (w *policyWorld) queue(t *testing.T) []work.WorkItem {
	t.Helper()
	for {
		n, err := w.scheduler.Tick(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	items, err := w.db.WorkQueue().Lease(t.Context(), work.WorkLease{
		Kinds: []string{schedule.WorkKind}, Holder: "gc-test", Limit: 10, Now: w.clock, Until: w.clock.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (w *policyWorld) execute(t *testing.T, items []work.WorkItem) {
	t.Helper()
	for _, it := range items {
		if err := w.scheduler.Execute(t.Context(), it, w.handlers); err != nil {
			t.Fatal(err)
		}
		if err := w.db.WorkQueue().Complete(t.Context(), it.Kind, it.Key, it.Generation, w.clock); err != nil {
			t.Fatal(err)
		}
	}
}

// fire moves the clock to at and runs what the schedule queued by then,
// returning the passes that ran.
func (w *policyWorld) fire(t *testing.T, at time.Time) []gc.Report {
	t.Helper()
	w.clock = at
	w.passes = nil
	w.execute(t, w.queue(t))
	return w.passes
}

func (w *policyWorld) stored(t *testing.T, sha string) bool {
	t.Helper()
	ok, err := w.blobs.Exists(t.Context(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestAccStoragePolicyEffects applies the descriptor's policy through the
// owner on every store and observes what the scheduled passes do to trashed
// entries and stored bytes.
func TestAccStoragePolicyEffects(t *testing.T) {
	for name, open := range policyStores {
		t.Run(name, func(t *testing.T) { testStoragePolicyEffects(t, open(t)) })
	}
}

func testStoragePolicyEffects(t *testing.T, db policyStore) {
	ctx := t.Context()
	w := newPolicyWorld(t, db)
	if err := w.policies.EnsureSchedule(ctx, w.clock); err != nil {
		t.Fatal(err)
	}
	daily := artifactsapi.StoragePolicyChange{GCCron: ptr("0 0 3 * * ?"), TrashEnabled: ptr(true), TrashRetentionDays: ptr(1)}
	if res := w.apply(t, daily); !res.GCChanged || !res.TrashChanged {
		t.Fatalf("first apply %+v", res)
	}
	kept := w.deploy(t, "keep.txt", "restored content")
	shared := w.deploy(t, "shared.txt", "shared content")
	w.deploy(t, "copy.txt", "shared content")
	gone := w.deploy(t, "gone.txt", "expired content")
	for _, p := range []string{"keep.txt", "shared.txt", "gone.txt"} {
		w.remove(t, p)
	}

	// Within the retention period a pass keeps what the trash can holds, and
	// a trashed file can still be restored.
	day1 := policyStart.Add(3 * time.Hour)
	if passes := w.fire(t, day1); len(passes) != 1 || passes[0].TrashPurged != 0 || passes[0].BlobsDeleted != 0 {
		t.Fatalf("first fire %+v", passes)
	}
	for _, sha := range []string{kept, shared, gone} {
		if !w.stored(t, sha) {
			t.Fatalf("trashed content %s collected within its retention", sha)
		}
	}
	if n, err := w.trash.Restore(ctx, "libs", "keep.txt", trash.Destination{Repo: w.libs}, "bob", nil); err != nil || n != 1 {
		t.Fatalf("restore %d, %v", n, err)
	}

	// Reapplying, and restarting, add no schedule and no extra pass.
	before := gcSchedule(t, db)
	if res := w.apply(t, daily); res.GCChanged || res.TrashChanged {
		t.Fatalf("reapply %+v", res)
	}
	if err := w.policies.EnsureSchedule(ctx, w.clock); err != nil {
		t.Fatal(err)
	}
	if after := gcSchedule(t, db); after != before {
		t.Fatalf("reapply or restart rescheduled: %+v, was %+v", after, before)
	}
	if passes := w.fire(t, day1.Add(time.Hour)); len(passes) != 0 {
		t.Fatalf("a pass ran between fires: %+v", passes)
	}

	// Past the retention period the next fire purges the entries and
	// collects bytes nothing references; the copy keeps the shared bytes.
	day2 := day1.Add(24 * time.Hour)
	passes := w.fire(t, day2)
	if len(passes) != 1 || passes[0].TrashPurged != 2 || passes[0].BlobsDeleted != 1 {
		t.Fatalf("expiry fire %+v", passes)
	}
	if w.stored(t, gone) || !w.stored(t, shared) || !w.stored(t, kept) {
		t.Fatalf("expiry collected the wrong content: gone=%v shared=%v kept=%v",
			w.stored(t, gone), w.stored(t, shared), w.stored(t, kept))
	}

	// With the trash can off a delete is final, so the bytes are collected
	// at the next fire once the grace period has passed.
	if res := w.apply(t, artifactsapi.StoragePolicyChange{TrashEnabled: ptr(false)}); res.GCChanged || !res.TrashChanged {
		t.Fatalf("disable %+v", res)
	}
	off := w.deploy(t, "off.txt", "deleted for good")
	w.remove(t, "off.txt")
	if entries, err := w.trash.Entries(ctx, "libs", "off.txt"); err != nil || len(entries) != 0 {
		t.Fatalf("a delete with the trash can off trashed %v, %v", entries, err)
	}
	if passes := w.fire(t, day2.Add(24*time.Hour)); len(passes) != 1 || w.stored(t, off) {
		t.Fatalf("disabled-trash fire %+v, stored %v", passes, w.stored(t, off))
	}

	// Fires missed while no node ran are all queued, but run one pass.
	day4 := day2.Add(48 * time.Hour)
	day7 := day4.Add(72 * time.Hour)
	w.clock = day7
	w.passes = nil
	missed := w.queue(t)
	w.execute(t, missed)
	if len(missed) != 4 || len(w.passes) != 1 {
		t.Fatalf("catch-up queued %d and ran %d passes", len(missed), len(w.passes))
	}

	// A schedule changed while an occurrence is queued supersedes it: the old
	// occurrence finishes without a pass, and the new expression fires.
	day8 := day4.Add(96 * time.Hour)
	w.clock = day8
	w.passes = nil
	stale := w.queue(t)
	if len(stale) != 1 {
		t.Fatalf("queued %d", len(stale))
	}
	if res := w.apply(t, artifactsapi.StoragePolicyChange{GCCron: ptr("0 30 * * * ?")}); !res.GCChanged || res.TrashChanged {
		t.Fatalf("reschedule %+v", res)
	}
	w.execute(t, stale)
	if len(w.passes) != 0 {
		t.Fatalf("a superseded occurrence ran %+v", w.passes)
	}
	if passes := w.fire(t, day8.Add(30*time.Minute)); len(passes) != 1 {
		t.Fatalf("new schedule fired %d passes", len(passes))
	}

	// A policy applied in a transaction that then fails leaves the settings
	// and the schedule, and queues nothing.
	scheduled := gcSchedule(t, db)
	err := db.Tx(ctx, func(ctx context.Context) error {
		if _, err := w.policies.Apply(ctx, artifactsapi.StoragePolicyChange{
			GCCron: ptr("* * * * * ?"), TrashEnabled: ptr(true), TrashRetentionDays: ptr(30),
		}, w.clock); err != nil {
			return err //nolint:wrapcheck // the test compares it
		}
		return errPolicyBoom
	})
	if !errors.Is(err, errPolicyBoom) {
		t.Fatal(err)
	}
	if got := gcSchedule(t, db); got != scheduled {
		t.Fatalf("rolled-back schedule %+v", got)
	}
	if got, _ := w.policies.Current(ctx); got.TrashEnabled || got.GCCron != "0 30 * * * ?" {
		t.Fatalf("rolled-back policy %+v", got)
	}
	if passes := w.fire(t, w.clock.Add(time.Second)); len(passes) != 0 {
		t.Fatalf("a rolled-back schedule fired %+v", passes)
	}
	var stored model.TrashcanSettings
	if err := db.Settings().Get(ctx, settings.SettingTrashcan, &stored); err != nil || stored.Enabled {
		t.Fatalf("stored trash can settings %+v, %v", stored, err)
	}
}
