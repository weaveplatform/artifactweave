package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

var errScheduleDiscoveryFixture = errors.New("injected schedule discovery failure")

type retryScheduleStore struct {
	schedule.Store
	calls   int
	retried chan struct{}
}

func (s *retryScheduleStore) Due(context.Context, time.Time, int) ([]schedule.State, error) {
	s.calls++
	if s.calls == 1 {
		return nil, errScheduleDiscoveryFixture
	}
	if s.calls == 2 {
		close(s.retried)
	}
	return nil, nil
}

type retryScheduleBackend struct {
	*inmem.Store
	schedules *retryScheduleStore
}

func (s retryScheduleBackend) Schedules() schedule.Store { return s.schedules }

func TestUnitScheduleDiscoveryRetriesTransientFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := inmem.New()
		rows := &retryScheduleStore{Store: db.Schedules(), retried: make(chan struct{})}
		service := &schedule.Service{Store: retryScheduleBackend{Store: db, schedules: rows}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runSchedules(ctx, service, slog.New(slog.DiscardHandler)) }()
		<-rows.retried
		cancel()
		<-done
		if rows.calls != 2 {
			t.Fatal("transient discovery failure did not resume polling", rows.calls)
		}
	})
}

func cleanupRuntime(t *testing.T, cfg config.Config) (*Runtime, func()) {
	t.Helper()
	rt, err := OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	closeRuntime := func() {
		t.Helper()
		if closed {
			return
		}
		closed = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := rt.Close(ctx); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(closeRuntime)
	return rt, closeRuntime
}

func cleanupRuntimeRequest(t *testing.T, rt *Runtime, method, path, body string, want int, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewRouter(
		RouterConfig{
			Store:          rt.Store,
			Blobs:          rt.Blobs,
			Auth:           rt.Auth,
			Tokens:         rt.Tokens,
			Access:         rt.modules.access.API(),
			Work:           rt.Work,
			Retention:      rt.modules.retention,
			FormatSettings: rt.Artifacts.FormatSettings(),
			Logger:         slog.New(slog.DiscardHandler),
		},
	)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth("admin", "cleanup-runtime-password")
	if len(headers)%2 != 0 {
		t.Fatal("runtime request headers need name/value pairs")
	}
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func seedCleanupRuntime(t *testing.T, rt *Runtime, cron string) {
	t.Helper()
	cleanupRuntimeRequest(t, rt, http.MethodPut, "/artifactory/api/repositories/runtime-cleanup", `{"rclass":"local","packageType":"maven"}`, http.StatusOK)
	for _, version := range []string{"1.0", "2.0"} {
		cleanupRuntimeRequest(t, rt, http.MethodPut, "/artifactory/runtime-cleanup/com/acme/app/"+version+"/app-"+version+".pom", "<project/>", http.StatusCreated)
	}
	policy := map[string]any{
		"key":     "runtime-policy",
		"cronExp": cron,
		"searchCriteria": map[string]any{
			"packageTypes":      []string{"maven"},
			"repos":             []string{"runtime-cleanup"},
			"includedPackages":  []string{"**"},
			"includedProjects":  []string{"default"},
			"keepLastNVersions": 1,
		},
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRuntimeRequest(t, rt, http.MethodPost, "/artifactory/api/cleanup/packages/policies/runtime-policy", string(raw), http.StatusCreated)
	cleanupRuntimeRequest(t, rt, http.MethodPost, "/artifactory/api/cleanup/packages/policies/runtime-policy/enablement", `{"enabled":true}`, http.StatusOK)
}

func waitCleanupRuntime(t *testing.T, rt *Runtime, id int64) model.CleanupRun {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		run, err := rt.Store.Cleanup().Run(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status.Terminal() {
			return run
		}
		select {
		case <-deadline.C:
			t.Fatal("registered cleanup worker did not settle admitted work")
			return model.CleanupRun{}
		case <-tick.C:
		}
	}
}

func TestAccRuntimeCleanupAdmissionSurvivesRestart(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "cleanup-runtime-password"
	first, closeFirst := cleanupRuntime(t, cfg)
	// Stop execution without closing persistence, then admit through the real
	// router. This establishes restart-before-work without timing a worker race.
	first.stopLoops()
	first.loops.Wait()
	seedCleanupRuntime(t, first, "")
	r := cleanupRuntimeRequest(
		t,
		first,
		http.MethodPut,
		"/artifactory/api/cleanup/packages/policies/runtime-policy/run",
		"",
		http.StatusAccepted,
		"Idempotency-Key",
		`"restart-cleanup"`,
	)
	var trigger artgen.CleanupRunTrigger
	if err := json.Unmarshal(r.Body.Bytes(), &trigger); err != nil || trigger.TriggerId == nil {
		t.Fatal("admission did not return an integer trigger", err)
	}
	id := int64(*trigger.TriggerId)
	queued, err := first.Store.Cleanup().Run(t.Context(), id)
	if err != nil || queued.Status != api.RunInProgress || !queued.Started.IsZero() {
		t.Fatal("fixture did not retain queued work before restart", err)
	}
	closeFirst()
	second, _ := cleanupRuntime(t, cfg)
	finished := waitCleanupRuntime(t, second, id)
	if finished.Status != api.RunCompleted || finished.TriggerID != id || finished.CleanedItems != 1 {
		t.Fatalf("restarted run: status=%s trigger=%d cleaned=%d", finished.Status, finished.TriggerID, finished.CleanedItems)
	}
	replay := cleanupRuntimeRequest(
		t,
		second,
		http.MethodPut,
		"/artifactory/api/cleanup/packages/policies/runtime-policy/run",
		"",
		http.StatusAccepted,
		"Idempotency-Key",
		`"restart-cleanup"`,
	)
	if replay.Body.String() != r.Body.String() {
		t.Fatal("restart lost the durable admission receipt")
	}
	cleanupRuntimeRequest(t, second, http.MethodGet, "/artifactory/runtime-cleanup/com/acme/app/1.0/app-1.0.pom", "", http.StatusNotFound)
	cleanupRuntimeRequest(t, second, http.MethodGet, "/artifactory/runtime-cleanup/com/acme/app/2.0/app-2.0.pom", "", http.StatusOK)
}

func TestAccRuntimeDiscoversCleanupSchedule(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "cleanup-runtime-password"
	rt, _ := cleanupRuntime(t, cfg)
	seedCleanupRuntime(t, rt, "0/1 * * * * ?")
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var run model.CleanupRun
	for {
		candidate, err := rt.Store.Cleanup().LastRun(t.Context(), "runtime-policy")
		if err == nil {
			run = candidate
			break
		}
		if !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("runtime never discovered the enabled durable cleanup schedule")
			return
		case <-tick.C:
		}
	}
	finished := waitCleanupRuntime(t, rt, run.ID)
	if finished.Status != api.RunCompleted || finished.Manual || finished.CleanedItems != 1 {
		t.Fatalf("scheduled cleanup: status=%s manual=%v cleaned=%d", finished.Status, finished.Manual, finished.CleanedItems)
	}
	cleanupRuntimeRequest(t, rt, http.MethodPost, "/artifactory/api/cleanup/packages/policies/runtime-policy/enablement", `{"enabled":false}`, http.StatusOK)
	cleanupRuntimeRequest(t, rt, http.MethodGet, "/artifactory/runtime-cleanup/com/acme/app/1.0/app-1.0.pom", "", http.StatusNotFound)
}
