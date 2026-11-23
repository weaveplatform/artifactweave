package project_test

import (
	"context"
	"errors"
	"testing"
	"time"

	accinmem "github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// storeFault fails exactly one call: the (skip+1)th call of method. Counting
// the calls proves that the operation reached the injected boundary rather
// than refusing earlier for an unrelated reason that wraps the same sentinel.
type storeFault struct {
	method string
	skip   int
	calls  int
}

func (f *storeFault) fail(method string) error {
	if f == nil || f.method != method {
		return nil
	}
	f.calls++
	if f.calls == f.skip+1 {
		return errStoreFailure
	}
	return nil
}

func (f *storeFault) reached() bool { return f.calls > f.skip }

type faultStore struct {
	port.Store
	f *storeFault
}

func (s faultStore) Projects() port.ProjectStore {
	return faultProjects{ProjectStore: s.Store.Projects(), f: s.f}
}

func (s faultStore) Users() port.UserStore { return faultUsers{UserStore: s.Store.Users(), f: s.f} }

func (s faultStore) Groups() port.GroupStore {
	return faultGroups{GroupStore: s.Store.Groups(), f: s.f}
}

type faultUsers struct {
	port.UserStore
	f *storeFault
}

func (s faultUsers) Get(ctx context.Context, name string) (model.User, error) {
	if err := s.f.fail("Users.Get"); err != nil {
		return model.User{}, err
	}
	return s.UserStore.Get(ctx, name) //nolint:wrapcheck // transparent test double
}

type faultGroups struct {
	port.GroupStore
	f *storeFault
}

func (s faultGroups) Get(ctx context.Context, name string) (model.Group, error) {
	if err := s.f.fail("Groups.Get"); err != nil {
		return model.Group{}, err
	}
	return s.GroupStore.Get(ctx, name) //nolint:wrapcheck // transparent test double
}

type faultProjects struct {
	port.ProjectStore
	f *storeFault
}

func (s faultProjects) Guard(ctx context.Context) error {
	if err := s.f.fail("Guard"); err != nil {
		return err
	}
	return s.ProjectStore.Guard(ctx) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Authority(ctx context.Context, key string) (model.ProjectAuthority, error) {
	if err := s.f.fail("Authority"); err != nil {
		return model.ProjectAuthority{}, err
	}
	return s.ProjectStore.Authority(ctx, key) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Get(ctx context.Context, key string) (model.Project, error) {
	if err := s.f.fail("Get"); err != nil {
		return model.Project{}, err
	}
	return s.ProjectStore.Get(ctx, key) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Update(ctx context.Context, p model.Project) error {
	if err := s.f.fail("Update"); err != nil {
		return err
	}
	return s.ProjectStore.Update(ctx, p) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Delete(ctx context.Context, key string) error {
	if err := s.f.fail("Delete"); err != nil {
		return err
	}
	return s.ProjectStore.Delete(ctx, key) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) List(ctx context.Context) ([]model.Project, error) {
	if err := s.f.fail("List"); err != nil {
		return nil, err
	}
	return s.ProjectStore.List(ctx) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Storage(ctx context.Context, key string) (model.ProjectStorage, bool, error) {
	if err := s.f.fail("Storage"); err != nil {
		return model.ProjectStorage{}, false, err
	}
	return s.ProjectStore.Storage(ctx, key) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) SetStorage(ctx context.Context, key string, value model.ProjectStorage) error {
	if err := s.f.fail("SetStorage"); err != nil {
		return err
	}
	return s.ProjectStore.SetStorage(ctx, key, value) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Roles(ctx context.Context, key string) ([]model.Role, error) {
	if err := s.f.fail("Roles"); err != nil {
		return nil, err
	}
	return s.ProjectStore.Roles(ctx, key) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Environments(ctx context.Context, key string) ([]model.Environment, error) {
	if err := s.f.fail("Environments"); err != nil {
		return nil, err
	}
	return s.ProjectStore.Environments(ctx, key) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) CreateEnvironment(ctx context.Context, e model.Environment) error {
	if err := s.f.fail("CreateEnvironment"); err != nil {
		return err
	}
	return s.ProjectStore.CreateEnvironment(ctx, e) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) RenameEnvironment(ctx context.Context, key, from, to string) error {
	if err := s.f.fail("RenameEnvironment"); err != nil {
		return err
	}
	return s.ProjectStore.RenameEnvironment(ctx, key, from, to) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) DeleteEnvironment(ctx context.Context, key, name string) error {
	if err := s.f.fail("DeleteEnvironment"); err != nil {
		return err
	}
	return s.ProjectStore.DeleteEnvironment(ctx, key, name) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Member(ctx context.Context, key string, typ model.PrincipalType, name string) (model.ProjectMember, error) {
	if err := s.f.fail("Member"); err != nil {
		return model.ProjectMember{}, err
	}
	return s.ProjectStore.Member(ctx, key, typ, name) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Members(ctx context.Context, key string, typ model.PrincipalType) ([]model.ProjectMember, error) {
	if err := s.f.fail("Members"); err != nil {
		return nil, err
	}
	return s.ProjectStore.Members(ctx, key, typ) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) DeleteMember(ctx context.Context, key string, typ model.PrincipalType, name string) error {
	if err := s.f.fail("DeleteMember"); err != nil {
		return err
	}
	return s.ProjectStore.DeleteMember(ctx, key, typ, name) //nolint:wrapcheck // transparent test double
}

func (s faultProjects) Assignments(ctx context.Context, key string) ([]model.RepositoryAssignment, error) {
	if err := s.f.fail("Assignments"); err != nil {
		return nil, err
	}
	return s.ProjectStore.Assignments(ctx, key) //nolint:wrapcheck // transparent test double
}

type faultRepositories struct {
	artifactsapi.Repositories
	f *storeFault
}

func (r faultRepositories) List(ctx context.Context, filter artifactsapi.RepoFilter) ([]repo.Config, error) {
	if err := r.f.fail("Repositories.List"); err != nil {
		return nil, err
	}
	return r.Repositories.List(ctx, filter) //nolint:wrapcheck // transparent test double
}

func (r faultRepositories) Update(ctx context.Context, c repo.Config) error {
	if err := r.f.fail("Repositories.Update"); err != nil {
		return err
	}
	return r.Repositories.Update(ctx, c) //nolint:wrapcheck // transparent test double
}

// faultWorld has a project-owned repository using alpha-stage, an unused
// alpha-spare environment, a user and group with direct roles, and a quota
// that the first 80 bytes take past the 75% warning threshold.
func faultWorld(t *testing.T, f *storeFault) (project.Service, *inmem.Store) {
	t.Helper()
	_, db := world(t)
	ctx := t.Context()
	must(t, db.Projects().Update(ctx, model.Project{Key: "alpha", DisplayName: "alpha", StorageQuotaBytes: 100, QuotaEmail: true}))
	must(t, db.Projects().CreateEnvironment(ctx, model.Environment{ProjectKey: "alpha", Name: "alpha-spare"}))
	must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "alpha-owned", Class: repo.Local, PackageType: repo.Generic, ProjectKey: "alpha", Environments: []string{"alpha-stage"},
	})))
	must(t, db.Users().Create(ctx, model.User{Name: "alice"}))
	must(t, db.Groups().Create(ctx, model.Group{Name: "team"}))
	healthy := project.Service{Store: db}
	must(t, healthy.CreateRole(ctx, "alpha", custom("reader")))
	_, err := healthy.PutMember(ctx, "alpha", model.PrincipalUser, "alice", []string{"reader"})
	must(t, err)
	_, err = healthy.PutMember(ctx, "alpha", model.PrincipalGroup, "team", []string{"reader"})
	must(t, err)
	return project.Service{
		Store:          faultStore{Store: db, f: f},
		Repositories:   faultRepositories{Repositories: support.Repositories(db), f: f},
		StorageBytes:   func(context.Context, string) (int64, error) { return 0, nil },
		StorageWarning: func(context.Context, model.ProjectStorageWarning) error { return nil },
	}, db
}

func TestUnitProjectServiceStorageFailuresKeepTheirCause(t *testing.T) {
	ctx := t.Context()
	renameStage := func(s project.Service) error {
		_, err := s.RenameEnvironment(ctx, "alpha", "alpha-stage", "alpha-renamed")
		return err
	}
	deleteSpare := func(s project.Service) error { return s.DeleteEnvironment(ctx, "alpha", "alpha-spare") }
	createEnv := func(s project.Service) error {
		_, err := s.CreateEnvironment(ctx, "alpha", "alpha-new")
		return err
	}
	grow := func(s project.Service) error { return s.ChangeStorage(ctx, "alpha", 80, nil) }
	updateAlpha := func(s project.Service) error {
		return s.UpdateProject(ctx, "alpha", model.Project{DisplayName: "Alpha renamed"})
	}
	for _, tc := range []struct {
		name   string
		method string
		skip   int
		run    func(project.Service) error
	}{
		{"list environments project", "Get", 0, func(s project.Service) error { _, err := s.Environments(ctx, "alpha"); return err }},
		{"list environments", "Environments", 0, func(s project.Service) error { _, err := s.Environments(ctx, "alpha"); return err }},
		{"create environment count", "Environments", 0, createEnv},
		{"create environment project names", "List", 0, createEnv},
		{"create environment global names", "Environments", 1, createEnv},
		{"create environment write", "CreateEnvironment", 0, createEnv},
		{"rename environment lookup", "Environments", 0, renameStage},
		{"rename environment names", "List", 0, renameStage},
		{"rename environment write", "RenameEnvironment", 0, renameStage},
		{"rename environment repositories", "Repositories.List", 0, renameStage},
		{"rename environment repository update", "Repositories.Update", 0, renameStage},
		{"delete environment lookup", "Environments", 0, deleteSpare},
		{"delete environment repositories", "Repositories.List", 0, deleteSpare},
		{"delete environment roles", "Roles", 0, deleteSpare},
		{"delete environment assignments", "Assignments", 0, deleteSpare},
		{"delete environment write", "DeleteEnvironment", 0, deleteSpare},
		{"members project", "Get", 0, func(s project.Service) error {
			_, err := s.Members(ctx, "alpha", model.PrincipalUser)
			return err
		}},
		{"members", "Members", 0, func(s project.Service) error { _, err := s.Members(ctx, "alpha", model.PrincipalUser); return err }},
		{"member project", "Get", 0, func(s project.Service) error {
			_, err := s.Member(ctx, "alpha", model.PrincipalUser, "alice")
			return err
		}},
		{"member", "Member", 0, func(s project.Service) error {
			_, err := s.Member(ctx, "alpha", model.PrincipalUser, "alice")
			return err
		}},
		{"put member project", "Get", 0, func(s project.Service) error {
			_, err := s.PutMember(ctx, "alpha", model.PrincipalUser, "alice", []string{"reader"})
			return err
		}},
		{"put member user identity", "Users.Get", 0, func(s project.Service) error {
			_, err := s.PutMember(ctx, "alpha", model.PrincipalUser, "alice", []string{"reader"})
			return err
		}},
		{"put member group identity", "Groups.Get", 0, func(s project.Service) error {
			_, err := s.PutMember(ctx, "alpha", model.PrincipalGroup, "team", []string{"reader"})
			return err
		}},
		{"delete member project", "Get", 0, func(s project.Service) error {
			return s.DeleteMember(ctx, "alpha", model.PrincipalUser, "alice")
		}},
		{"delete member", "DeleteMember", 0, func(s project.Service) error {
			return s.DeleteMember(ctx, "alpha", model.PrincipalUser, "alice")
		}},
		{"assigned roles project", "Get", 0, func(s project.Service) error {
			_, err := s.AssignedRoles(ctx, "alpha", "alice", nil)
			return err
		}},
		{"assigned roles identity", "Users.Get", 0, func(s project.Service) error {
			_, err := s.AssignedRoles(ctx, "alpha", "alice", nil)
			return err
		}},
		{"list projects", "List", 0, func(s project.Service) error { _, err := s.Projects(ctx); return err }},
		{"read project", "Get", 0, func(s project.Service) error { _, err := s.Project(ctx, "alpha"); return err }},
		{"create project names", "List", 0, func(s project.Service) error {
			return s.CreateProject(ctx, model.Project{Key: "gamma", DisplayName: "gamma"})
		}},
		{"update project names", "List", 0, updateAlpha},
		{"update project write", "Update", 0, updateAlpha},
		{"delete project read", "Get", 0, func(s project.Service) error { return s.DeleteProject(ctx, "alpha") }},
		{"delete project repositories", "Repositories.List", 0, func(s project.Service) error { return s.DeleteProject(ctx, "alpha") }},
		{"delete project unassign", "Repositories.Update", 0, func(s project.Service) error { return s.DeleteProject(ctx, "alpha") }},
		{"delete project write", "Delete", 0, func(s project.Service) error { return s.DeleteProject(ctx, "alpha") }},
		{"roles project", "Get", 0, func(s project.Service) error { _, err := s.Roles(ctx, "alpha"); return err }},
		{"role project", "Get", 0, func(s project.Service) error { _, err := s.Role(ctx, "alpha", "Viewer"); return err }},
		{"create role project", "Get", 0, func(s project.Service) error { return s.CreateRole(ctx, "alpha", custom("writer")) }},
		{"update role project", "Get", 0, func(s project.Service) error {
			return s.UpdateRole(ctx, "alpha", "reader", custom("reader"))
		}},
		{"delete role project", "Get", 0, func(s project.Service) error { return s.DeleteRole(ctx, "alpha", "reader") }},
		{"delete role memberships", "Members", 0, func(s project.Service) error { return s.DeleteRole(ctx, "alpha", "reader") }},
		{"storage guard", "Guard", 0, grow},
		{"storage project", "Get", 0, grow},
		{"storage counter", "Storage", 0, grow},
		{"storage warning lifetime", "Authority", 0, grow},
		{"storage save", "SetStorage", 0, grow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &storeFault{method: tc.method, skip: tc.skip}
			s, db := faultWorld(t, f)
			before, err := db.Projects().Authority(ctx, "alpha")
			must(t, err)
			if err := tc.run(s); !errors.Is(err, errStoreFailure) || !f.reached() {
				t.Fatalf("storage failure at %s#%d was hidden or not reached: reached=%v err=%v", tc.method, tc.skip+1, f.reached(), err)
			}
			after, err := db.Projects().Authority(ctx, "alpha")
			must(t, err)
			if before != after {
				t.Fatal("a failed operation rotated project authority")
			}
		})
	}
}

func TestUnitProjectServiceRefusesMissingCompositionAndBadInput(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		want error
		run  func(s, unbound project.Service) error
	}{
		{"members of unknown type", project.ErrInvalid, func(s, unbound project.Service) error {
			_, err := s.Members(ctx, "alpha", model.PrincipalType("robot"))
			return err
		}},
		{"member of unknown type", project.ErrInvalid, func(s, unbound project.Service) error {
			_, err := s.Member(ctx, "alpha", model.PrincipalType("robot"), "r2")
			return err
		}},
		{"delete member of unknown type", project.ErrInvalid, func(s, unbound project.Service) error {
			return s.DeleteMember(ctx, "alpha", model.PrincipalType("robot"), "r2")
		}},
		{"rename to an unprefixed name", project.ErrInvalid, func(s, unbound project.Service) error {
			_, err := s.RenameEnvironment(ctx, "alpha", "alpha-stage", "beta-stage")
			return err
		}},
		{"rename without repository binding", kernel.ErrStore, func(s, unbound project.Service) error {
			_, err := unbound.RenameEnvironment(ctx, "alpha", "alpha-stage", "alpha-renamed")
			return err
		}},
		{"delete environment without repository binding", kernel.ErrStore, func(s, unbound project.Service) error {
			return unbound.DeleteEnvironment(ctx, "alpha", "alpha-spare")
		}},
		{"create project without repository binding", kernel.ErrStore, func(s, unbound project.Service) error {
			return unbound.CreateProject(ctx, model.Project{Key: "gamma", DisplayName: "gamma"})
		}},
		{"delete project without repository binding", kernel.ErrStore, func(s, unbound project.Service) error { return unbound.DeleteProject(ctx, "alpha") }},
		{"update role with an unknown action", project.ErrInvalid, func(s, unbound project.Service) error {
			r := custom("reader")
			r.Actions = []string{"NOT_AN_ACTION"}
			return s.UpdateRole(ctx, "alpha", "reader", r)
		}},
		{"storage warning without a handoff", kernel.ErrStore, func(s, unbound project.Service) error {
			silent := s
			silent.StorageWarning = nil
			return silent.ChangeStorage(ctx, "alpha", 80, nil)
		}},
		{"storage warning handoff failure", errStoreFailure, func(s, unbound project.Service) error {
			failing := s
			failing.StorageWarning = func(context.Context, model.ProjectStorageWarning) error { return errStoreFailure }
			return failing.ChangeStorage(ctx, "alpha", 80, nil)
		}},
		{"storage publication failure", errStoreFailure, func(s, unbound project.Service) error {
			return s.ChangeStorage(ctx, "alpha", 1, func(context.Context) error { return errStoreFailure })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := faultWorld(t, nil)
			before, _, err := db.Projects().Storage(ctx, "alpha")
			must(t, err)
			if err := tc.run(s, project.Service{Store: s.Store}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			after, _, err := db.Projects().Storage(ctx, "alpha")
			must(t, err)
			if before != after {
				t.Fatalf("a refused operation changed the storage counter: %+v -> %+v", before, after)
			}
		})
	}
}

// The composed in-memory store initializes counters when a quota policy is
// written, so the lazy-initialization refusals use a bare access store.
func TestUnitProjectStorageInitializationRefusalSavesNoCounter(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name  string
		bytes func(context.Context, string) (int64, error)
		want  error
	}{
		{"no initializer", nil, kernel.ErrStore},
		{"initializer failure", func(context.Context, string) (int64, error) { return 0, errStoreFailure }, errStoreFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := accinmem.New(memtx.New())
			must(t, store.Projects().Create(ctx, model.Project{Key: "alpha", DisplayName: "alpha"}))
			s := project.Service{Store: store, StorageBytes: tc.bytes}
			if err := s.ChangeStorage(ctx, "alpha", 1, nil); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if _, initialized, err := store.Projects().Storage(ctx, "alpha"); err != nil || initialized {
				t.Fatalf("refused initialization saved a counter: initialized=%v err=%v", initialized, err)
			}
		})
	}
}

type refusingQueue struct{ work.WorkQueue }

func (refusingQueue) Enqueue(context.Context, string, string, []byte, time.Time) error {
	return errStoreFailure
}

func TestUnitProjectStorageWarningHandoffRefusesIncompleteOrFailedQueueing(t *testing.T) {
	complete := model.ProjectStorageWarning{ProjectKey: "alpha", Incarnation: "life", WarningKey: "w", Bytes: 80, Limit: 100}
	for _, tc := range []struct {
		name    string
		queue   work.WorkQueue
		warning model.ProjectStorageWarning
		want    error
	}{
		{"no queue", nil, complete, kernel.ErrStore},
		{"no limit", refusingQueue{}, model.ProjectStorageWarning{ProjectKey: "alpha", Incarnation: "life", WarningKey: "w"}, kernel.ErrStore},
		{"queue failure", refusingQueue{}, complete, errStoreFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := project.EnqueueStorageWarning(t.Context(), tc.queue, tc.warning, time.Now()); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnitProjectStorageAccountingRefusesOutsideItsRootAndOnLifetimeFailures(t *testing.T) {
	ctx := t.Context()
	var empty project.StorageAccounting
	if err := empty.Middleware(ctx, func(context.Context) error { return nil }); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("missing composition was accepted: %v", err)
	}
	if err := empty.Policy(ctx, "alpha"); !errors.Is(err, kernel.ErrStore) || !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("policy outside a root transaction did not refuse and report its unbound refusal: %v", err)
	}
	if change, err := empty.Content(ctx, "owned"); change != nil || !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("content outside a root transaction: %v", err)
	}
	if change, err := empty.Ownership(ctx, artifactsapi.StorageOwnership{Project: "alpha"}); change != nil || !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("ownership outside a root transaction: %v", err)
	}

	transfer := artifactsapi.StorageOwnership{Repository: "owned", PreviousProject: "alpha", Project: "beta"}
	for _, tc := range []struct {
		name   string
		method string
		skip   int
		bytes  int64
		want   error
	}{
		{"previous lifetime", "Authority", 0, 1, errStoreFailure},
		{"next lifetime", "Authority", 1, 1, errStoreFailure},
		{"initial counter", "Storage", 0, 1, errStoreFailure},
		{"settlement lifetime", "Authority", 2, 1, errStoreFailure},
		{"negative transfer", "", 0, -1, kernel.ErrStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var accounting project.StorageAccounting
			backend := memtx.NewWithMiddleware(accounting.Middleware)
			store := accinmem.New(backend)
			for _, key := range []string{"alpha", "beta"} {
				must(t, store.Projects().Create(ctx, model.Project{Key: key, DisplayName: key}))
			}
			f := &storeFault{method: tc.method, skip: tc.skip}
			accounting.Service = project.Service{
				Store:        faultStore{Store: store, f: f},
				StorageBytes: func(context.Context, string) (int64, error) { return 10, nil },
			}
			accounting.Repository = func(context.Context, string) (artifactsapi.ProjectRepository, error) {
				return artifactsapi.ProjectRepository{ProjectKey: "alpha"}, nil
			}
			var late artifactsapi.StorageChange
			err := backend.Tx(ctx, func(ctx context.Context) error {
				change, err := accounting.Ownership(ctx, transfer)
				if err != nil {
					return err
				}
				late = change
				return change(ctx, tc.bytes)
			})
			if !errors.Is(err, tc.want) || tc.method != "" && !f.reached() {
				t.Fatalf("got %v (reached=%v), want %v", err, f.reached(), tc.want)
			}
			if late != nil {
				if err := late(ctx, 1); !errors.Is(err, kernel.ErrConflict) {
					t.Fatalf("a change recorded after its root ended was accepted: %v", err)
				}
			}
			for _, key := range []string{"alpha", "beta"} {
				_, initialized, err := store.Projects().Storage(ctx, key)
				must(t, err)
				if initialized {
					t.Fatalf("failed transfer retained %s's counter", key)
				}
			}
		})
	}
}
