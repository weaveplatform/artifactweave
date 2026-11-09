package apilimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	platinmem "github.com/weaveplatform/artifactweave/migration/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

func TestUnitRecoveryReservedAllowance(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 15, 0, time.UTC)
	s, err := apilimit.New(
		platinmem.New(memtx.New()),
		apilimit.Default(),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, d, err := s.EnterRecovery(ctx)
	if err != nil || !d.Allowed || d.Remaining != 59 {
		t.Fatalf("first: %+v %v", d, err)
	}
	second, _, err := s.EnterRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnterRecovery(ctx); !errors.Is(err, apilimit.ErrBusy) {
		t.Fatalf("unbounded recovery: %v", err)
	}
	first()
	first()
	second()
	for range 58 {
		release, d, err := s.EnterRecovery(ctx)
		if err != nil || !d.Allowed {
			t.Fatalf("allowance: %+v %v", d, err)
		}
		release()
	}
	if release, d, err := s.EnterRecovery(
		ctx,
	); err != nil || release != nil || d.Allowed ||
		d.RetryAfter != 45*time.Second {
		t.Fatalf("window limit: %+v %v", d, err)
	}
	now = now.Add(time.Minute)
	release, d, err := s.EnterRecovery(ctx)
	if err != nil || !d.Allowed {
		t.Fatalf("reset: %+v %v", d, err)
	}
	release()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.EnterRecovery(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
