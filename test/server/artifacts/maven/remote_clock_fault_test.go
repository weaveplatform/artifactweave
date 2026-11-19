package maven_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	mavenfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type clockTransactionKey struct{}

// clockStatementStore fails only an existing-copy clock publication. The
// controlled SQL statement leaves the real schema and retained copy untouched.
type clockStatementStore struct {
	remote.Store
	sql      *appsql.Store
	stage    string
	deadline bool
	mu       sync.Mutex
	armed    bool
	hits     int
	cause    error
	ended    bool
}

func (s *clockStatementStore) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.armed
}

func (s *clockStatementStore) arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *clockStatementStore) failure(ctx context.Context) error {
	_, err := s.sql.Exec(ctx, "publish existing remote clock", "UPDATE cache_clock_missing_table SET doc = doc")
	if s.deadline {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	s.mu.Lock()
	s.hits++
	s.cause = err
	s.ended = ctx.Err() != nil
	s.mu.Unlock()
	return err
}

func (s *clockStatementStore) RemoteCache() port.RemoteCacheStore {
	return clockStatementCache{RemoteCacheStore: s.Store.RemoteCache(), owner: s}
}

func (s *clockStatementStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	return s.Store.Tx(ctx, func(tx context.Context) error {
		clock := false
		tx = context.WithValue(tx, clockTransactionKey{}, &clock)
		if err := fn(tx); err != nil {
			return err
		}
		if clock && s.ready() && s.stage == "transaction" {
			// Fail after the actual clock Put, while its transaction can still
			// roll back; a successful partial write must not become visible.
			return s.failure(tx)
		}
		return nil
	})
}

type clockStatementCache struct {
	port.RemoteCacheStore
	owner *clockStatementStore
}

func (c clockStatementCache) Put(
	ctx context.Context,
	token model.RemoteCacheToken,
	entry model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	if clock, ok := ctx.Value(clockTransactionKey{}).(*bool); ok {
		*clock = true
	}
	if c.owner.ready() && c.owner.stage == "put" {
		return model.RemoteCacheEntry{}, c.owner.failure(ctx)
	}
	return c.RemoteCacheStore.Put(ctx, token, entry)
}

func TestAccRemoteClockStatementFaultsAreInternal(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, kind := range []repo.PackageType{repo.Generic, repo.Maven} {
			for _, mode := range []string{"conditional304", "read-only-unchanged200"} {
				for _, stage := range []string{"check", "put", "transaction"} {
					for _, deadline := range []bool{false, true} {
						for _, virtual := range []bool{false, true} {
							name := fmt.Sprintf("%s/%s/%s/%s/deadline=%t/virtual=%t", backend, kind, mode, stage, deadline, virtual)
							t.Run(name, func(t *testing.T) {
								remoteClockStatementFault(t, backend, kind, mode, stage, deadline, virtual)
							})
						}
					}
				}
			}
		}
	}
}

