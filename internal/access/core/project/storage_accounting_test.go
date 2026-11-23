package project_test

import (
	"context"
	"errors"
	"testing"

	accinmem "github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

type initialCounterFailure struct {
	port.Store
	enabled *bool
	reached *bool
}

func (s initialCounterFailure) Projects() port.ProjectStore {
	return initialCounterProjects{ProjectStore: s.Store.Projects(), owner: s}
}

type initialCounterProjects struct {
	port.ProjectStore
	owner initialCounterFailure
}

func (p initialCounterProjects) SetStorage(ctx context.Context, key string, value model.ProjectStorage) error {
	if err := p.ProjectStore.SetStorage(ctx, key, value); err != nil {
		return err
	}
	if *p.owner.enabled {
		*p.owner.reached = true
		return errStoreFailure
	}
	return nil
}

func TestUnitProjectContentInitializationFailurePoisonsRoot(t *testing.T) {
	for _, stage := range []string{"read repository owner", "read owned bytes", "save initial counter"} {
		t.Run(stage, func(t *testing.T) {
			var accounting project.StorageAccounting
			backend := memtx.NewWithMiddleware(accounting.Middleware)
			store := accinmem.New(backend)
			failed, counterFailed, reached := true, stage == "save initial counter", false
			accounting.Service = project.Service{
				Store: initialCounterFailure{Store: store, enabled: &counterFailed, reached: &reached},
				StorageBytes: func(_ context.Context, key string) (int64, error) {
					if key != "alpha" {
						t.Fatalf("initialized another project's storage: %q", key)
					}
					if failed && stage == "read owned bytes" {
						reached = true
						return 0, errStoreFailure
					}
					return 37, nil
				},
			}
			accounting.Repository = func(_ context.Context, key string) (artifactsapi.ProjectRepository, error) {
				if key != "owned" {
					t.Fatalf("looked up another repository's owner: %q", key)
				}
				if failed && stage == "read repository owner" {
					reached = true
					return artifactsapi.ProjectRepository{}, errStoreFailure
				}
				return artifactsapi.ProjectRepository{ProjectKey: "alpha"}, nil
			}
			ctx := t.Context()
			original := model.Project{Key: "alpha", DisplayName: "Alpha", StorageQuotaBytes: 100}
			must(t, store.Projects().Create(ctx, original))
			authority, err := store.Projects().Authority(ctx, "alpha")
			must(t, err)
			_, initialized, err := store.Projects().Storage(ctx, "alpha")
			must(t, err)
			if initialized {
				t.Fatal("fixture already has an initialized counter")
			}

			finalized, committed := false, false
			err = backend.Tx(ctx, func(ctx context.Context) error {
				must(t, tx.BeforeCommit(ctx, func(context.Context) error {
					finalized = true
					return nil
				}))
				backend.AfterCommit(ctx, func() { committed = true })
				change, err := accounting.Content(ctx, "owned")
				if change != nil || !errors.Is(err, errStoreFailure) || !reached {
					t.Fatalf("initialization did not refuse at the injected stage: change=%v reached=%v err=%v", change != nil, reached, err)
				}
				// A joined caller can ignore the returned error. The owning
				// middleware must still refuse every publication and roll back.
				change, err = accounting.Content(ctx, "owned")
				if change != nil || !errors.Is(err, errStoreFailure) {
					t.Fatalf("initialization refusal was not sticky: change=%v err=%v", change != nil, err)
				}
				return store.Users().Create(ctx, model.User{Name: "publication"})
			})
			if !errors.Is(err, errStoreFailure) || finalized || committed {
				t.Fatalf("failed initialization escaped the root: err=%v finalized=%v committed=%v", err, finalized, committed)
			}
			failed, counterFailed = false, false
			if _, err := store.Users().Get(ctx, "publication"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("joined publication survived initialization refusal: %v", err)
			}
			state, initialized, err := store.Projects().Storage(ctx, "alpha")
			must(t, err)
			if initialized || state != (model.ProjectStorage{}) {
				t.Fatalf("failed initialization retained a counter: %+v initialized=%v", state, initialized)
			}
			retained, err := store.Projects().Get(ctx, "alpha")
			must(t, err)
			current, err := store.Projects().Authority(ctx, "alpha")
			must(t, err)
			if retained != original || current != authority {
				t.Fatal("failed initialization changed project policy or authority")
			}

			must(t, backend.Tx(ctx, func(ctx context.Context) error {
				change, err := accounting.Content(ctx, "owned")
				if err != nil {
					return err
				}
				if change == nil {
					t.Fatal("healthy initialization returned no content accounting callback")
				}
				if err := change(ctx, 5); err != nil {
					return err
				}
				return store.Users().Create(ctx, model.User{Name: "publication"})
			}))
			state, initialized, err = store.Projects().Storage(ctx, "alpha")
			must(t, err)
			if !initialized || state.Bytes != 42 {
				t.Fatalf("healthy retry did not initialize and settle actual bytes: %+v initialized=%v", state, initialized)
			}
			_, err = store.Users().Get(ctx, "publication")
			must(t, err)
		})
	}
}

// The global admission sees one net delta for the whole transaction, across
// projects, unassigned repositories and orphan rows, before any project
// counter settles, and its refusal rolls the transaction back.
func TestUnitProjectStorageAdmissionSeesTheWholeTransaction(t *testing.T) {
	var accounting project.StorageAccounting
	backend := memtx.NewWithMiddleware(accounting.Middleware)
	store := accinmem.New(backend)
	owners := map[string]string{"owned": "alpha", "unassigned": ""}
	accounting.Service = project.Service{Store: store, StorageBytes: func(context.Context, string) (int64, error) { return 0, nil }}
	accounting.Repository = func(_ context.Context, key string) (artifactsapi.ProjectRepository, error) {
		owner, ok := owners[key]
		if !ok {
			return artifactsapi.ProjectRepository{}, kernel.ErrNotFound
		}
		return artifactsapi.ProjectRepository{ProjectKey: owner}, nil
	}
	var seen []int64
	var refuse error
	var committed int64
	accounting.Admission = func(ctx context.Context, delta int64) error {
		state, _, err := store.Projects().Storage(ctx, "alpha")
		must(t, err)
		if state.Bytes != committed {
			t.Fatal("the project counter settled before the global admission")
		}
		seen = append(seen, delta)
		return refuse
	}
	ctx := t.Context()
	must(t, store.Projects().Create(ctx, model.Project{Key: "alpha", DisplayName: "Alpha", StorageQuotaBytes: 1000}))
	write := func(changes map[string]int64) error {
		return backend.Tx(ctx, func(ctx context.Context) error {
			for repository, bytes := range changes {
				change, err := accounting.Content(ctx, repository)
				if err != nil {
					return err
				}
				if err := change(ctx, bytes); err != nil {
					return err
				}
			}
			return nil
		})
	}
	must(t, write(map[string]int64{"owned": 10, "unassigned": 5, "orphan": 2}))
	committed = 10
	must(t, write(map[string]int64{"owned": 10, "unassigned": -10}))
	committed = 20
	must(t, write(map[string]int64{"owned": -3}))
	committed = 17
	if len(seen) != 1 || seen[0] != 17 {
		t.Fatal("admission deltas", seen)
	}
	refuse = kernel.ErrDiskQuota
	if err := write(map[string]int64{"owned": 1}); !errors.Is(err, kernel.ErrDiskQuota) {
		t.Fatal(err)
	}
	state, _, err := store.Projects().Storage(ctx, "alpha")
	must(t, err)
	if state.Bytes != 17 {
		t.Fatal("a refused write settled its project bytes", state.Bytes)
	}
}
