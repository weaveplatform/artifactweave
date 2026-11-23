package quota_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/quota"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errFixture = errors.New("fixture failure")

// volume is a deterministic measured capacity: tests set exactly what the
// next measurement reports.
type volume struct {
	mu          sync.Mutex
	total, used int64
	err         error
	calls       int
}

func (v *volume) Measure(context.Context) (int64, int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	return v.total, v.used, v.err
}

func (v *volume) set(total, used int64, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.total, v.used, v.err = total, used, err
}

func putPolicy(t *testing.T, db *inmem.Store, doc string) {
	t.Helper()
	if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: quota.ConfigKind, Key: quota.ConfigKey, Enabled: true, Document: []byte(doc),
	}); err != nil {
		t.Fatal(err)
	}
}

func admit(t *testing.T, db *inmem.Store, a quota.Admission, delta int64) error {
	t.Helper()
	return db.Tx(t.Context(), func(ctx context.Context) error { return a.Admit(ctx, delta) })
}

func check(t *testing.T, db *inmem.Store, a quota.Admission) error {
	t.Helper()
	return quota.Monitor{Admission: a, Tx: db, Guard: db.Projects().Guard}.Check(t.Context())
}

type occurrence struct {
	item  work.WorkItem
	Level string `json:"level"`
	Key   string `json:"key"`
	Used  int64  `json:"used"`
}

// queued leases every pending warning, so each is seen once.
func queued(t *testing.T, db *inmem.Store) []occurrence {
	t.Helper()
	now := time.Now()
	items, err := db.WorkQueue().Lease(t.Context(), work.WorkLease{
		Kinds: []string{quota.WarningKind}, Holder: "test", Limit: 10, Now: now, Until: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]occurrence, 0, len(items))
	for _, item := range items {
		var o occurrence
		if err := json.Unmarshal(item.Payload, &o); err != nil {
			t.Fatal(err)
		}
		o.item = item
		out = append(out, o)
	}
	return out
}