func remoteClockStatementFault(
	t *testing.T,
	backend string,
	kind repo.PackageType,
	mode, stage string,
	deadline, virtual bool,
) {
	t.Helper()
	w := sourceLookupWorld(t, kind, backend)
	w.Repo.RetrievalCachePeriodSecs = new(0)
	w.Update()
	logs := sourceLookupLogs(w)
	path := "plain.jar"
	if kind == repo.Maven {
		path = "org/acme/retained/1.0-SNAPSHOT/retained-1.0-SNAPSHOT.jar"
	}
	data := sourceJAR(t, "retained clock control")
	resource := fakeupstream.Resource{
		Body: data, ETag: `"same"`,
		Header: http.Header{"Cache-Control": {"max-age=0"}, "Content-Type": {"application/java-archive"}},
	}
	w.Upstream.Set("/"+path, resource)
	target := "/artifactory/remote/" + path
	if virtual {
		target = "/artifactory/virtual/" + path
		if err := w.Runtime.Store.Permissions().Update(t.Context(), accesstest.Permission{
			Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{
				api.ResourceArtifact: {
					Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead | api.ActionWrite}},
					Targets: map[string]accesstest.Target{"remote": {}, "virtual": {}, "local": {}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
		// The priority remote must not hide its own storage failure by
		// falling through to a perfectly usable later local holder.
		remoteStatus(t, w.Request(http.MethodPut, "/artifactory/local/"+path, "admin", string(data)), 201)
	}
	remoteStatus(t, w.Request(http.MethodGet, target, "reader", ""), 200)
	if mode == "read-only-unchanged200" {
		if err := w.Runtime.Store.Permissions().Update(t.Context(), accesstest.Permission{
			Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{
				api.ResourceArtifact: {
					Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
					Targets: map[string]accesstest.Target{"remote": {}, "virtual": {}, "local": {}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
		resource.Status = http.StatusOK
		w.Upstream.Set("/"+path, resource)
	}
	remoteStatus(t, w.Request(http.MethodGet, target, "reader", ""), 200)
	before, _, err := w.Runtime.Store.RemoteCache().Exact(t.Context(), "remote", path, kernel.Page{Limit: 10})
	if err != nil || len(before) != 1 || before[0].Blob.Checksums.SHA256 == "" {
		t.Fatal("healthy complete cache entry", before, err)
	}
	state, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	policy := w.Runtime.Artifacts.RemoteSources().HTTP.(*mavenfmt.MavenBackend).Policy
	originalStore, originalCheck, originalProject := policy.Store, policy.CheckCurrent, policy.ProjectResponse
	fault := &clockStatementStore{Store: originalStore, sql: w.Runtime.Store, stage: stage, deadline: deadline}
	policy.Store = fault
	policy.ProjectResponse = func(q source.Request, res *http.Response) error {
		if originalProject != nil {
			if err := originalProject(q, res); err != nil {
				return err
			}
		}
		if q.Resource.Path == path {
			fault.arm()
		}
		return nil
	}
	policy.CheckCurrent = func(ctx context.Context, st remote.Store, q source.Request, p source.Permit, revision string) error {
		if fault.ready() && stage == "check" {
			return fault.failure(ctx)
		}
		return originalCheck(ctx, st, q, p, revision)
	}
	t.Cleanup(func() {
		policy.Store, policy.CheckCurrent, policy.ProjectResponse = originalStore, originalCheck, originalProject
	})
	logs.Reset()
	res := w.Request(http.MethodGet, target, "reader", "")
	want := `{"errors":[{"message":"internal error","status":500}]}` + "\n"
	if res.Code != http.StatusInternalServerError || res.Body.String() != want {
		t.Errorf("clock statement fault: %d %s", res.Code, res.Body.String())
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 ||
		!strings.Contains(logs.String(), "cache_clock_missing_table") ||
		(!strings.Contains(logs.String(), "no such table") && !strings.Contains(logs.String(), "SQLSTATE 42P01")) {
		t.Errorf("original statement cause must be logged once: %s", logs.String())
	}
	fault.mu.Lock()
	if fault.hits != 1 || fault.ended || !errors.Is(fault.cause, kernel.ErrStore) ||
		(deadline && !errors.Is(fault.cause, context.DeadlineExceeded)) {
		t.Errorf("expected one classified real SQL fault: hits=%d cause=%v", fault.hits, fault.cause)
	}
	fault.mu.Unlock()
	after, _, err := w.Runtime.Store.RemoteCache().Exact(t.Context(), "remote", path, kernel.Page{Limit: 10})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("failed clock publication changed the cached copy: before=%+v after=%+v err=%v", before, after, err)
	}
	afterState, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
	// The successful upstream response advances the state's CAS revision
	// before clock publication. Its availability/circuit fields must not change.
	afterState.Revision = state.Revision
	if err != nil || !reflect.DeepEqual(state, afterState) {
		t.Errorf("clock fault changed circuit state: before=%+v after=%+v err=%v", state, afterState, err)
	}
	policy.Store, policy.CheckCurrent, policy.ProjectResponse = originalStore, originalCheck, originalProject
	got := w.Request(http.MethodGet, target, "reader", "")
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatalf("healthy recovery lost retained bytes: %d %s", got.Code, got.Body.String())
	}
}
