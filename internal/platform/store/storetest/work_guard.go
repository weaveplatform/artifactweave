package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// RunWorkLeaseGuard proves that a replaced dispatcher cannot commit business
// effects or an event acknowledgement, using the actual shared unit of work.
func RunWorkLeaseGuard(t *testing.T, s Store) {
	t.Helper()
	ctx, queue := t.Context(), s.WorkQueue()
	storekit.Must(t, queue.Enqueue(ctx, "dispatch", "consumer", nil, storekit.At(1)))
	first := storekit.Get[[]work.WorkItem](t)(queue.Lease(ctx, lease("first", 1, 1, "dispatch")))
	if len(first) != 1 {
		t.Fatal("initial dispatcher was not leased")
	}
	_, err := queue.GuardLease(ctx, "dispatch", "consumer", first[0].Generation)
	storekit.WantErr(t, err, kernel.ErrInvalid)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		until, err := queue.GuardLease(ctx, "dispatch", "consumer", first[0].Generation)
		if err == nil && !until.Equal(first[0].LeaseUntil) {
			t.Fatalf("guard changed lease expiry: %s", until)
		}
		if err != nil {
			return fmt.Errorf("guard current test lease: %w", err)
		}
		return nil
	}))
	second := storekit.Get[[]work.WorkItem](t)(queue.Lease(ctx, lease("replacement", 12, 1, "dispatch")))
	if len(second) != 1 || second[0].Generation <= first[0].Generation {
		t.Fatal("replacement did not take a newer generation")
	}
	position := storekit.Get[outbox.Position](t)(s.Events().RegisterConsumer(ctx, "instance", "guarded"))
	event := storekit.Get[outbox.Event](t)(s.Events().AppendEvent(ctx, businessEvent(1)))
	apply := func(gen int64) error {
		return s.Tx(ctx, func(ctx context.Context) error {
			if err := s.Settings().Put(ctx, "lease-guard-effect", "committed"); err != nil {
				return fmt.Errorf("write test effect: %w", err)
			}
			if err := s.Events().Ack(ctx, position, event.Seq); err != nil {
				return fmt.Errorf("acknowledge test event: %w", err)
			}
			_, err := queue.GuardLease(ctx, "dispatch", "consumer", gen)
			if err != nil {
				return fmt.Errorf("guard effect publication: %w", err)
			}
			return nil
		})
	}
	storekit.WantErr(t, apply(first[0].Generation), kernel.ErrConflict)
	var effect string
	storekit.WantErr(t, s.Settings().Get(ctx, "lease-guard-effect", &effect), kernel.ErrNotFound)
	_, after, err := s.Events().Next(ctx, "instance", "guarded", 1)
	storekit.Must(t, err)
	if after.Seq != 0 {
		t.Fatal("stale dispatcher advanced the cursor")
	}
	storekit.Must(t, apply(second[0].Generation))
	storekit.Must(t, s.Settings().Get(ctx, "lease-guard-effect", &effect))
	if effect != "committed" {
		t.Fatal("current dispatcher lost its business effect")
	}
	storekit.Must(t, queue.Release(ctx, "dispatch", "consumer", second[0].Generation, storekit.At(13)))
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		_, err := queue.GuardLease(ctx, "dispatch", "consumer", second[0].Generation)
		if !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("pending work retained a lease: %v", err)
		}
		return nil
	}))
}
