package outbox_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func TestUnitHLC(t *testing.T) {
	a := outbox.HLC{Wall: 100}
	if got := a.Next(time.UnixMilli(200)); got != (outbox.HLC{Wall: 200}) {
		t.Errorf("physical time ahead: %+v", got)
	}
	if got := a.Next(time.UnixMilli(50)); got != (outbox.HLC{Wall: 100, Logical: 1}) {
		t.Errorf("physical time behind: %+v", got)
	}
	merges := []struct {
		local, remote outbox.HLC
		now           int64
		want          outbox.HLC
	}{
		{
			outbox.HLC{Wall: 100, Logical: 2},
			outbox.HLC{Wall: 100, Logical: 5},
			50,
			outbox.HLC{Wall: 100, Logical: 6},
		},
		{
			outbox.HLC{Wall: 200, Logical: 2},
			outbox.HLC{Wall: 100, Logical: 5},
			50,
			outbox.HLC{Wall: 200, Logical: 3},
		},
		{
			outbox.HLC{Wall: 100, Logical: 2},
			outbox.HLC{Wall: 300, Logical: 5},
			50,
			outbox.HLC{Wall: 300, Logical: 6},
		},
		{outbox.HLC{Wall: 100}, outbox.HLC{Wall: 100}, 400, outbox.HLC{Wall: 400}},
	}
	for _, m := range merges {
		if got := m.local.Merge(m.remote, time.UnixMilli(m.now)); got != m.want {
			t.Errorf("%+v merge %+v at %d = %+v, want %+v", m.local, m.remote, m.now, got, m.want)
		}
	}
	ordered := []outbox.HLC{
		{Wall: 1},
		{Wall: 1, Logical: 1},
		{Wall: 1, Logical: 0xffffffff},
		{Wall: 2},
	}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := ordered[i].Compare(ordered[j]); got != want {
				t.Errorf("%+v vs %+v = %d", ordered[i], ordered[j], got)
			}
			if got := strings.Compare(ordered[i].String(), ordered[j].String()); got != want {
				t.Errorf("text order of %+v vs %+v = %d", ordered[i], ordered[j], got)
			}
		}
		back, err := outbox.ParseHLC(ordered[i].String())
		if err != nil || back != ordered[i] {
			t.Errorf("%s: %+v %v", ordered[i], back, err)
		}
	}
	for _, bad := range []string{"", "1.2", "000000000000000g.00000000", "0000000000000001.0000000g", "0000000000000001-00000000"} {
		if _, err := outbox.ParseHLC(bad); !errors.Is(err, outbox.ErrInvalidHLC) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestUnitCursorsAndMarkers(t *testing.T) {
	c := outbox.PeerCursor{AckedSeq: 10}
	if c.Behind(15) != 5 || c.Behind(5) != 0 {
		t.Error("Behind")
	}
	if (outbox.AppliedMarker{LastSeq: 9}).Expect() != 10 {
		t.Error("Expect")
	}
}
