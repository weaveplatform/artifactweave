package retention_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccCleanupRunRoutesReference(t *testing.T) { cleanupRunRoutes(t, inmem.New()) }

func TestAccCleanupRunRoutesSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) { cleanupRunRoutes(t, newStore(t)) },
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupRunRoutes(t *testing.T, db harness.Store) {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	storekit.Must(t, f.module.RegisterWork(f.queue))
	w := policyWorld{t: t, store: db, h: f.handler}
	const runPath = policies + "/execution-policy/run"
	const stopPath = "/artifactory/api/cleanup/policies/runs/"
	admit := func(path string) int64 {
		t.Helper()
		r := w.request(http.MethodPut, path, "admin", "", http.StatusAccepted)
		var trigger artgen.CleanupRunTrigger
		storekit.Must(t, json.Unmarshal(r.Body.Bytes(), &trigger))
		if trigger.TriggerId == nil || *trigger.TriggerId <= 0 {
			t.Fatal("HTTP admission lost integer trigger")
		}
		return int64(*trigger.TriggerId)
	}
	run := func(id int64) model.CleanupRun {
		t.Helper()
		return storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(f.ctx, id))
	}
	process := func() {
		t.Helper()
		n, err := f.queue.RunDue(f.ctx, cleanup.WorkKind)
		storekit.Must(t, err)
		if n != 1 {
			t.Fatal("registered cleanup worker did not process its admitted run", n)
		}
	}
	storekit.Must(t, f.policies.Enable(f.caller, "execution-policy", false))
	w.request(http.MethodPut, runPath, "admin", "", http.StatusBadRequest)
	stopped := admit(runPath + "?dryRun=true")
	if r := run(stopped); r.TriggerID != stopped || !r.DryRun || r.Status != retapi.RunInProgress {
		t.Fatal("HTTP admission was not durable and dry")
	}
	// A prior store fault may exhaust the queue's retries. An explicit stop
	// must still wake the real worker so STOPPING can become STOPPED.
	now := time.Now()
	parked, taken, err := db.WorkQueue().
		LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(stopped, 10), work.WorkLease{Holder: "failed-worker", Now: now, Until: now.Add(time.Minute)})
	storekit.Must(t, err)
	if !taken {
		t.Fatal("HTTP run was not queued")
	}
	storekit.Must(
		t,
		db.WorkQueue().Fail(f.ctx, parked.Kind, parked.Key, parked.Generation, work.WorkFailure{Park: true, Error: "store unavailable", RetryAt: now.Add(time.Hour), Now: now}),
	)
	w.request(
		http.MethodPut,
		policies+"/execution-policy",
		"admin",
		`{"key":"execution-policy","searchCriteria":{"packageTypes":["generic"],"repos":["execution"],"includedPackages":["**"],"includedProjects":["default"]}}`,
		http.StatusBadRequest,
	)
	w.request(http.MethodDelete, policies+"/execution-policy", "admin", "", http.StatusBadRequest)
	w.request(http.MethodPut, stopPath+strconv.FormatInt(stopped, 10), "admin", `{"action":"stop"}`, http.StatusAccepted)
	if r := run(stopped); r.Status != retapi.RunStopping || r.Status.Terminal() {
		t.Fatal("stop response falsely claimed completed settlement")
	}
	process()
	if r := run(stopped); r.Status != retapi.RunStopped || r.CleanedItems != 0 {
		t.Fatal("registered worker did not settle stop")
	}
	f.preserved(t)
	dry := admit(runPath + "?dryRun=true")
	process()
	if r := run(dry); r.Status != retapi.RunCompleted || r.TotalItems != 2 || r.CleanedItems != 0 {
		t.Fatal("disabled dry run had incorrect effects")
	}
	f.preserved(t)
	w.request(http.MethodPost, policies+"/execution-policy/enablement", "admin", `{"enabled":true}`, http.StatusOK)
	actual := admit(runPath)
	if actual == dry || actual == stopped {
		t.Fatal("distinct HTTP runs coalesced")
	}
	w.request(http.MethodPut, runPath, "admin", "", http.StatusBadRequest)
	process()
	if r := run(actual); r.Status != retapi.RunCompleted || r.CleanedItems != 2 {
		t.Fatal("default dryRun=false did not execute real cleanup")
	}
	for _, id := range f.ids {
		_, err := db.Items().GetByID(f.ctx, id)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
	w.request(http.MethodPut, stopPath+strconv.FormatInt(actual, 10), "admin", `{"action":"stop"}`, http.StatusAccepted)
	for _, id := range []string{"0", "-1", "999999"} {
		want := http.StatusBadRequest
		if id == "999999" {
			want = http.StatusNotFound
		}
		w.request(http.MethodPut, stopPath+id, "admin", `{"action":"stop"}`, want)
	}
	w.request(http.MethodPut, stopPath+strconv.FormatInt(actual, 10), "admin", `{"action":"resume"}`, http.StatusBadRequest)
	keyedHeaders := support.Basic("admin", "pw")
	keyedHeaders["Idempotency-Key"] = `"cleanup-admission"`
	first := support.Do(t, f.handler, http.MethodPut, runPath+"?dryRun=true", keyedHeaders, "")
	retry := support.Do(t, f.handler, http.MethodPut, runPath+"?dryRun=true", keyedHeaders, "")
	if first.Code != http.StatusAccepted || retry.Code != http.StatusAccepted || first.Body.String() != retry.Body.String() {
		t.Fatal("keyed retry failed to retain one accepted trigger", first.Code, retry.Code)
	}
	mismatch := support.Do(t, f.handler, http.MethodPut, runPath+"?dryRun=false", keyedHeaders, "")
	if mismatch.Code != http.StatusUnprocessableEntity {
		t.Fatal("keyed retry changed its dry-run selection", mismatch.Code)
	}
	rows, _, err := db.Cleanup().Runs(f.ctx, retport.RunFilter{PolicyKey: "execution-policy"}, kernel.Page{Limit: 100})
	storekit.Must(t, err)
	if len(rows) != 4 {
		t.Fatal("keyed replay created another run", len(rows))
	}
	process()
	completedReplay := support.Do(t, f.handler, http.MethodPut, runPath+"?dryRun=true", keyedHeaders, "")
	if completedReplay.Code != http.StatusAccepted || completedReplay.Body.String() != first.Body.String() {
		t.Fatal("completed admission no longer replayed its retained trigger", completedReplay.Code)
	}
}

