package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type projectBindingValues struct {
	Producer   model.Project
	Consumer   model.Project
	Role       model.Role
	Assignment model.RepositoryAssignment
}

func testProjectBindingSnapshots(t *testing.T, newStore Factory) {
	for _, mutation := range []string{"update", "rename environment", "recreate producer", "global policy and recreate consumer"} {
		for _, projectFirst := range []bool{true, false} {
			order := "assignment first"
			if projectFirst {
				order = "project first"
			}
			t.Run("binding/"+mutation+"/"+order, func(t *testing.T) {
				s := newStore(t)
				createProjects(t, s, "alpha", "beta")
				ps := s.Projects()
				initial := projectBindingValues{
					Producer: project("alpha"), Consumer: project("beta"),
					Role: model.Role{ProjectKey: "alpha", Name: "reader", Environments: []string{"stage-0"}},
					Assignment: model.RepositoryAssignment{
						RepoKey: "alpha-libs", ProjectKey: "alpha", SharedWith: []string{"beta"}, Environments: []string{"stage-0"},
					},
				}
				storekit.Must(t, ps.CreateEnvironment(t.Context(), model.Environment{ProjectKey: "alpha", Name: "stage-0"}))
				storekit.Must(t, ps.PutRole(t.Context(), initial.Role))
				storekit.Must(t, ps.PutAssignment(t.Context(), initial.Assignment))
				ops := authoritySnapshotOps[projectBindingValues]{
					authority: func(ctx context.Context) (model.AuthorizationRevision, error) {
						var a model.SharingAuthority
						var err error
						if !projectFirst {
							a, err = ps.SharingAuthority(ctx, "alpha-libs")
							if err != nil {
								return "", err
							}
						}
						p, err := ps.Authority(ctx, "alpha")
						if err != nil {
							return "", err
						}
						c, err := ps.Authority(ctx, "beta")
						if err != nil {
							return "", err
						}
						if projectFirst {
							a, err = ps.SharingAuthority(ctx, "alpha-libs")
						}
						return model.AuthorizationRevision(fmt.Sprintf("%s/%s/%s/%s/%s", p.Incarnation, p.Revision, c.Incarnation, c.Revision, a.Revision)), err
					},
					value: func(ctx context.Context) (projectBindingValues, error) {
						var out projectBindingValues
						var err error
						if !projectFirst {
							out.Assignment, err = ps.Assignment(ctx, "alpha-libs")
							if err != nil {
								return out, err
							}
						}
						out.Producer, err = ps.Get(ctx, "alpha")
						if err != nil {
							return out, err
						}
						out.Consumer, err = ps.Get(ctx, "beta")
						if err != nil {
							return out, err
						}
						out.Role, err = ps.Role(ctx, "alpha", "reader")
						if err != nil {
							return out, err
						}
						if projectFirst {
							out.Assignment, err = ps.Assignment(ctx, "alpha-libs")
						}
						return out, err
					},
					change: func(ctx context.Context, generation int) (projectBindingValues, error) {
						want := initial
						stage := fmt.Sprintf("stage-%d", generation)
						switch mutation {
						case "rename environment":
							want.Role.Environments, want.Assignment.Environments = []string{stage}, []string{stage}
							return want, ps.RenameEnvironment(ctx, "alpha", fmt.Sprintf("stage-%d", generation-1), stage)
						case "recreate producer":
							if err := ps.Delete(ctx, "alpha"); err != nil {
								return want, err
							}
							want.Producer.Description = fmt.Sprintf("producer lifetime %d", generation)
							if err := ps.Create(ctx, want.Producer); err != nil {
								return want, err
							}
							if err := ps.CreateEnvironment(ctx, model.Environment{ProjectKey: "alpha", Name: "stage-0"}); err != nil {
								return want, err
							}
							if err := ps.PutRole(ctx, want.Role); err != nil {
								return want, err
							}
						case "global policy and recreate consumer":
							if err := ps.Delete(ctx, "beta"); err != nil {
								return want, err
							}
							want.Consumer.Description = fmt.Sprintf("consumer lifetime %d", generation)
							if err := ps.Create(ctx, want.Consumer); err != nil {
								return want, err
							}
							if err := ps.PutRole(ctx, model.Role{Name: "global", Actions: []string{fmt.Sprintf("generation-%d", generation)}}); err != nil {
								return want, err
							}
						default:
							want.Producer.Description = fmt.Sprintf("policy generation %d", generation)
							if err := ps.Update(ctx, want.Producer); err != nil {
								return want, err
							}
						}
						want.Assignment.Updated = storekit.At(generation)
						return want, ps.PutAssignment(ctx, want.Assignment)
					},
				}
				testAuthoritySnapshots(t, s, projectFirst, initial, ops)
			})
		}
	}
}
