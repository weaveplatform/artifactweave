package project_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitProjectEnvironmentRenameAndDeleteReferences(t *testing.T) {
	s, db := lifecycleWorld()
	ctx := t.Context()
	must(t, s.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"}))
	e, err := s.CreateEnvironment(ctx, "alpha", "alpha-stage")
	must(t, err)
	r := custom("tester")
	r.Environments = []string{e.Name}
	must(t, s.CreateRole(ctx, "alpha", r))
	c := repo.Config{
		Key: "alpha-local", Class: repo.Local, PackageType: repo.Generic,
		ProjectKey: "alpha", Environments: []string{e.Name},
	}
	must(t, db.Repos().Create(ctx, c))
	must(t, db.Projects().PutAssignment(ctx, model.RepositoryAssignment{
		RepoKey: c.Key, ProjectKey: "alpha", Environments: []string{e.Name},
	}))
	if err := s.DeleteEnvironment(ctx, "alpha", e.Name); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("deleted referenced environment: %v", err)
	}
	renamed, err := s.RenameEnvironment(ctx, "alpha", "ALPHA-STAGE", "alpha-Test")
	must(t, err)
	if renamed.Name != "alpha-Test" {
		t.Fatalf("renamed spelling: %+v", renamed)
	}
	role, err := s.Role(ctx, "alpha", "tester")
	must(t, err)
	if !slices.Equal(role.Environments, []string{"alpha-Test"}) {
		t.Fatalf("role still names old environment: %+v", role)
	}
	stored, err := db.Repos().Get(ctx, c.Key)
	must(t, err)
	if !slices.Equal(stored.Environments, []string{"alpha-Test"}) {
		t.Fatalf("repository still names old environment: %+v", stored)
	}
	assigned, err := db.Projects().Assignment(ctx, c.Key)
	must(t, err)
	if !slices.Equal(assigned.Environments, []string{"alpha-Test"}) {
		t.Fatalf("assignment still names old environment: %+v", assigned)
	}
	stored.Environments = []string{"DEV"}
	must(t, db.Repos().Update(ctx, stored))
	if err := s.DeleteEnvironment(ctx, "alpha", renamed.Name); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("deleted role-referenced environment: %v", err)
	}
	must(t, s.DeleteRole(ctx, "alpha", "tester"))
	if err := s.DeleteEnvironment(ctx, "alpha", renamed.Name); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("deleted assignment-referenced environment: %v", err)
	}
	assigned.Environments = []string{"DEV"}
	must(t, db.Projects().PutAssignment(ctx, assigned))
	must(t, s.DeleteEnvironment(ctx, "alpha", "ALPHA-test"))
	all, err := s.Environments(ctx, "alpha")
	must(t, err)
	if len(all) != 0 {
		t.Fatalf("project environment survived delete: %+v", all)
	}
}

func TestUnitProjectEnvironmentRejectsCollisionPrefixAndLimit(t *testing.T) {
	s, db := lifecycleWorld()
	ctx := t.Context()
	must(t, s.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"}))
	for _, name := range []string{"stage", "beta-stage", "alpha-", "alpha-" + strings.Repeat("x", 27)} {
		if _, err := s.CreateEnvironment(ctx, "alpha", name); !errors.Is(err, project.ErrInvalid) {
			t.Errorf("invalid project environment %q: %v", name, err)
		}
	}
	must(t, db.Projects().CreateEnvironment(ctx, model.Environment{Name: "alpha-collision"}))
	if _, err := s.CreateEnvironment(ctx, "alpha", "ALPHA-COLLISION"); !errors.Is(err, project.ErrConflict) {
		t.Fatalf("global environment collision: %v", err)
	}
	for n := range project.DefaultEnvironmentLimit {
		_, err := s.CreateEnvironment(ctx, "alpha", fmt.Sprintf("alpha-e%d", n))
		must(t, err)
	}
	if _, err := s.CreateEnvironment(ctx, "alpha", "alpha-eleventh"); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("project environment limit: %v", err)
	}
	if _, err := s.RenameEnvironment(ctx, "alpha", "alpha-e0", "alpha-e1"); !errors.Is(err, project.ErrConflict) {
		t.Fatalf("rename into existing environment: %v", err)
	}
	if _, err := s.RenameEnvironment(ctx, "alpha", "alpha-e0", "alpha-E0"); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if _, err := s.Environments(ctx, "missing"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("missing project environments: %v", err)
	}
	if err := s.DeleteEnvironment(ctx, "alpha", "DEV"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("project delete reached global environment: %v", err)
	}
}

func TestUnitProjectRenameAndDeletionRollBackRepositoryUpdates(t *testing.T) {
	s, db := lifecycleWorld()
	ctx := t.Context()
	must(t, s.CreateProject(ctx, model.Project{Key: "alpha", DisplayName: "Alpha"}))
	_, err := s.CreateEnvironment(ctx, "alpha", "alpha-stage")
	must(t, err)
	c := repo.Config{
		Key: "alpha-local", Class: repo.Local, PackageType: repo.Generic,
		ProjectKey: "alpha", Environments: []string{"alpha-stage"},
	}
	must(t, db.Repos().Create(ctx, c))
	s.Repositories = lifecycleRepositories{db: db, failUpdate: c.Key}
	if _, err := s.RenameEnvironment(ctx, "alpha", "alpha-stage", "alpha-new"); !errors.Is(err, errStoreFailure) {
		t.Fatalf("rename repository failure: %v", err)
	}
	all, err := s.Environments(ctx, "alpha")
	must(t, err)
	if len(all) != 1 || all[0].Name != "alpha-stage" {
		t.Fatalf("environment rename survived rollback: %+v", all)
	}
	if err := s.DeleteProject(ctx, "alpha"); !errors.Is(err, errStoreFailure) {
		t.Fatalf("unassignment failure: %v", err)
	}
	_, err = s.Project(ctx, "alpha")
	must(t, err)
	got, err := db.Repos().Get(ctx, c.Key)
	must(t, err)
	if got.ProjectKey != "alpha" || !slices.Equal(got.Environments, c.Environments) {
		t.Fatalf("repository ownership survived partial deletion: %+v", got)
	}
	s.Repositories = lifecycleRepositories{db: db}
	must(t, s.DeleteProject(ctx, "alpha"))
	if _, err := s.Project(ctx, "alpha"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("project survived deletion: %v", err)
	}
	for _, key := range []string{c.Key, "alpha-build-info"} {
		got, err := db.Repos().Get(ctx, key)
		must(t, err)
		if got.ProjectKey != "" || len(got.Environments) != 0 {
			t.Fatalf("retained repository still assigned: %+v", got)
		}
	}
	if _, err := db.Projects().Authority(ctx, "alpha"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("project lifetime survived deletion: %v", err)
	}
}
