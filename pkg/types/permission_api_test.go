package types_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitActionsSpellings(t *testing.T) {
	all := types.ActionRead | types.ActionAnnotate | types.ActionWrite | types.ActionDelete |
		types.ActionManage | types.ActionScan | types.ActionExecute
	if diff := cmp.Diff(
		[]string{"READ", "ANNOTATE", "WRITE", "DELETE", "MANAGE", "SCAN", "EXECUTE"},
		all.AccessNames(),
	); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff(
		[]string{"read", "annotate", "write", "delete", "manage", "managedXrayMeta", "distribute"},
		all.LegacyNames(),
	); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff([]string{"r", "n", "w", "d", "m", "mxm", "x"}, all.Letters()); diff != "" {
		t.Error(diff)
	}
	if a, err := types.ParseAccessActions(
		[]string{"read", "Write"},
	); err != nil ||
		a != types.ActionRead|types.ActionWrite {
		t.Errorf("access names are case-insensitive: %v %v", a, err)
	}
	for _, bad := range []func() error{
		func() error { _, err := types.ParseAccessActions([]string{"FLY"}); return err },
		func() error { _, err := types.ParseLegacyActions([]string{"READ"}); return err },
		func() error { _, err := types.ParseLetters([]string{"q"}); return err },
	} {
		if err := bad(); !errors.Is(err, types.ErrUnknownAction) {
			t.Errorf("want ErrUnknownAction, got %v", err)
		}
	}
}

func TestUnitActionsJSON(t *testing.T) {
	b, err := json.Marshal(types.ActionRead | types.ActionScan)
	if err != nil || string(b) != `["READ","SCAN"]` {
		t.Fatalf("%s %v", b, err)
	}
	if b, _ := json.Marshal(types.Actions(0)); string(b) != `[]` {
		t.Fatalf("empty actions: %s", b)
	}
	var a types.Actions
	if err := json.Unmarshal(
		[]byte(`["delete","MANAGE"]`),
		&a,
	); err != nil ||
		a != types.ActionDelete|types.ActionManage {
		t.Fatalf("%v %v", a, err)
	}
	if err := json.Unmarshal([]byte(`["nope"]`), &a); !errors.Is(err, types.ErrUnknownAction) {
		t.Fatalf("want ErrUnknownAction, got %v", err)
	}
	if err := json.Unmarshal([]byte(`"READ"`), &a); err == nil {
		t.Fatal("a non-array should not decode")
	}
}