func armed(t *testing.T, db *inmem.Store) map[string]string {
	t.Helper()
	var st map[string]string
	err := db.Settings().Get(t.Context(), settings.SettingStorageQuotaWarning, &st)
	if errors.Is(err, kernel.ErrNotFound) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUnitPolicyLevelsAndFields(t *testing.T) {
	p := quota.Policy{Enabled: true, LimitPercent: 95, Warning: 85}
	for _, tc := range []struct {
		total, used int64
		want        quota.Level
	}{
		{1000, 849, quota.Below},
		{1000, 850, quota.Warning},
		{1000, 949, quota.Warning},
		{1000, 950, quota.Limit},
		{3, 2, quota.Below},
		{1 << 62, 1<<62 - 1, quota.Limit},
		{100 << 50, 85 << 50, quota.Warning},
		{100 << 50, 85<<50 - 1, quota.Below},
	} {
		if got := p.Level(tc.total, tc.used); got != tc.want {
			t.Errorf("%d of %d: %v, want %v", tc.used, tc.total, got, tc.want)
		}
	}
	if (quota.Policy{Enabled: true}).Level(100, 0) != quota.Limit {
		t.Fatal("an enabled policy with the XSD's zero limit admitted growth; 7.x refuses (used >= 0)")
	}
	if (quota.Policy{LimitPercent: 50, Warning: 90}).Level(100, 60) != quota.Limit ||
		quota.Below.String() != "below" || quota.Warning.String() != "warning" || quota.Limit.String() != "limit" {
		t.Fatal("level names or limit precedence")
	}
	p, err := quota.CheckFields(map[string]any{
		quota.FieldEnabled: true, quota.FieldLimit: float64(90), quota.FieldWarning: json.Number("80"),
	})
	if err != nil || p != (quota.Policy{Enabled: true, LimitPercent: 90, Warning: 80}) {
		t.Fatal(p, err)
	}
	for _, bad := range []map[string]any{
		{quota.FieldWarning: json.Number("eighty")}, {quota.FieldLimit: json.Number("1e3")}, {quota.FieldLimit: int64(5)},
	} {
		if _, err := quota.CheckFields(bad); !errors.Is(err, configapi.ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
}

func TestUnitPolicyRead(t *testing.T) {
	db := inmem.New()
	if p, stored, err := quota.Read(t.Context(), db.Configs()); err != nil || stored || p != (quota.Policy{}) {
		t.Fatal("absent policy", p, stored, err)
	}
	for _, doc := range []string{`[]`, `null`, `{"enabled":"yes"}`} {
		putPolicy(t, db, doc)
		if _, _, err := quota.Read(t.Context(), db.Configs()); !errors.Is(err, kernel.ErrStore) {
			t.Fatal(doc, err)
		}
	}
	if _, _, err := quota.Read(t.Context(), failingConfigs{db.Configs()}); !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
}

func TestUnitAdmissionRefusesAtTheMeasuredLimit(t *testing.T) {
	v := &volume{total: 1000, used: 100}
	db := inmem.NewWithCapacity(v)
	a := db.StorageQuota()
	if err := admit(t, db, a, 10); err != nil || v.calls != 0 {
		t.Fatal("no policy measured or refused", err, v.calls)
	}
	putPolicy(t, db, `{"enabled":false,"diskSpaceLimitPercentage":1}`)
	if err := admit(t, db, a, 10); err != nil || v.calls != 0 {
		t.Fatal("a disabled policy measured or refused", err, v.calls)
	}
	putPolicy(t, db, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	v.set(1000, 950, nil)
	err := admit(t, db, a, 1)
	var refusal interface{ Error() string }
	if !errors.Is(err, kernel.ErrDiskQuota) || !errors.As(err, &refusal) {
		t.Fatal("growth at the limit", err)
	}
	// Reclaiming, net-zero and reads are never refused, not even measured.
	calls := v.calls
	for _, delta := range []int64{0, -1, -1000} {
		if err := admit(t, db, a, delta); err != nil {
			t.Fatal(delta, err)
		}
	}
	if v.calls != calls || len(armed(t, db)) != 0 || len(queued(t, db)) != 0 {
		t.Fatal("non-growth measured or a refused write left a warning", v.calls, armed(t, db))
	}
	v.set(1000, 949, nil)
	if err := admit(t, db, a, 1); err != nil {
		t.Fatal("growth below the limit", err)
	}
}

func TestUnitAdmissionFailsClosedWithoutAMeasurement(t *testing.T) {
	for name, a := range map[string]func(*inmem.Store) quota.Admission{
		"no capacity": func(db *inmem.Store) quota.Admission { return db.StorageQuota() },
		"failing": func(db *inmem.Store) quota.Admission {
			a := db.StorageQuota()
			a.Capacity = &volume{err: errFixture}
			return a
		},
		"implausible": func(db *inmem.Store) quota.Admission {
			a := db.StorageQuota()
			a.Capacity = &volume{total: 10, used: 11}
			return a
		},
	} {
		t.Run(name, func(t *testing.T) {
			db := inmem.New()
			putPolicy(t, db, `{"enabled":true,"diskSpaceLimitPercentage":95}`)
			if err := admit(t, db, a(db), 1); !errors.Is(err, kernel.ErrStorageCapacity) {
				t.Fatal(err)
			}
			if err := check(t, db, a(db)); !errors.Is(err, kernel.ErrStorageCapacity) {
				t.Fatal(err)
			}
			if err := admit(t, db, a(db), -1); err != nil {
				t.Fatal("reclamation needs no measurement", err)
			}
		})
	}
}

func TestUnitWarningsTransitionRearmAndSurviveRestart(t *testing.T) {
	v := &volume{total: 1000, used: 100}
	db := inmem.NewWithCapacity(v)
	a := db.StorageQuota()
	putPolicy(t, db, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	if err := admit(t, db, a, 1); err != nil || len(armed(t, db)) != 0 || len(queued(t, db)) != 0 {
		t.Fatal("below the warning", err)
	}
	v.set(1000, 850, nil)
	if err := admit(t, db, a, 1); err != nil {
		t.Fatal(err)
	}
	first := queued(t, db)
	if len(first) != 1 || first[0].Level != "warning" || first[0].Used != 850 || armed(t, db)["warning"] != first[0].Key {
		t.Fatal("warning occurrence", first, armed(t, db))
	}
	// A rolled-back write takes its occurrence with it.
	v.set(1000, 100, nil)
	err := db.Tx(t.Context(), func(ctx context.Context) error {
		if err := a.Admit(ctx, 1); err != nil {
			return err
		}
		return errFixture
	})
	if !errors.Is(err, errFixture) || armed(t, db)["warning"] != first[0].Key {
		t.Fatal("a rolled-back rearm committed", err, armed(t, db))
	}
	// A new Admission over the same store, as after a restart, raises nothing
	// again while the occurrence stays armed.
	v.set(1000, 900, nil)
	if err := admit(t, db, db.StorageQuota(), 1); err != nil || len(queued(t, db)) != 0 {
		t.Fatal("restart duplicated the warning", err)
	}
	// Only the monitor records the limit: a refused write rolls back.
	v.set(1000, 960, nil)
	if err := admit(t, db, a, 1); !errors.Is(err, kernel.ErrDiskQuota) || len(queued(t, db)) != 0 {
		t.Fatal(err)
	}
	if err := check(t, db, a); err != nil {
		t.Fatal(err)
	}
	limit := queued(t, db)
	if len(limit) != 1 || limit[0].Level != "limit" || armed(t, db)["limit"] != limit[0].Key || armed(t, db)["warning"] != first[0].Key {
		t.Fatal("limit occurrence", limit, armed(t, db))
	}
	if err := check(t, db, a); err != nil || len(queued(t, db)) != 0 {
		t.Fatal("a repeated check raised the limit again", err)
	}
	// Back into the warning band rearms the limit without a new warning.
	v.set(1000, 900, nil)
	if err := check(t, db, a); err != nil || len(queued(t, db)) != 0 || armed(t, db)["limit"] != "" {
		t.Fatal("falling from the limit", err, armed(t, db))
	}
	v.set(1000, 970, nil)
	if err := check(t, db, a); err != nil {
		t.Fatal(err)
	}
	if again := queued(t, db); len(again) != 1 || again[0].Key == limit[0].Key {
		t.Fatal("the rearmed limit was not raised afresh", again)
	}
	// Below both rearms both; a jump straight to the limit arms off the warning.
	v.set(1000, 10, nil)
	if err := check(t, db, a); err != nil || len(armed(t, db)) != 0 {
		t.Fatal("recovery", err, armed(t, db))
	}
	v.set(1000, 999, nil)
	if err := check(t, db, a); err != nil {
		t.Fatal(err)
	}
	if jumped := queued(t, db); len(jumped) != 1 || jumped[0].Level != "limit" || armed(t, db)["warning"] == "" {
		t.Fatal("a jump to the limit", jumped, armed(t, db))
	}
	// Disabling clears the state, so enabling again warns afresh.
	putPolicy(t, db, `{"enabled":false,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	if err := check(t, db, a); err != nil || len(armed(t, db)) != 0 {
		t.Fatal("disable", err, armed(t, db))
	}
	if err := check(t, db, a); err != nil {
		t.Fatal("a second disabled check", err)
	}
}

func TestUnitAdmissionStoreFailuresRefuse(t *testing.T) {
	v := &volume{total: 100, used: 90}
	db := inmem.NewWithCapacity(v)
	putPolicy(t, db, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	for name, a := range map[string]quota.Admission{
		"configs":  {Configs: failingConfigs{db.Configs()}, Settings: db.Settings(), Queue: db.WorkQueue(), Capacity: v},
		"state":    {Configs: db.Configs(), Settings: failingSettings{SettingsStore: db.Settings(), get: true}, Queue: db.WorkQueue(), Capacity: v},
		"save":     {Configs: db.Configs(), Settings: failingSettings{SettingsStore: db.Settings()}, Queue: db.WorkQueue(), Capacity: v},
		"no queue": {Configs: db.Configs(), Settings: db.Settings(), Capacity: v},
		"enqueue":  {Configs: db.Configs(), Settings: db.Settings(), Queue: failingQueue{db.WorkQueue()}, Capacity: v},
	} {
		t.Run(name, func(t *testing.T) {
			if err := admit(t, db, a, 1); err == nil {
				t.Fatal("a failed warning handoff admitted the write")
			}
			if len(armed(t, db)) != 0 {
				t.Fatal("a failed handoff kept its state", armed(t, db))
			}
		})
	}
	// Rearming needs its delete; a missing state is already rearmed.
	if err := db.Settings().Put(t.Context(), settings.SettingStorageQuotaWarning, map[string]string{"warning": "x"}); err != nil {
		t.Fatal(err)
	}
	v.set(100, 1, nil)
	a := quota.Admission{Configs: db.Configs(), Settings: failingSettings{SettingsStore: db.Settings(), del: true}, Queue: db.WorkQueue(), Capacity: v}
	if err := admit(t, db, a, 1); !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
}

func TestUnitMonitorRunsUntilCancelled(t *testing.T) {
	db := inmem.New()
	if err := (quota.Monitor{}).Check(t.Context()); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	m := quota.Monitor{Admission: db.StorageQuota(), Tx: db, Guard: func(context.Context) error { return errFixture }}
	if err := m.Check(t.Context()); !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
	putPolicy(t, db, `{"enabled":true}`)
	var logged sync.WaitGroup
	logged.Add(1)
	m = quota.Monitor{Admission: db.StorageQuota(), Tx: db, Guard: db.Projects().Guard, Log: logOnce(&logged)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { m.Run(ctx, time.Millisecond); close(done) }()
	logged.Wait()
	cancel()
	<-done
}

type failingConfigs struct{ configdoc.ConfigStore }

func (failingConfigs) Get(context.Context, configdoc.ConfigKind, string) (configdoc.ConfigDocument, error) {
	return configdoc.ConfigDocument{}, errFixture
}

type failingSettings struct {
	settings.SettingsStore
	get, del bool
}

func (f failingSettings) Get(ctx context.Context, key string, into any) error {
	if f.get {
		return errFixture
	}
	return f.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // transparent test double
}

func (f failingSettings) Put(ctx context.Context, key string, v any) error {
	if f.get || f.del {
		return f.SettingsStore.Put(ctx, key, v) //nolint:wrapcheck // transparent test double
	}
	return errFixture
}

func (f failingSettings) Delete(ctx context.Context, key string) error {
	if f.del {
		return errFixture
	}
	return f.SettingsStore.Delete(ctx, key) //nolint:wrapcheck // transparent test double
}

type failingQueue struct{ work.WorkQueue }

func (failingQueue) Enqueue(context.Context, string, string, []byte, time.Time) error {
	return errFixture
}
