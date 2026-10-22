package item

import (
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestRemotePropertyOwnership(t *testing.T) {
	now := time.Now().UTC()
	initial, state := MergeRemoteProperties(
		nil,
		nil,
		types.Properties{"up": {"one"}, "remove": {"gone"}, "edit": {"before"}},
		now,
	)
	initial["edit"] = []string{"local"}
	initial["existing"] = []string{"local"}
	delete(initial, "up")
	merged, next := MergeRemoteProperties(
		initial,
		state,
		types.Properties{
			"up":       {"two"},
			"edit":     {"remote"},
			"existing": {"remote"},
			"new":      {"added"},
		},
		now.Add(time.Second),
	)
	want := types.Properties{"edit": {"local"}, "existing": {"local"}, "new": {"added"}}
	if !reflect.DeepEqual(merged, want) || !next.LocalProperties["up"] ||
		!next.LocalProperties["edit"] ||
		!next.LocalProperties["existing"] {
		t.Fatal(merged, next)
	}
	if !reflect.DeepEqual(next.OwnedProperties, types.Properties{"new": {"added"}}) ||
		state.LocalProperties["up"] {
		t.Fatal("snapshot mutated or ownership lost", state, next)
	}
	merged, next = MergeRemoteProperties(merged, next, types.Properties{}, now.Add(2*time.Second))
	if _, ok := merged["new"]; ok || len(next.OwnedProperties) != 0 {
		t.Fatal("upstream deletion retained owned value", merged, next)
	}
}
