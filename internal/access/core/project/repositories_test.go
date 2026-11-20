package project_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestUnitRepositoryBindingIsSynchronousAndDetachKeepsShares(t *testing.T) {
	db := inmem.New()
	ctx := t.Context()
	for _, key := range []string{"alpha", "beta", "gamma"} {
		must(t, db.Projects().Create(ctx, model.Project{Key: key}))
	}
	c := *repo.Defaults(&repo.Config{Key: "existing-local", Class: repo.Local, PackageType: repo.Generic})
	must(t, db.Repos().Create(ctx, c))
	s := project.Service{Store: db, Repositories: support.Repositories(db)}
	initial, err := s.RepositoryStatus(ctx, c.Key)
	must(t, err)
	if initial.RepoKey != c.Key || initial.ProjectKey != "" {
		t.Fatalf("unassigned repository missing from status: %+v", initial)
	}
	must(t, s.AttachRepository(ctx, c.Key, "alpha", false))
	stored, err := db.Repos().Get(ctx, c.Key)
	must(t, err)
	if stored.Key != "existing-local" || stored.ProjectKey != "alpha" || !reflect.DeepEqual(stored.Environments, []string{"DEV"}) {
		t.Fatalf("attach must publish canonical configuration without renaming: %+v", stored)
	}
	a, err := s.RepositoryStatus(ctx, c.Key)
	must(t, err)
	if a.ProjectKey != stored.ProjectKey || !reflect.DeepEqual(a.Environments, stored.Environments) {
		t.Fatalf("configuration and assignment differ: %+v, %+v", stored, a)
	}
	a.SharedWith, a.SharedWithAll, a.SharedReadOnly = []string{"gamma"}, true, true
	must(t, db.Projects().PutAssignment(ctx, a))
	if err := s.AttachRepository(ctx, c.Key, "beta", false); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("reassignment requires force: %v", err)
	}
	after, err := db.Repos().Get(ctx, c.Key)
	must(t, err)
	if !reflect.DeepEqual(after, stored) {
		t.Fatal("refused force changed repository configuration")
	}
	must(t, s.AttachRepository(ctx, c.Key, "beta", true))
	must(t, s.DetachRepository(ctx, c.Key))
	after, err = db.Repos().Get(ctx, c.Key)
	must(t, err)
	a, err = s.RepositoryStatus(ctx, c.Key)
	must(t, err)
	if after.Key != c.Key || after.ProjectKey != "" || len(after.Environments) != 0 || a.ProjectKey != "" ||
		!a.SharedWithAll || !a.SharedReadOnly || !reflect.DeepEqual(a.SharedWith, []string{"gamma"}) {
		t.Fatalf("detach must clear owner and keep standard shares: config=%+v status=%+v", after, a)
	}
}

type assignmentFailure struct{ *inmem.Store }

func (s assignmentFailure) Projects() port.ProjectStore {
	return assignmentWriteFailure{s.Store.Projects()}
}

type assignmentWriteFailure struct{ port.ProjectStore }

func (s assignmentWriteFailure) PutAssignment(ctx context.Context, a model.RepositoryAssignment) error {
	if err := s.ProjectStore.PutAssignment(ctx, a); err != nil {
		return err
	}
	return errStoreFailure
}

func TestUnitRepositoryBindingRollsBackBothRepresentations(t *testing.T) {
	db := inmem.New()
	ctx := t.Context()
	must(t, db.Projects().Create(ctx, model.Project{Key: "alpha"}))
	c := *repo.Defaults(&repo.Config{Key: "existing-local", Class: repo.Local, PackageType: repo.Generic})
	must(t, db.Repos().Create(ctx, c))
	failing := assignmentFailure{db}
	s := project.Service{Store: failing, Repositories: support.Repositories(failing)}
	if err := s.AttachRepository(ctx, c.Key, "alpha", false); !errors.Is(err, errStoreFailure) {
		t.Fatalf("expected failure after both writes: %v", err)
	}
	stored, err := db.Repos().Get(ctx, c.Key)
	must(t, err)
	if stored.ProjectKey != "" || len(stored.Environments) != 0 {
		t.Fatalf("failed assignment left published repository binding: %+v", stored)
	}
	if _, err := db.Projects().Assignment(ctx, c.Key); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("failed assignment left access state: %v", err)
	}
}

func TestUnitRepositoryStandardSharesDoNotRequireAnOwner(t *testing.T) {
	db := inmem.New()
	ctx := t.Context()
	for _, key := range []string{"alpha", "beta"} {
		must(t, db.Projects().Create(ctx, model.Project{Key: key}))
	}
	c := *repo.Defaults(&repo.Config{Key: "shared-local", Class: repo.Local, PackageType: repo.Generic})
	must(t, db.Repos().Create(ctx, c))
	s := project.Service{Store: db, Repositories: support.Repositories(db)}
	must(t, s.ShareRepository(ctx, c.Key, "alpha", false, true))
	must(t, s.ShareRepository(ctx, c.Key, "beta", false, true))
	must(t, s.ShareRepository(ctx, c.Key, "", true, true))
	a, err := s.RepositoryStatus(ctx, c.Key)
	must(t, err)
	if a.ProjectKey != "" || !a.SharedWithAll || !a.SharedReadOnly || !reflect.DeepEqual(a.SharedWith, []string{"alpha", "beta"}) {
		t.Fatalf("unowned explicit/all-project shares: %+v", a)
	}
	must(t, s.UnshareRepository(ctx, c.Key, "", true))
	a, err = s.RepositoryStatus(ctx, c.Key)
	must(t, err)
	if a.SharedWithAll || !reflect.DeepEqual(a.SharedWith, []string{"alpha", "beta"}) {
		t.Fatalf("removing all-project sharing lost explicit targets: %+v", a)
	}
	must(t, s.UnshareRepository(ctx, c.Key, "alpha", false))
	a, err = s.RepositoryStatus(ctx, c.Key)
	must(t, err)
	if !reflect.DeepEqual(a.SharedWith, []string{"beta"}) || !a.SharedReadOnly {
		t.Fatalf("removing one target changed another target/ceiling: %+v", a)
	}
	after, err := db.Repos().Get(ctx, c.Key)
	must(t, err)
	if !reflect.DeepEqual(after, c) {
		t.Fatal("standard sharing changed repository configuration or activated a source mode")
	}
}

