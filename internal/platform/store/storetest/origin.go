package storetest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunOrigins proves lazy origin creation inside metadata transactions and the
// conflict path's continued usability on PostgreSQL, not just outside Tx.
func RunOrigins(t *testing.T, newStore func(*testing.T) Store) {
	t.Helper()
	t.Run("ConcurrentTransactionalInitialization", func(t *testing.T) {
		s := newStore(t)
		start := make(chan struct{})
		ids := make(chan string, 2)
		var wg sync.WaitGroup
		for n := range 2 {
			wg.Go(func() {
				<-start
				err := s.Tx(t.Context(), func(ctx context.Context) error {
					id, err := instance.EnsureID(ctx, s)
					if err != nil {
						return fmt.Errorf("resolve transactional origin: %w", err)
					}
					ids <- id
					if err := s.Settings().Put(ctx, fmt.Sprintf("initializer%d", n), true); err != nil {
						return fmt.Errorf("initializer business write: %w", err)
					}
					return nil
				})
				if err != nil {
					t.Error(err)
				}
			})
		}
		close(start)
		wg.Wait()
		close(ids)
		winner := storekit.Get[string](t)(s.Instance().ID(t.Context()))
		if err := instance.ValidateInstanceID(winner); err != nil {
			t.Fatal(err)
		}
		for id := range ids {
			if id != winner {
				t.Errorf("initializer used a losing origin %q instead of%q", id, winner)
			}
		}
		for n := range 2 {
			var value bool
			storekit.Must(t, s.Settings().Get(t.Context(), fmt.Sprintf("initializer%d", n), &value))
			if !value {
				t.Fatal("initializer business write lost")
			}
		}
		// The losing initializer reports conflict without aborting a caller
		// transaction that deliberately handles that race.
		storekit.Must(t, s.Tx(t.Context(), func(ctx context.Context) error {
			storekit.WantErr(t, s.Instance().Init(ctx, instance.InstanceIdentity{InstanceID: "losing-candidate"}), kernel.ErrConflict)
			if err := s.Settings().Put(ctx, "after-handled-conflict", true); err != nil {
				return fmt.Errorf("write after handled origin race: %w", err)
			}
			return nil
		}))
		if got := storekit.Get[string](t)(instance.EnsureID(t.Context(), s)); got != winner {
			t.Fatal("origin changed across service reconstruction")
		}
	})
}