func TestAccCleanupRunRoutesRespectProjectAuthority(t *testing.T) {
	w := newWorld(t, inmem.New())
	const stopPath = "/artifactory/api/cleanup/policies/runs/"
	ids := map[string]int{}
	for _, project := range []string{"alpha", "beta"} {
		key := project + "-run-policy"
		w.request(http.MethodPost, policies+"/"+key, "admin", policyBody(key, project, project+"-npm"), http.StatusCreated)
		r := w.request(http.MethodPut, policies+"/"+key+"/run?dryRun=true", "admin", "", http.StatusAccepted)
		var trigger artgen.CleanupRunTrigger
		storekit.Must(t, json.Unmarshal(r.Body.Bytes(), &trigger))
		if trigger.TriggerId == nil {
			t.Fatal("missing project trigger")
		}
		ids[project] = *trigger.TriggerId
	}
	w.request(http.MethodPut, stopPath+strconv.Itoa(ids["beta"]), "cleanup-owner", `{"action":"stop"}`, http.StatusForbidden)
	w.request(http.MethodPut, stopPath+strconv.Itoa(ids["alpha"]), "cleanup-owner", `{"action":"stop"}`, http.StatusAccepted)
	w.request(http.MethodPut, stopPath+"forceStopAll", "cleanup-owner", "", http.StatusForbidden)
	for _, user := range []string{"", "cleanup-user"} {
		want := http.StatusForbidden
		if user == "" {
			want = http.StatusUnauthorized
		}
		w.request(http.MethodPut, policies+"/beta-run-policy/run", user, "", want)
		w.request(http.MethodPut, stopPath+strconv.Itoa(ids["beta"]), user, `{"action":"stop"}`, want)
		w.request(http.MethodPut, stopPath+"forceStopAll", user, "", want)
	}
	w.request(http.MethodPut, stopPath+"forceStopAll", "admin", "", http.StatusAccepted)
	for _, id := range ids {
		r := storekit.Get[model.CleanupRun](t)(w.store.Cleanup().Run(t.Context(), int64(id)))
		if r.Status != retapi.RunStopping {
			t.Fatal("force-stop did not request every owned run")
		}
	}
}
