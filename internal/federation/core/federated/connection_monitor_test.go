package federated_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitConnectionProjectionIsBoundedAndKeepsReceiverRole(t *testing.T) {
	for _, unneeded := range []string{"runs", "fetches", "settings", "stamps"} {
		t.Run(unneeded, func(t *testing.T) {
			f, m, _ := monitorFixture(t)
			ms := f.s.members(t, "fed")
			ms[0].Mode = "receiver"
			if err := f.s.Federation().SetMembers(t.Context(), "fed", ms); err != nil {
				t.Fatal(err)
			}
			m.Membership.Store = monitorFailureStore{Store: f.s, original: f.s, fail: unneeded}
			rows, err := m.ConnectionStates(t.Context(), "fed")
			if err != nil || len(rows) != 2 || rows[0].Mode != "Receiving_Only" || rows[0].Status != "HEALTHY" {
				t.Fatalf("bounded projection read %s: %+v %v", unneeded, rows, err)
			}
			legacy, err := m.Operations.Connections(t.Context(), "fed")
			if err != nil || len(legacy) != 2 || legacy[0].Mode != "Receiving_Only" || legacy[0].Status != "HEALTHY" {
				t.Fatalf("existing endpoint projection %+v %v", legacy, err)
			}
		})
	}
}

func TestUnitConnectionProjectionDoesNotDiscloseUnknownOrNonFederatedKeys(t *testing.T) {
	_, m, _ := monitorFixture(t)
	for _, key := range []string{"missing", "local", "local-cache"} {
		rows, err := m.ConnectionStates(t.Context(), key)
		if err != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("%s: %+v %v", key, rows, err)
		}
		legacy, err := m.Operations.Connections(t.Context(), key)
		if err != nil || legacy == nil || len(legacy) != 0 {
			t.Fatalf("%s existing projection: %+v %v", key, legacy, err)
		}
	}
	m.Authorize = func(context.Context) error { return kernel.ErrForbidden }
	if _, err := m.ConnectionStates(t.Context(), "missing"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("authority lost: %v", err)
	}
}

func TestUnitConnectionProjectionSharesCurrentCursorStatus(t *testing.T) {
	f, m, self := monitorFixture(t)
	appendMonitorEvent(t, f, self, federated.EventPut, "create", "queued", t0.Add(-2*federated.StatusLagThreshold))
	view, err := m.Read(t.Context(), "fed", false)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := m.ConnectionStates(t.Context(), "fed")
	if err != nil || len(rows) != len(view.Members) {
		t.Fatalf("connections %+v %v", rows, err)
	}
	for i, row := range rows {
		if row.Status != view.Members[i].Status || row.QueuedEvents != view.Members[i].QueuedEvents || row.LagInMS != view.Members[i].LagInMS {
			t.Fatalf("divergent member state %+v vs %+v", row, view.Members[i])
		}
	}
	if view.Status != model.SyncOutOfSync {
		t.Fatalf("aggregate state %s", view.Status)
	}
}
