package federation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// The 7.161.26 resource checks base configuration even with no federated
// repositories and keeps the same complete response shapes on its 400 branch.
func TestAccFederatedStateReportsBaseConfiguration(t *testing.T) {
	testFederatedStateReportsBaseConfiguration(t, func(t *testing.T, base string) http.Handler { return newFederatedWorld(t, base).h })
}

func TestAccFederatedStateReportsBaseConfigurationSQLite(t *testing.T) {
	testFederatedStateReportsBaseConfiguration(t, func(t *testing.T, base string) http.Handler { return stateReportsSQLRouter(t, "sqlite", "", base) })
}

func TestAccFederatedStateReportsBaseConfigurationPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	testFederatedStateReportsBaseConfiguration(t, func(t *testing.T, base string) http.Handler {
		return stateReportsSQLRouter(t, "postgres", sqltest.ScratchDSN(t, dsn), base)
	})
}

func stateReportsSQLRouter(t *testing.T, driver, dsn, base string) http.Handler {
	t.Helper()
	runtime := harness.OpenApp(t, pushConfig(t, driver, dsn), harness.AppOptions())
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "dev", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	return harness.Router(
		harness.Config{
			Store:        runtime.Store,
			Blobs:        runtime.Blobs,
			Auth:         runtime.Auth,
			Tokens:       runtime.Tokens,
			Keys:         runtime.Keys,
			PasswordHash: support.TestHash,
			BaseURL:      base,
			Logger:       slog.New(slog.DiscardHandler),
		},
	)
}

func testFederatedStateReportsBaseConfiguration(t *testing.T, newRouter func(*testing.T, string) http.Handler) {
	t.Helper()
	for _, configured := range []bool{false, true} {
		for _, endpoint := range []string{"stateList", "stateSummary"} {
			t.Run(fmt.Sprintf("configured=%t/%s", configured, endpoint), func(t *testing.T) {
				base := ""
				status := http.StatusBadRequest
				if configured {
					base, status = "https://a.example", http.StatusOK
				}
				router := newRouter(t, base)
				path := "/artifactory/api/federation/status/" + endpoint
				call := func(user string, want int) string {
					t.Helper()
					var headers map[string]string
					if user != "" {
						headers = support.Basic(user, "pw")
					}
					response := support.Do(t, router, http.MethodGet, path, headers, "")
					if response.Code != want {
						t.Fatalf("%s as %s: %d %s, want %d", path, user, response.Code, response.Body, want)
					}
					return response.Body.String()
				}
				call("", http.StatusUnauthorized)
				call("dev", http.StatusForbidden)
				raw := call("admin", status)
				var envelope map[string]json.RawMessage
				if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
					t.Fatal(err)
				}
				if len(envelope) != 1 {
					t.Fatalf("state envelope: %s", raw)
				}
				if endpoint == "stateList" {
					if string(envelope["federations"]) != "[]" {
						t.Fatalf("empty federation list: %s", raw)
					}
					return
				}
				var counts map[string]json.RawMessage
				if err := json.Unmarshal(envelope["federationStates"], &counts); err != nil {
					t.Fatal(err)
				}
				fields := []string{"HEALTHY", "READ_ONLY", "NOT_AVAILABLE", "DELAYED", "PENDING_FS", "FULL_SYNC_RUNNING", "ERROR", "DISABLED"}
				if len(counts) != len(fields) {
					t.Fatalf("complete state counters: %s", raw)
				}
				for _, field := range fields {
					if string(counts[field]) != "0" {
						t.Fatalf("zero %s counter: %s", field, raw)
					}
				}
			})
		}
	}
}

