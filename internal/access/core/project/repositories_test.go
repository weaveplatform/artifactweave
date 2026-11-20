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

type assignmentFailure struct{ port.Store }

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
	s := project.Service{Store: assignmentFailure{db}, Repositories: support.Repositories(db)}
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
