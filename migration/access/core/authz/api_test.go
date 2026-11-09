package authz

import (
	"context"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
)

// otherDecider is an access/api Decider that is not the evaluator, as a
// fake in another module's tests is.
type otherDecider struct{ accessapi.Decider }

func TestUnitAsAPIAndFromAPI(t *testing.T) {
	if AsAPI(nil) != nil {
		t.Fatal("AsAPI(nil) is a typed nil, not a nil interface")
	}
	db := world(t)
	d, err := New(
		context.Background(),
		Sources{Permissions: db.Permissions(), Groups: db.Groups()},
		accessapi.Subject{Name: "dev", Groups: []string{"devs"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	view := AsAPI(d)
	libs := accessapi.Repo{Key: "libs-local", Class: "local"}
	if !view.Allows(accessapi.ResourceArtifact, libs, "org/acme/a.jar", accessapi.ActionRead) ||
		view.Allows(
			accessapi.ResourceArtifact,
			libs,
			"org/acme/secret/a.jar",
			accessapi.ActionRead,
		) {
		t.Error("the access/api view answers differently from the evaluator")
	}
	if back, ok := FromAPI(view); !ok || back != d {
		t.Errorf("FromAPI(AsAPI(d)) = %p, %v; want %p, true", back, ok, d)
	}
	for name, in := range map[string]accessapi.Decider{
		"nil":             nil,
		"typed nil":       (*Decider)(nil),
		"another decider": otherDecider{},
	} {
		if back, ok := FromAPI(in); ok || back != nil {
			t.Errorf("FromAPI(%s) = %p, %v; want nil, false", name, back, ok)
		}
	}
}
