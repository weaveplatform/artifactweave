package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func authorityProject(t *testing.T, ps port.ProjectStore, key string) model.ProjectAuthority {
	t.Helper()
	a := storekit.Get[model.ProjectAuthority](t)(ps.Authority(t.Context(), key))
	if a.Incarnation == "" || a.Revision == "" {
		t.Fatal("project authority is incomplete")
	}
	return a
}

func authoritySharing(t *testing.T, ps port.ProjectStore) model.SharingAuthority {
	t.Helper()
	a := storekit.Get[model.SharingAuthority](t)(ps.SharingAuthority(t.Context(), "alpha-libs"))
	if a.Revision == "" {
		t.Fatal("sharing authority is incomplete")
	}
	return a
}

func authorityProjects(t *testing.T, s port.Store) port.ProjectStore {
	t.Helper()
	createProjects(t, s, "alpha", "beta")
	ps, ctx := s.Projects(), t.Context()
	for _, err := range []error{
		ps.PutRole(ctx, model.Role{ProjectKey: "alpha", Name: "alpha-role", Actions: []string{"READ_REPOSITORY"}, Environments: []string{"alpha-QA"}}),
		ps.PutRole(ctx, model.Role{Name: "global-role", Actions: []string{"READ_REPOSITORY"}, Environments: []string{"GLOBAL-QA"}}),
		ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "alpha", Name: "alpha-QA"}),
		ps.CreateEnvironment(ctx, model.Environment{Name: "GLOBAL-QA"}),
		ps.PutMember(ctx, model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "ann", Roles: []string{"alpha-role"}}),
		ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha", SharedWith: []string{"beta"}, Environments: []string{"alpha-QA"}}),
	} {
		storekit.Must(t, err)
	}
	return ps
}

func testProjectAuthorities(t *testing.T, newStore Factory) {
	t.Run("policy mutations", func(t *testing.T) { testProjectPolicyRevisions(t, newStore) })
	t.Run("lifetime and assignment recreation", func(t *testing.T) {
		ps := authorityProjects(t, newStore(t))
		first := authorityProject(t, ps, "alpha")
		storekit.Same(t, "stable read", first, authorityProject(t, ps, "alpha"))
		share := authoritySharing(t, ps)
		storekit.Must(t, ps.DeleteAssignment(t.Context(), "alpha-libs"))
		_, err := ps.SharingAuthority(t.Context(), "alpha-libs")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.Must(t, ps.PutAssignment(t.Context(), model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"}))
		if authoritySharing(t, ps) == share {
			t.Fatal("reattachment revived the old sharing revision")
		}
		storekit.Must(t, ps.Delete(t.Context(), "alpha"))
		_, err = ps.Authority(t.Context(), "alpha")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		_, err = ps.SharingAuthority(t.Context(), "alpha-libs")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.Must(t, ps.Create(t.Context(), project("alpha")))
		second := authorityProject(t, ps, "alpha")
		if first.Incarnation == second.Incarnation || first.Revision == second.Revision {
			t.Fatal("project recreation revived its previous authority")
		}
		storekit.Same(t, "project public value", project("alpha"), storekit.Get[model.Project](t)(ps.Get(t.Context(), "alpha")))
	})
	t.Run("incoming share removal", func(t *testing.T) {
		ps := authorityProjects(t, newStore(t))
		before := authoritySharing(t, ps)
		storekit.Must(t, ps.Delete(t.Context(), "beta"))
		if authoritySharing(t, ps) == before {
			t.Fatal("deleting the receiving project retained the old sharing revision")
		}
		got := storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(t.Context(), "alpha-libs"))
		if len(got.SharedWith) != 0 {
			t.Fatal("deleted receiving project remains shared")
		}
	})
	t.Run("rollback", func(t *testing.T) {
		s := newStore(t)
		ps := authorityProjects(t, s)
		before, share := authorityProject(t, ps, "alpha"), authoritySharing(t, ps)
		assignment := storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(t.Context(), "alpha-libs"))
		rollback := errors.New("rollback project changes")
		err := s.Tx(t.Context(), func(ctx context.Context) error {
			if err := ps.PutMember(ctx, model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "ann"}); err != nil {
				return err
			}
			if err := ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"}); err != nil {
				return err
			}
			return rollback
		})
		storekit.WantErr(t, err, rollback)
		storekit.Same(t, "rolled back project authority", before, authorityProject(t, ps, "alpha"))
		storekit.Same(t, "rolled back sharing authority", share, authoritySharing(t, ps))
		storekit.Same(t, "rolled back assignment", assignment, storekit.Get[model.RepositoryAssignment](t)(ps.Assignment(t.Context(), "alpha-libs")))
	})
	t.Run("failed writes and cancellation", func(t *testing.T) {
		ps := authorityProjects(t, newStore(t))
		before, share := authorityProject(t, ps, "alpha"), authoritySharing(t, ps)
		storekit.WantErr(t, ps.Create(t.Context(), project("alpha")), kernel.ErrConflict)
		storekit.WantErr(t, ps.PutAssignment(t.Context(), model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "absent"}), kernel.ErrNotFound)
		storekit.WantErr(t, ps.CreateEnvironment(t.Context(), model.Environment{Name: "GLOBAL-QA"}), kernel.ErrConflict)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		storekit.WantErr(t, ps.Update(ctx, project("alpha")), context.Canceled)
		storekit.Same(t, "failed writes preserve project authority", before, authorityProject(t, ps, "alpha"))
		storekit.Same(t, "failed writes preserve sharing authority", share, authoritySharing(t, ps))
		_, err := ps.Authority(t.Context(), "absent")
		storekit.WantErr(t, err, kernel.ErrNotFound)
		_, err = ps.SharingAuthority(t.Context(), "absent")
		storekit.WantErr(t, err, kernel.ErrNotFound)
	})
}

