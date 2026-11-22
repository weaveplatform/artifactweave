package federationhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type executeBoundaryStore struct {
	*fedinmem.Store
	queue work.WorkQueue
}

func (s executeBoundaryStore) WorkQueue() work.WorkQueue { return s.queue }

type executeBoundaryPolicy struct{}

func (executeBoundaryPolicy) Any() bool                      { return true }
func (executeBoundaryPolicy) Platform() bool                 { return true }
func (executeBoundaryPolicy) Allows(string, repo.Class) bool { return true }

type executeBoundaryRepository struct{}

func (executeBoundaryRepository) ProjectRepository(_ context.Context, key string) (artifactsapi.ProjectRepository, error) {
	if key != "local" {
		return artifactsapi.ProjectRepository{}, kernel.ErrNotFound
	}
	return artifactsapi.ProjectRepository{Key: key, Class: repo.Local}, nil
}

type executeBoundaryWorld struct {
	api                ReplicationAPI
	store              executeBoundaryStore
	ctx                context.Context
	admissions, starts int
}

func newExecuteBoundaryWorld(t *testing.T) *executeBoundaryWorld {
	t.Helper()
	shared := memtx.New()
	platform := platforminmem.New(shared)
	w := &executeBoundaryWorld{
		store: executeBoundaryStore{Store: fedinmem.New(shared), queue: platform.WorkQueue()},
		ctx:   accessapi.WithCaller(t.Context(), kernel.Principal{Name: "execute-author", TokenID: "verified-execute-token"}, nil),
	}
	service := replication.Service{
		Store: w.store, Ownership: executeBoundaryRepository{}, Settings: platform.Settings(),
		Authorize: func(context.Context) (replication.Policy, error) {
			w.admissions++
			return executeBoundaryPolicy{}, nil
		},
		Guard: func(context.Context) error { return nil },
	}
	targetURL := "https://peer.example/artifactory/destination"
	if err := service.Replace(w.ctx, "local", replication.Input{URL: &targetURL}); err != nil {
		t.Fatal(err)
	}
	w.admissions = 0
	w.api.Executions = &replication.Coordinator{
		Store: w.store, Configurations: service, Now: func() time.Time { return time.Unix(1, 0) },
		Drivers: map[model.ReplicationDirection]replication.TransferStart{
			model.ReplicationPush: func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				w.starts++
				if !w.store.InTx(ctx) {
					t.Fatal("accepted HTTP execution did not share the admission transaction")
				}
				return replication.TransferAdmission{
					ID: request.ID, Incarnation: "transfer-" + request.ID,
					Repository: artifactsapi.TransferRepository{Key: "local", Incarnation: "repository-life", Generation: "generation", PackageType: repo.Generic},
				}, nil
			},
		},
	}
	return w
}

func TestUnitReplicationExecuteBodyLimitReachesBothStrictDecoderReads(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{name: "first decode", body: `[{"username":"` + strings.Repeat("x", 96) + `"}]`, want: http.StatusRequestEntityTooLarge},
		{name: "trailing decode", body: `[]` + strings.Repeat(" ", 96), want: http.StatusRequestEntityTooLarge},
		{name: "within limit", body: `[{}]`, want: http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newExecuteBoundaryWorld(t)
			request := httptest.NewRequest(http.MethodPost, "/artifactory/api/replication/execute/local/selected", strings.NewReader(tc.body)).WithContext(w.ctx)
			request.ContentLength = -1 // Exercise MaxBytesReader rather than declared-length rejection.
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			middleware.BodyLimit(48)(http.HandlerFunc(w.api.execute)).ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("execution response = %d, want %d: %s", response.Code, tc.want, response.Body.String())
			}
			stats, err := w.store.WorkQueue().Stats(w.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == http.StatusAccepted {
				if w.admissions != 1 || w.starts != 1 || len(stats) != 1 || stats[0].Pending != 1 {
					t.Fatal("within-limit control did not admit exactly one execution")
				}
			} else if w.admissions != 0 || w.starts != 0 || len(stats) != 0 {
				t.Fatal("oversized execution body reached command admission or retained work")
			}
		})
	}
}

func TestUnitReplicationExecuteRejectsEncodedRepositorySeparatorsBeforeAdmission(t *testing.T) {
	for _, path := range []string{"loc%2Fal", "loc%5Cal"} {
		t.Run(path, func(t *testing.T) {
			w := newExecuteBoundaryWorld(t)
			request := httptest.NewRequest(http.MethodPost, "/artifactory/api/replication/execute/"+path+"/selected", strings.NewReader("[]")).WithContext(w.ctx)
			response := httptest.NewRecorder()
			w.api.execute(response, request)
			if response.Code != http.StatusBadRequest || w.admissions != 0 || w.starts != 0 {
				t.Fatal("encoded repository separator reached execution admission", response.Code)
			}
			stats, err := w.store.WorkQueue().Stats(w.ctx)
			if err != nil || len(stats) != 0 {
				t.Fatal("invalid repository path retained queued work", err)
			}
			response = httptest.NewRecorder()
			w.api.execute(response, httptest.NewRequest(http.MethodPost, "/artifactory/api/replication/execute/local/selected", strings.NewReader("[]")).WithContext(w.ctx))
			if response.Code != http.StatusAccepted || w.admissions != 1 || w.starts != 1 {
				t.Fatal("valid repository path did not recover execution admission", response.Code)
			}
		})
	}
}