func testFederationMonitoringHTTP(t *testing.T, driver string, dsn func() string) {
	a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
	a.federate(b)
	const base = "/artifactory/api/federation/"
	// Real storage contributes an artifact and a create. Synthetic durable entries
	// cross the read-page boundary and include each remaining operation category.
	a.call(http.MethodPut, "/artifactory/fed/actual.bin", "actual content", http.StatusCreated)
	at := time.Now().Add(-2 * time.Minute)
	for i := range 105 {
		body := fmt.Sprintf(`{"path":"pending-%03d","operation":"create"}`, i)
		if _, err := a.runtime.Store.Outbox().
			Append(t.Context(), outbox.OutboxEntry{Origin: a.id, Stream: "repo:fed", EventType: federated.EventPut, Payload: []byte(body), Created: at}); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []string{"update", "properties", "delete"} {
		typ := federated.EventPut
		if operation == "delete" {
			typ = federated.EventDelete
		}
		if _, err := a.runtime.Store.Outbox().
			Append(t.Context(), outbox.OutboxEntry{Origin: a.id, Stream: "repo:fed", EventType: typ, Payload: []byte(`{"path":"changed","operation":"` + operation + `"}`), Created: at}); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{base, "/artifactory/api/v2/federation/"} {
		read := func(suffix string) map[string]json.RawMessage {
			t.Helper()
			raw := a.call(http.MethodGet, prefix+"status/repo/fed"+suffix, "", http.StatusOK)
			var body map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				t.Fatal(err)
			}
			return body
		}
		full, mirrors, binaries := read(""), read("/mirrors"), read("/binaries")
		// The two reads happen at different instants; lag and current transport
		// health can advance, while the queued operation totals must agree.
		type eventTotals struct {
			RemoteUrl, RemoteRepoKey                                                          string
			CreateEvents, UpdateEvents, DeleteEvents, PropsEvents, EventRegistrationTimeStamp int64
		}
		var complete, projected []eventTotals
		if err := json.Unmarshal(full["mirrorEventsStatusInfo"], &complete); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(mirrors["mirrorEventsStatusInfo"], &projected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(complete, projected) || len(mirrors) != 2 {
			t.Fatalf("mirror projection differs: %+v / %+v", complete, projected)
		}
		if !reflect.DeepEqual(full["federatedArtifactStatus"], binaries["federatedArtifactStatus"]) || len(binaries) != 3 {
			t.Fatalf("binary projection differs: %+v / %+v", full, binaries)
		}
		var events []struct{ CreateEvents, UpdateEvents, DeleteEvents, PropsEvents int64 }
		if err := json.Unmarshal(
			full["mirrorEventsStatusInfo"],
			&events,
		); err != nil || len(events) != 1 || events[0].CreateEvents != 106 || events[0].UpdateEvents != 1 || events[0].DeleteEvents != 1 ||
			events[0].PropsEvents != 1 {
			t.Fatalf("wrong event totals: %s %v", full["mirrorEventsStatusInfo"], err)
		}
		var artifacts map[string]int64
		if err := json.Unmarshal(full["federatedArtifactStatus"], &artifacts); err != nil || artifacts["countFullyReplicateArtifacts"] != 1 || len(artifacts) != 2 {
			t.Fatalf("wrong source DTO fields: %s %v", full["federatedArtifactStatus"], err)
		}
		a.call(http.MethodGet, prefix+"status/repo/fed/not-a-member", "", http.StatusBadRequest)
		a.call(http.MethodGet, prefix+"status/repo/missing/not-a-member", "", http.StatusNotFound)
	}
	// Projection fields never expose historical queued entries after acknowledgement.
	head, err := a.runtime.Store.Outbox().LastSeq(t.Context(), a.id, "repo:fed")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.Store.Outbox().PutCursor(t.Context(), outbox.PeerCursor{PeerInstanceID: b.id, Stream: "repo:fed", AckedSeq: head}); err != nil {
		t.Fatal(err)
	}
	var caughtUp struct {
		MirrorEventsStatusInfo []struct{ CreateEvents, LagInMS, EventRegistrationTimeStamp int64 }
	}
	raw := a.call(http.MethodGet, base+"status/repo/fed/mirrors", "", http.StatusOK)
	if err := json.Unmarshal(
		[]byte(raw),
		&caughtUp,
	); err != nil || len(caughtUp.MirrorEventsStatusInfo) != 1 || caughtUp.MirrorEventsStatusInfo[0].CreateEvents != 0 || caughtUp.MirrorEventsStatusInfo[0].LagInMS != 0 ||
		caughtUp.MirrorEventsStatusInfo[0].EventRegistrationTimeStamp != 0 {
		t.Fatalf("acknowledged entries remain visible: %s %v", raw, err)
	}
	// Non-federated local repositories and cache aliases are accepted by the
	// source resource; a virtual or a missing repository is not.
	a.call(http.MethodPut, "/artifactory/api/repositories/local-status", `{"rclass":"local","packageType":"generic"}`, http.StatusOK)
	a.call(http.MethodGet, base+"status/repo/local-status", "", http.StatusOK)
	a.call(http.MethodPut, "/artifactory/api/repositories/remote-status", `{"rclass":"remote","packageType":"generic","url":"https://up.example"}`, http.StatusOK)
	a.call(http.MethodGet, base+"status/repo/remote-status-cache", "", http.StatusOK)
	a.call(http.MethodGet, base+"status/repo/remote-status", "", http.StatusNotFound)
}

func TestAccFederationMonitoringHTTPSQLite(t *testing.T) {
	t.Parallel()
	testFederationMonitoringHTTP(t, "", func() string { return "" })
}

func TestAccFederationMonitoringHTTPPostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testFederationMonitoringHTTP(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
