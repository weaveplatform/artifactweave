package federation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const (
	pullFaultPath = "selected/file.bin"
	pullFaultBody = "verified selected peer bytes"
)

func newPullDriverWorld(t *testing.T, backend string, noStore bool) (*pushWorld, replication.Pull, model.PullRun) {
	t.Helper()
	w := newPushBackendWorld(t, backend)
	w.request(w.target, http.MethodPut, itemRoute("target", pullFaultPath), pullFaultBody, http.StatusCreated)
	configurePullRemote(t, w, map[string]any{"storeArtifactsLocally": !noStore})
	if err := w.runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{Name: "pull-current-reader", Resources: map[accessapi.ResourceType]accesstest.Resource{
		accessapi.ResourceArtifact: {
			Targets: map[string]accesstest.Target{"pull-remote": {Include: []string{"selected/**"}}},
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"replica-reader": accessapi.ActionRead | accessapi.ActionWrite | accessapi.ActionDelete}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	w.request(w.source, http.MethodPut, replicationPath+"pull-remote", `{"enabled":true,"syncProperties":false,"includePathPrefixPattern":"**/*.bin"}`, http.StatusCreated)
	targets, err := w.runtime.Store.Replications().ForRepo(t.Context(), "pull-remote")
	if err != nil || len(targets) != 1 {
		t.Fatal("pull target fixture", err)
	}
	principal, err := w.runtime.Auth.Basic(t.Context(), "replica-reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	driver, err := w.runtime.Federation.Puller()
	if err != nil {
		t.Fatal(err)
	}
	run, err := driver.Start(accessapi.WithCaller(t.Context(), principal, nil), replication.PullRequest{
		ID: "qualified-pull", TargetKey: targets[0].Key, TargetRevision: targets[0].Revision, Root: "selected",
	}, nil)
	if noStore {
		if !errors.Is(err, artifactsapi.ErrPullNotStored) || w.bridge.count(http.MethodGet, itemRoute("target", pullFaultPath)) != 0 ||
			w.bridge.count(http.MethodGet, "/artifactory/api/storage/target/selected") != 0 {
			t.Fatal("no-store admission reached the peer or hid its refusal category", err)
		}
		assertNoPullCache(t, w)
		return w, driver, model.PullRun{}
	}
	if err != nil {
		t.Fatal("admit fault qualification pull", err)
	}
	return w, driver, run
}

func configurePullRemote(t *testing.T, w *pushWorld, options map[string]any) {
	t.Helper()
	configuration := map[string]any{
		"rclass": "remote", "packageType": "generic", "url": w.bridge.URL + "/artifactory/target", "username": "receiver", "password": "pw",
	}
	for key, value := range options {
		configuration[key] = value
	}
	body, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	w.request(w.source, http.MethodPut, "/artifactory/api/repositories/pull-remote", string(body), http.StatusOK)
}

func pendingPullFile(t *testing.T, w *pushWorld, run model.PullRun) model.PullItem {
	t.Helper()
	item, err := w.runtime.Store.PullProgress().Item(t.Context(), run.Reference, model.PullFetch, pullFaultPath)
	if err != nil {
		t.Fatal("retained selected pull intent", err)
	}
	return item
}

func assertNoPullCache(t *testing.T, w *pushWorld) {
	t.Helper()
	if _, err := w.runtime.Store.Items().Get(t.Context(), "pull-remote", true, pullFaultPath); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("refused pull published cache content", err)
	}
}

func TestAccSinglePullBlockedPublicationSQLite(t *testing.T) {
	qualifyPullBlockedPublication(t, "sqlite")
}

func TestAccSinglePullBlockedPublicationPostgres(t *testing.T) {
	qualifyPullBlockedPublication(t, "postgres")
}

func qualifyPullBlockedPublication(t *testing.T, backend string) {
	t.Helper()
	for _, change := range []string{"permissions", "caller-credential", "source-credentials", "source-url", "source-recreated", "disabled", "lease-lost"} {
		t.Run(change, func(t *testing.T) {
			w, driver, run := newPullDriverWorld(t, backend, false)
			if _, err := driver.Step(t.Context(), run.Reference, w.fence); err != nil {
				t.Fatal("select durable intent", err)
			}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var signal, unblock sync.Once
			joined := false
			t.Cleanup(func() {
				unblock.Do(func() { close(release) })
				if !joined {
					select {
					case <-done:
					case <-time.After(15 * time.Second):
						t.Error("owned pull worker did not join")
					}
				}
			})
			w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodGet || request.URL.Path != itemRoute("target", pullFaultPath) {
					return false
				}
				signal.Do(func() { close(entered) })
				select {
				case <-release:
				case <-request.Context().Done():
					return true
				}
				w.bridge.proxy.ServeHTTP(writer, request)
				return true
			})
			var alive atomic.Bool
			alive.Store(true)
			fence := func(ctx context.Context) error {
				if !alive.Load() {
					return kernel.ErrConflict
				}
				return w.fence(ctx)
			}
			go func() { _, err := driver.Step(t.Context(), run.Reference, fence); done <- err }()
			select {
			case <-entered:
			case err := <-done:
				joined = true
				t.Fatal("pull ended before external byte transfer", err)
			case <-time.After(15 * time.Second):
				t.Fatal("pull did not reach external byte gate")
			}
			switch change {
			case "permissions":
				if err := w.runtime.Store.Permissions().Delete(t.Context(), "pull-current-reader"); err != nil {
					t.Fatal(err)
				}
			case "caller-credential":
				w.request(w.source, http.MethodPut, "/artifactory/api/security/users/replica-reader",
					`{"email":"replica-reader@example.invalid","password":"replacement-password"}`, http.StatusCreated)
			case "source-credentials":
				w.request(w.source, http.MethodPost, "/artifactory/api/repositories/pull-remote", `{"password":"replacement-peer-password"}`, http.StatusOK)
			case "source-url":
				body, err := json.Marshal(map[string]string{"url": w.bridge.URL + "/artifactory/replaced-source"})
				if err != nil {
					t.Fatal(err)
				}
				w.request(w.source, http.MethodPost, "/artifactory/api/repositories/pull-remote", string(body), http.StatusOK)
			case "source-recreated":
				w.request(w.source, http.MethodDelete, "/artifactory/api/repositories/pull-remote", "", http.StatusOK)
				configurePullRemote(t, w, nil)
				w.request(w.source, http.MethodPut, replicationPath+"pull-remote", `{"enabled":true,"syncProperties":false}`, http.StatusCreated)
			case "disabled":
				w.request(w.source, http.MethodPost, replicationPath+"pull-remote", `{"enabled":false}`, http.StatusOK)
			case "lease-lost":
				alive.Store(false)
			}
			unblock.Do(func() { close(release) })
			select {
			case err := <-done:
				joined = true
				if err == nil {
					t.Fatal("obsolete blocked pull was accepted")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("released pull did not finish")
			}
			assertNoPullCache(t, w)
			item := pendingPullFile(t, w, run)
			if item.Phase != model.PullIntent || item.Cache != nil || item.Outcome != "" {
				t.Fatal("obsolete worker published a terminal receipt")
			}
			before := w.bridge.count(http.MethodGet, itemRoute("target", pullFaultPath))
			if _, err := driver.Step(t.Context(), run.Reference, fence); err == nil || w.bridge.count(http.MethodGet, itemRoute("target", pullFaultPath)) != before {
				t.Fatal("revoked pull reused old authority to reach the peer", err)
			}
		})
	}
}

func TestAccSinglePullInvalidBytesSQLite(t *testing.T)   { qualifyPullInvalidBytes(t, "sqlite") }
func TestAccSinglePullInvalidBytesPostgres(t *testing.T) { qualifyPullInvalidBytes(t, "postgres") }

func qualifyPullInvalidBytes(t *testing.T, backend string) {
	t.Helper()
	for _, failure := range []string{"truncated", "corrupt", "no-store"} {
		t.Run(failure, func(t *testing.T) {
			w, driver, run := newPullDriverWorld(t, backend, failure == "no-store")
			if failure == "no-store" {
				return // admission itself refused before any network work
			}
			if _, err := driver.Step(t.Context(), run.Reference, w.fence); err != nil {
				t.Fatal("select fault intent", err)
			}
			if failure != "no-store" {
				w.bridge.intercept(func(writer http.ResponseWriter, request *http.Request) bool {
					if request.Method != http.MethodGet || request.URL.Path != itemRoute("target", pullFaultPath) {
						return false
					}
					writer.Header().Set("Content-Type", "application/octet-stream")
					writer.Header().Set("Content-Length", strconv.Itoa(len(pullFaultBody)))
					body := strings.Repeat("x", len(pullFaultBody))
					if failure == "truncated" {
						body = pullFaultBody[:len(pullFaultBody)-1]
					}
					_, _ = writer.Write([]byte(body))
					return true
				})
			}
			_, _ = driver.Step(t.Context(), run.Reference, w.fence)
			assertNoPullCache(t, w)
			item := pendingPullFile(t, w, run)
			if item.Outcome == model.PullSucceeded || item.Cache != nil {
				t.Fatal("incomplete/corrupt/transient bytes became a successful cache receipt")
			}
		})
	}
}

func TestAccSinglePullOfflineSQLite(t *testing.T)   { qualifyPullOffline(t, "sqlite") }
func TestAccSinglePullOfflinePostgres(t *testing.T) { qualifyPullOffline(t, "postgres") }

func qualifyPullOffline(t *testing.T, backend string) {
	t.Helper()
	w, driver, run := newPullDriverWorld(t, backend, false)
	w.request(w.source, http.MethodPost, "/artifactory/api/repositories/pull-remote", `{"offline":true}`, http.StatusOK)
	if _, err := driver.Step(t.Context(), run.Reference, w.fence); !errors.Is(err, artifactsapi.ErrPullOffline) {
		t.Fatal("offline owning source did not retain its explicit refusal", err)
	}
	if w.bridge.count(http.MethodGet, itemRoute("target", pullFaultPath)) != 0 ||
		w.bridge.count(http.MethodGet, "/artifactory/api/storage/target/selected") != 0 {
		t.Fatal("offline replication performed metadata or payload network work")
	}
	assertNoPullCache(t, w)
}

func TestAccSinglePullRestartSQLite(t *testing.T)   { qualifyPullRestart(t, "sqlite") }
func TestAccSinglePullRestartPostgres(t *testing.T) { qualifyPullRestart(t, "postgres") }

func qualifyPullRestart(t *testing.T, backend string) {
	t.Helper()
	w, driver, run := newPullDriverWorld(t, backend, false)
	w.request(w.target, http.MethodPut, itemRoute("target", "selected/z.bin"), "second peer file", http.StatusCreated)
	for step := 0; step < 2; step++ {
		var err error
		run, err = driver.Step(t.Context(), run.Reference, w.fence)
		if err != nil {
			t.Fatal("commit first cache receipt", err)
		}
	}
	if item := pendingPullFile(t, w, run); item.Outcome != model.PullSucceeded {
		t.Fatal("restart fixture had no previously committed cache progress")
	}
	w.restartSource()
	var err error
	driver, err = w.runtime.Federation.Puller()
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 12 && !run.Phase.Terminal(); step++ {
		run, err = driver.Step(t.Context(), run.Reference, w.fence)
		if err != nil {
			t.Fatal("resume pull after process reopen", err)
		}
	}
	items, next, err := w.runtime.Store.PullProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 10})
	if err != nil || next != "" || len(items) != 2 || run.Phase != model.PullComplete {
		t.Fatal("reopened pull lost durable traversal or receipts", err)
	}
	for _, item := range items {
		if item.Outcome != model.PullSucceeded || item.Cache == nil || w.bridge.count(http.MethodGet, itemRoute("target", item.Path)) != 1 {
			t.Fatal("restart repeated a committed transfer or fabricated success")
		}
	}
}

