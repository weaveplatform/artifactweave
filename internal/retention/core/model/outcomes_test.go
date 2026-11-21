package model_test

import (
	"errors"
	"math"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupOutcomeGroupsAccumulateWithoutOverflow(t *testing.T) {
	var totals model.CleanupOutcomeTotals
	if err := totals.AddGroup(model.CleanupSelected, 2, 20, 10); err != nil {
		t.Fatal(err)
	}
	if err := totals.AddGroup(model.CleanupSelected, 1, 7, 7); err != nil {
		t.Fatal(err)
	}
	if totals.Items != 3 || totals.Selected != 3 || totals.SelectedVirtualBytes != 27 || totals.SelectedContentBytes != 17 {
		t.Fatal("groups replaced earlier totals", totals)
	}
	for _, tc := range []struct {
		name                    string
		initial                 model.CleanupOutcomeTotals
		state                   model.CleanupEntryState
		count, virtual, content int64
	}{
		{name: "unknown state", state: "success", count: 1},
		{name: "negative count", state: model.CleanupSelected, count: -1},
		{name: "negative virtual bytes", state: model.CleanupDeleted, count: 1, virtual: -1},
		{name: "negative content bytes", state: model.CleanupDeleted, count: 1, content: -1},
		{name: "content exceeds virtual", state: model.CleanupDeleted, count: 1, virtual: 1, content: 2},
		{name: "bytes without items", state: model.CleanupDeleted, virtual: 1, content: 1},
		{name: "item overflow", initial: model.CleanupOutcomeTotals{Items: math.MaxInt64}, state: model.CleanupPending, count: 1},
		{name: "state overflow", initial: model.CleanupOutcomeTotals{Deleted: math.MaxInt64}, state: model.CleanupDeleted, count: 1},
		{name: "virtual overflow", initial: model.CleanupOutcomeTotals{SelectedVirtualBytes: math.MaxInt64}, state: model.CleanupSelected, count: 1, virtual: 1},
		{name: "content overflow", initial: model.CleanupOutcomeTotals{SelectedContentBytes: math.MaxInt64}, state: model.CleanupSelected, count: 1, virtual: 1, content: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.initial
			err := out.AddGroup(tc.state, tc.count, tc.virtual, tc.content)
			if !errors.Is(err, kernel.ErrStore) || out != tc.initial {
				t.Fatal("invalid group changed report totals", err, out)
			}
		})
	}
}
