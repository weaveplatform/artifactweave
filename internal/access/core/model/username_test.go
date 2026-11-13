package model_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
)

func TestUnitFoldUsername(t *testing.T) {
	if got := model.FoldUsername("Bob.Smith@Example"); got != "bob.smith@example" {
		t.Errorf("FoldUsername = %q", got)
	}
	if !model.SameUsername("BOB", "bob") || model.SameUsername("bob", "bobby") {
		t.Error("SameUsername compares without case, and only that")
	}
	got := model.FoldUsernames([]string{"Zed", "amy"})
	if !slices.Equal(got, []string{"zed", "amy"}) {
		t.Errorf("FoldUsernames keeps the order: %v", got)
	}
	if model.FoldUsernames(nil) != nil || model.FoldUserGrants(nil) != nil {
		t.Error("nil stays nil")
	}
}

// TestUnitFoldGrants: two spellings of one user hold the union of their
// actions, group grants keep their case, and the permission the grants are
// folded from is not changed.
func TestUnitFoldGrants(t *testing.T) {
	r := model.Resource{Actions: model.Grants{
		Users: map[string]api.Actions{
			"BOB": api.ActionRead, "bob": api.ActionWrite, "Amy": api.ActionRead,
		},
		Groups: map[string]api.Actions{"Ops": api.ActionRead},
	}}
	p := model.Permission{Name: "p", Resources: map[api.ResourceType]model.Resource{
		api.ResourceArtifact: r,
	}}
	got := p.FoldGrants()
	want := map[string]api.Actions{"bob": api.ActionRead | api.ActionWrite, "amy": api.ActionRead}
	if folded := got.Resources[api.ResourceArtifact].Actions; !maps.Equal(folded.Users, want) ||
		!maps.Equal(folded.Groups, map[string]api.Actions{"Ops": api.ActionRead}) {
		t.Errorf("folded %+v", folded)
	}
	if _, ok := p.Resources[api.ResourceArtifact].Actions.Users["BOB"]; !ok {
		t.Error("the permission's own grants changed")
	}
	if empty := (model.Permission{Name: "e"}).FoldGrants(); empty.Resources != nil {
		t.Errorf("a permission without sections gained some: %+v", empty)
	}
}
