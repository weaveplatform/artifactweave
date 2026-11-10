package itemkit

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestUnitAccessForEvaluatesWithoutDecider: accessFor keeps the decider it is
// given and asks Authorizer.For only when it has none, returning For's error
// unwrapped. Over the access module that evaluation reads the anonymous
// access setting, which counts as off while it was never written, so the
// anonymous user's grants apply only once it is on.
func TestUnitAccessForEvaluatesWithoutDecider(t *testing.T) {
	ctx := t.Context()
	asked := false
	scripted := &accessapitest.Authorizer{DecideFunc: func(
		context.Context, accessapi.Subject, bool,
	) (accessapi.Decider, error) {
		asked = true
		return nil, errBoom
	}}
	given := &accessapitest.Decider{Admin: true}
	acc, err := AccessFor(ctx, scripted, kernel.Principal{Name: "ann"}, given)
	if err != nil || acc.Decider != given || asked {
		t.Errorf(
			"with a decider: %v, %v, evaluated %t; want the decider kept",
			acc.Decider,
			err,
			asked,
		)
	}
	acc, err = AccessFor(ctx, scripted, kernel.Principal{Name: "ann"}, nil)
	if acc.Decider != nil || !errors.Is(err, errBoom) ||
		err.Error() != "evaluate permissions: boom" {
		t.Errorf("without a decider: %v, %v; want For's error as it is", acc.Decider, err)
	}

	st := inmem.New()
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := st.Permissions().Create(ctx, types.Permission{
		Name: "anonymous-reads",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				kernel.AnonymousUser: types.ActionRead,
			}},
			Targets: map[string]types.Target{c.Key: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	reads := func() bool {
		t.Helper()
		acc, err := AccessFor(ctx, authorizerOver(st), kernel.Anonymous, nil)
		if err != nil {
			t.Fatal(err)
		}
		return acc.File(c, "a.txt", types.ActionRead)
	}
	if reads() {
		t.Error("anonymous reads while the anonymous access setting was never written")
	}
	if err := st.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if !reads() {
		t.Error("anonymous cannot read once anonymous access is on")
	}
}