func testProjectPolicyRevisions(t *testing.T, newStore Factory) {
	cases := []struct {
		name     string
		projects []string
		sharing  bool
		change   func(context.Context, port.ProjectStore) error
	}{
		{"project update", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			v := project("alpha")
			v.AdminPrivileges = model.AdminPrivileges{}
			return ps.Update(ctx, v)
		}},
		{"project role update", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutRole(ctx, model.Role{ProjectKey: "alpha", Name: "alpha-role", Actions: []string{"WRITE_REPOSITORY"}})
		}},
		{"project role delete", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteRole(ctx, "alpha", "alpha-role")
		}},
		{"global role update", []string{"alpha", "beta"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutRole(ctx, model.Role{Name: "global-role", Actions: []string{"WRITE_REPOSITORY"}})
		}},
		{"global role delete", []string{"alpha", "beta"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteRole(ctx, "", "global-role")
		}},
		{"project environment create", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "alpha", Name: "alpha-UAT"})
		}},
		{"project environment rename", []string{"alpha"}, true, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.RenameEnvironment(ctx, "alpha", "alpha-QA", "alpha-UAT")
		}},
		{"project environment delete", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteEnvironment(ctx, "alpha", "alpha-QA")
		}},
		{"global environment create", []string{"alpha", "beta"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.CreateEnvironment(ctx, model.Environment{Name: "GLOBAL-UAT"})
		}},
		{"global environment rename", []string{"alpha", "beta"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.RenameEnvironment(ctx, "", "GLOBAL-QA", "GLOBAL-UAT")
		}},
		{"global environment delete", []string{"alpha", "beta"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteEnvironment(ctx, "", "GLOBAL-QA")
		}},
		{"member replacement", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutMember(ctx, model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "ann"})
		}},
		{"member deletion", []string{"alpha"}, false, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.DeleteMember(ctx, "alpha", model.PrincipalUser, "ann")
		}},
		{"share revocation", nil, true, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"})
		}},
		{"share permission change", nil, true, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha", SharedWith: []string{"beta"}, SharedReadOnly: true})
		}},
		{"share with all projects", nil, true, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha", SharedWithAll: true})
		}},
		{"assignment ownership change", nil, true, func(ctx context.Context, ps port.ProjectStore) error {
			return ps.PutAssignment(ctx, model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "beta"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := authorityProjects(t, newStore(t))
			before := map[string]model.ProjectAuthority{
				"alpha": authorityProject(t, ps, "alpha"),
				"beta":  authorityProject(t, ps, "beta"),
			}
			share := authoritySharing(t, ps)
			storekit.Must(t, tc.change(t.Context(), ps))
			for _, key := range []string{"alpha", "beta"} {
				if after := authorityProject(t, ps, key); after.Incarnation != before[key].Incarnation {
					t.Fatal("policy mutation replaced the project's lifetime")
				}
			}
			for _, key := range tc.projects {
				if authorityProject(t, ps, key).Revision == before[key].Revision {
					t.Fatalf("%s policy revision did not change", key)
				}
			}
			if tc.sharing && authoritySharing(t, ps) == share {
				t.Fatal("assignment/share revision did not change")
			}
		})
	}
}