func TestAccSinglePullPolicyRecoverySQLite(t *testing.T) { qualifyPullPolicyRecovery(t, "sqlite") }

func TestAccSinglePullPolicyRecoveryPostgres(t *testing.T) { qualifyPullPolicyRecovery(t, "postgres") }

func qualifyPullPolicyRecovery(t *testing.T, backend string) {
	t.Helper()
	w, driver, initial := newPullDriverWorld(t, backend, false)
	finish := func(run model.PullRun) model.PullRun {
		t.Helper()
		var err error
		for step := 0; step < 20 && !run.Phase.Terminal(); step++ {
			run, err = driver.Step(t.Context(), run.Reference, w.fence)
			if err != nil {
				t.Fatal("recover current pull", err)
			}
		}
		if run.Phase != model.PullComplete {
			t.Fatal("current pull did not finish", run.Phase)
		}
		return run
	}
	initial = finish(initial)
	payloads := w.bridge.count(http.MethodGet, itemRoute("target", pullFaultPath))
	w.request(w.source, http.MethodPost, "/artifactory/api/repositories/pull-remote", `{"retrievalCachePeriodSecs":12345}`, http.StatusOK)
	principal, err := w.runtime.Auth.Basic(t.Context(), "replica-reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	start := func(id string) model.PullRun {
		t.Helper()
		targets, err := w.runtime.Store.Replications().ForRepo(t.Context(), "pull-remote")
		if err != nil || len(targets) != 1 {
			t.Fatal("current replication target", err)
		}
		run, err := driver.Start(
			accessapi.WithCaller(t.Context(), principal, nil),
			replication.PullRequest{ID: id, TargetKey: targets[0].Key, TargetRevision: targets[0].Revision, Root: "selected"},
			nil,
		)
		if err != nil {
			t.Fatal("admit fresh pull", err)
		}
		return run
	}
	warm := finish(start("policy-warm-recovery"))
	if warm.Binding.Source.Revision == initial.Binding.Source.Revision || warm.Binding.Source.Repository.Generation != initial.Binding.Source.Repository.Generation {
		t.Fatal("policy edit lost distinct run fencing or changed byte identity")
	}
	item, err := w.runtime.Store.PullProgress().Item(t.Context(), warm.Reference, model.PullFetch, pullFaultPath)
	if err != nil || item.Outcome != model.PullSucceeded || w.bridge.count(http.MethodGet, itemRoute("target", pullFaultPath)) != payloads {
		t.Fatal("fresh policy pull did not checkpoint warm verified bytes", err)
	}
	source := w.runtime.Artifacts.API().PullSource
	if _, _, err := source.Items(
		accessapi.WithCaller(t.Context(), principal, nil),
		initial.Binding.Source,
		initial.Binding.Selection(),
		kernel.Page{},
	); !errors.Is(
		err,
		artifactsapi.ErrPullChanged,
	) {
		t.Fatal("old run retained policy authority", err)
	}
	// A new source credential generation can retire old bytes only after exact
	// current-peer absence. Neither the old job nor a listing omission grants it.
	w.request(w.target, http.MethodDelete, itemRoute("target", pullFaultPath), "", http.StatusNoContent)
	w.request(w.source, http.MethodPost, "/artifactory/api/repositories/pull-remote", `{"username":"receiver-readonly"}`, http.StatusOK)
	w.request(w.source, http.MethodPost, replicationPath+"pull-remote", `{"syncDeletes":true}`, http.StatusOK)
	reconciled := finish(start("generation-reconciliation"))
	removed, err := w.runtime.Store.PullProgress().Item(t.Context(), reconciled.Reference, model.PullDelete, pullFaultPath)
	if err != nil || removed.Outcome != model.PullSucceeded || len(removed.Removed) == 0 {
		t.Fatal("fresh source generation could not retire absent earlier cache", err)
	}
	assertNoPullCache(t, w)
	if w.bridge.count(http.MethodDelete, itemRoute("target", pullFaultPath)) != 0 {
		t.Fatal("cache reconciliation sent upstream DELETE")
	}
}
