package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestRepoReader(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	artifact := func(g types.Grants, targets ...string) map[types.ResourceType]types.Resource {
		ts := map[string]types.Target{}
		for _, target := range targets {
			ts[target] = types.Target{Include: []string{"**"}}
		}
		return map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {Actions: g, Targets: ts},
		}
	}
	for _, p := range []types.Permission{
		{Name: "locals", Resources: artifact(
			types.Grants{Groups: map[string]types.Actions{"devs": types.ActionRead}}, types.TargetAnyLocal)},
		{Name: "remotes", Resources: artifact(
			types.Grants{Users: map[string]types.Actions{"alice": types.ActionRead}}, types.TargetAnyRemote)},
		{Name: "one", Resources: artifact(
			types.Grants{Users: map[string]types.Actions{"bob": types.ActionRead}}, "named")},
		{Name: "write only", Resources: artifact(
			types.Grants{Users: map[string]types.Actions{"carol": types.ActionWrite}}, types.TargetAny)},
		{Name: "builds", Resources: map[types.ResourceType]types.Resource{types.ResourceBuild: {
			Actions: types.Grants{Users: map[string]types.Actions{"carol": types.ActionRead}},
			Targets: map[string]types.Target{types.TargetAny: {}},
		}}},
	} {
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	local := repo.Config{Key: "local", Class: repo.Local}
	federated := repo.Config{Key: "fed", Class: repo.Federated}
	remote := repo.Config{Key: "remote", Class: repo.Remote}
	named := repo.Config{Key: "named", Class: repo.Virtual}
	for _, tc := range []struct {
		p    Principal
		sees []repo.Config
	}{
		{Principal{Name: "root", Admin: true}, []repo.Config{local, federated, remote, named}},
		{Principal{Name: "dev", Groups: []string{"devs"}}, []repo.Config{local, federated}},
		{Principal{Name: "alice"}, []repo.Config{remote}},
		{Principal{Name: "bob"}, []repo.Config{named}},
		// WRITE without READ, and READ on builds only, show nothing.
		{Principal{Name: "carol"}, nil},
		{Anonymous, nil},
	} {
		can, err := RepoReader(ctx, db.Permissions(), tc.p)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []repo.Config{local, federated, remote, named} {
			want := false
			for _, s := range tc.sees {
				want = want || s.Key == c.Key
			}
			if can(c) != want {
				t.Errorf("%s reading %s: %v, want %v", tc.p.Name, c.Key, can(c), want)
			}
		}
	}
	if _, err := RepoReader(
		ctx,
		failingPermissions{db.Permissions()},
		Principal{Name: "x"},
	); err == nil {
		t.Fatal("a store failure was not reported")
	}
}

type failingPermissions struct{ store.PermissionStore }

func (failingPermissions) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]types.Permission, error) {
	return nil, errors.New("boom")
}