type repositoryFailureStore struct {
	support.SeedStore
	fail string
}

func (s repositoryFailureStore) Projects() port.ProjectStore {
	return failingProjects{ProjectStore: s.SeedStore.Projects(), fail: "binding " + s.fail}
}

type repositoryFailureAPI struct {
	artifactsapi.Repositories
	fail string
}

func (r repositoryFailureAPI) ProjectRepository(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
	if r.fail == "metadata" {
		return artifactsapi.ProjectRepository{}, errStoreFailure
	}
	return r.Repositories.ProjectRepository(ctx, key)
}

func (r repositoryFailureAPI) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.fail == "configuration" {
		return repo.Config{}, errStoreFailure
	}
	return r.Repositories.Get(ctx, key)
}

func (r repositoryFailureAPI) CheckEnvironments(ctx context.Context, c *repo.Config) error {
	if r.fail == "environment" {
		return errStoreFailure
	}
	return r.Repositories.CheckEnvironments(ctx, c)
}

func (r repositoryFailureAPI) Update(ctx context.Context, c repo.Config) error {
	if err := r.Repositories.Update(ctx, c); err != nil {
		return err
	}
	if r.fail == "publish" {
		return errStoreFailure
	}
	return nil
}

func TestUnitRepositoryBindingFailuresPreserveCanonicalAndAccessState(t *testing.T) {
	for _, stage := range []string{"guard", "metadata", "configuration", "assignment", "environment", "publish"} {
		t.Run(stage, func(t *testing.T) {
			db, ctx := inmem.New(), t.Context()
			for _, key := range []string{"alpha", "beta"} {
				must(t, db.Projects().Create(ctx, model.Project{Key: key}))
			}
			c := *repo.Defaults(&repo.Config{Key: "existing", Class: repo.Local, PackageType: repo.Generic})
			must(t, db.Repos().Create(ctx, c))
			initial := project.Service{Store: db, Repositories: support.Repositories(db)}
			must(t, initial.AttachRepository(ctx, c.Key, "alpha", false))
			must(t, initial.ShareRepository(ctx, c.Key, "beta", false, true))
			configuration, err := db.Repos().Get(ctx, c.Key)
			must(t, err)
			assignment, err := db.Projects().Assignment(ctx, c.Key)
			must(t, err)
			authority, err := db.Projects().SharingAuthority(ctx, c.Key)
			must(t, err)
			failing := repositoryFailureStore{SeedStore: db, fail: stage}
			s := project.Service{Store: failing, Repositories: repositoryFailureAPI{Repositories: support.Repositories(failing), fail: stage}}
			if err := s.AttachRepository(ctx, c.Key, "beta", true); !errors.Is(err, errStoreFailure) {
				t.Fatalf("binding %s failure was hidden: %v", stage, err)
			}
			after, err := db.Repos().Get(ctx, c.Key)
			must(t, err)
			stored, err := db.Projects().Assignment(ctx, c.Key)
			must(t, err)
			revision, err := db.Projects().SharingAuthority(ctx, c.Key)
			must(t, err)
			if !reflect.DeepEqual(configuration, after) || !reflect.DeepEqual(assignment, stored) || revision != authority {
				t.Fatalf("binding %s failure changed owner, shares or authority", stage)
			}
		})
	}
}

func TestUnitRepositoryBindingRequiresAvailableRepositoryService(t *testing.T) {
	s := project.Service{Store: inmem.New()}
	if _, err := s.RepositoryStatus(t.Context(), "existing"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("missing repository service returned %v, want store failure", err)
	}
}

func TestUnitRepositorySharingRejectsUnsupportedClassAndUnknownTarget(t *testing.T) {
	db, ctx := inmem.New(), t.Context()
	must(t, db.Projects().Create(ctx, model.Project{Key: "alpha"}))
	must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Generic})))
	must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Generic})))
	s := project.Service{Store: db, Repositories: support.Repositories(db)}
	if err := s.ShareRepository(ctx, "virtual", "alpha", false, true); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("virtual repository was shared: %v", err)
	}
	if err := s.UnshareRepository(ctx, "local", "missing", false); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("unknown target was silently accepted: %v", err)
	}
	for _, key := range []string{"local", "virtual"} {
		if _, err := db.Projects().Assignment(ctx, key); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("refused share changed binding for %s: %v", key, err)
		}
	}
}
