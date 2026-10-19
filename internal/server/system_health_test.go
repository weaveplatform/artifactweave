package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The health probes (#403): ping and readiness probe the database and the
// binary store, router health reports each service, and a failure names
// the part that failed without repeating the store's own error.

const healthInstanceID = "jfrt@5d0c9a3e-1b2f-4c6d-8e7f-0a1b2c3d4e5f"

// secretCause is what a failing driver might report: it names the database
// host and a password, which no probe answer may repeat.
var secretCause = errors.New(
	"dial postgres://aw:s3cret@db.internal:5432/artifactweave: connection refused",
)

// healthStore answers reads of the instance identity until fail holds an
// error, then fails them, as a database that stopped answering does. It
// records whether each read had a deadline.
type healthStore struct {
	store.Store
	fail       atomic.Pointer[error]
	noDeadline atomic.Bool
}

func (s *healthStore) Instance() store.InstanceStore {
	return healthInstance{s.Store.Instance(), s}
}

type healthInstance struct {
	store.InstanceStore
	s *healthStore
}

func (i healthInstance) Get(ctx context.Context) (types.InstanceIdentity, error) {
	if _, ok := ctx.Deadline(); !ok {
		i.s.noDeadline.Store(true)
	}
	if err := i.s.fail.Load(); err != nil {
		return types.InstanceIdentity{}, *err
	}
	return i.InstanceStore.Get(ctx)
}

// healthBlobs fails Exists while fail holds an error.
type healthBlobs struct {
	blob.Store
	fail atomic.Pointer[error]
}

func (b *healthBlobs) Exists(ctx context.Context, sha string) (bool, error) {
	if err := b.fail.Load(); err != nil {
		return false, *err
	}
	return b.Store.Exists(ctx, sha)
}

type healthFixture struct {
	h     http.Handler
	db    *healthStore
	blobs *healthBlobs
	logs  *bytes.Buffer
}

func newHealthFixture(t *testing.T, identity bool) healthFixture {
	t.Helper()
	db := inmem.New()
	if identity {
		if err := db.Instance().Init(context.Background(), types.InstanceIdentity{
			InstanceID: healthInstanceID, ProtocolVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := healthFixture{
		db: &healthStore{Store: db}, blobs: &healthBlobs{Store: files}, logs: &bytes.Buffer{},
	}
	f.h = New(Config{
		CompatVersion: "7.155.0",
		Store:         f.db,
		Blobs:         f.blobs,
		Logger:        slog.New(slog.NewJSONHandler(f.logs, nil)),
	})
	return f
}

// routerHealthBody is the router health document whose jfrt and jfac
// entries have the given states and messages, and whose router is
// UNHEALTHY_PEER unless both are healthy.
func routerHealthBody(id, rtState, rtMessage, acState, acMessage string) string {
	uuid := strings.TrimPrefix(id, "jfrt@")
	router, node := `"state":"HEALTHY"`, nodeID()
	if rtState != stateHealthy || acState != stateHealthy {
		router = `"state":"UNHEALTHY_PEER"`
	}
	return `{"router":{"node_id":"` + node + `",` + router + `,"message":"OK"},"services":[` +
		`{"service_id":"jfrt@` + uuid + `","node_id":"` + node + `","state":"` + rtState +
		`","message":"` + rtMessage + `"},` +
		`{"service_id":"jfac@` + uuid + `","node_id":"` + node + `","state":"` + acState +
		`","message":"` + acMessage + `"}]}`
}

// probeCase is one probe request and the answer it must get; a body that
// starts with { is compared as JSON, any other as text.
type probeCase struct {
	path   string
	status int
	body   string
}

func checkProbes(t *testing.T, h http.Handler, cases []probeCase) {
	t.Helper()
	for _, c := range cases {
		rec := do(t, h, http.MethodGet, c.path, nil, "")
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.path, rec.Code, c.status, rec.Body)
			continue
		}
		got := rec.Body.String()
		if strings.HasPrefix(c.body, "{") {
			if diff := cmp.Diff(decode(t, c.body), decode(t, got)); diff != "" {
				t.Errorf("%s: body (-want +got):\n%s", c.path, diff)
			}
		} else if got != c.body {
			t.Errorf("%s: body %q, want %q", c.path, got, c.body)
		}
		for _, secret := range []string{"s3cret", "db.internal", "postgres://"} {
			if strings.Contains(got, secret) {
				t.Errorf("%s: the answer repeats %q: %s", c.path, secret, got)
			}
		}
	}
}

const probeOKBody = `{"code":"OK"}`

// The three liveness probes answer whatever the stores do.
var livenessCases = []probeCase{
	{"/artifactory/api/v1/system/liveness", 200, probeOKBody},
	{"/access/api/v1/system/liveness", 200, probeOKBody},
	{"/router/api/v1/system/liveness", 200, probeOKBody},
}

func TestProbesHealthy(t *testing.T) {
	f := newHealthFixture(t, true)
	checkProbes(t, f.h, append([]probeCase{
		{"/artifactory/api/system/ping", 200, "OK"},
		{"/artifactory/api/v1/system/readiness", 200, probeOKBody},
		{"/access/api/v1/system/ping", 200, "OK"},
		{"/access/api/v1/system/readiness", 200, probeOKBody},
		{"/router/api/v1/system/readiness", 200, probeOKBody},
		{
			"/router/api/v1/system/health", 200,
			routerHealthBody(healthInstanceID, stateHealthy, "OK", stateHealthy, "OK"),
		},
	}, livenessCases...))
	if f.db.noDeadline.Load() {
		t.Error("a probe read the database without a deadline")
	}
}

// TestProbesDatabaseDown: every probe that needs the database answers 500
// with the reason, router health 503 naming the services by the id read
// before the database went, and the cause reaches the log alone.
func TestProbesDatabaseDown(t *testing.T) {
	f := newHealthFixture(t, true)
	checkProbes(t, f.h, []probeCase{{"/artifactory/api/v1/system/readiness", 200, probeOKBody}})
	f.db.fail.Store(&secretCause)
	const reason = "The database is not available"
	checkProbes(t, f.h, append([]probeCase{
		{"/artifactory/api/system/ping", 500, reason},
		{
			"/artifactory/api/v1/system/readiness", 500,
			`{"errors":[{"status":500,"message":"` + reason + `"}]}`,
		},
		{"/access/api/v1/system/ping", 500, reason},
		{
			"/access/api/v1/system/readiness", 500,
			`{"errors":[{"code":"INTERNAL_ERROR","message":"` + reason + `"}]}`,
		},
		{
			"/router/api/v1/system/readiness", 500,
			`{"errors":[{"status":500,"message":"` + reason + `"}]}`,
		},
		{
			"/router/api/v1/system/health", 503,
			routerHealthBody(healthInstanceID, stateUnhealthy, reason, stateUnhealthy, reason),
		},
	}, livenessCases...))
	if !strings.Contains(f.logs.String(), "connection refused") {
		t.Errorf("the cause is not in the log:\n%s", f.logs)
	}
}

// TestProbesBinaryStoreDown: Artifactory's probes fail and Access's, which
// need only the database, pass.
func TestProbesBinaryStoreDown(t *testing.T) {
	f := newHealthFixture(t, true)
	f.blobs.fail.Store(&secretCause)
	const reason = "The binary store is not available"
	checkProbes(t, f.h, append([]probeCase{
		{"/artifactory/api/system/ping", 500, reason},
		{
			"/artifactory/api/v1/system/readiness", 500,
			`{"errors":[{"status":500,"message":"` + reason + `"}]}`,
		},
		{"/access/api/v1/system/ping", 200, "OK"},
		{"/access/api/v1/system/readiness", 200, probeOKBody},
		{
			"/router/api/v1/system/readiness", 500,
			`{"errors":[{"status":500,"message":"` + reason + `"}]}`,
		},
		{
			"/router/api/v1/system/health", 503,
			routerHealthBody(healthInstanceID, stateUnhealthy, reason, stateHealthy, "OK"),
		},
	}, livenessCases...))
}

// A database that has answered but holds no identity yet is healthy; the
// services then carry no id, and nothing remembered stands in for one.
func TestRouterHealthWithoutIdentity(t *testing.T) {
	f := newHealthFixture(t, false)
	checkProbes(t, f.h, []probeCase{
		{"/artifactory/api/v1/system/readiness", 200, probeOKBody},
		{
			"/router/api/v1/system/health", 200,
			routerHealthBody("", stateHealthy, "OK", stateHealthy, "OK"),
		},
	})
	f.db.fail.Store(&secretCause)
	const reason = "The database is not available"
	checkProbes(t, f.h, []probeCase{{
		"/router/api/v1/system/health", 503,
		routerHealthBody("", stateUnhealthy, reason, stateUnhealthy, reason),
	}})
}

// A service built only to reach the store probes it and remembers nothing.
func TestProbeWithoutMemory(t *testing.T) {
	db := inmem.New()
	if err := db.Instance().Init(context.Background(), types.InstanceIdentity{
		InstanceID: healthInstanceID, ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	s := artifactoryService{cfg: Config{Store: db}}
	id, err := s.probeDatabase(context.Background())
	if err != nil || id != healthInstanceID {
		t.Fatalf("probe: %q %v", id, err)
	}
	if got := s.lastInstanceID(); got != "" {
		t.Fatalf("remembered %q without a memory", got)
	}
}
