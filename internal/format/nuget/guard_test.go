package nuget_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestWriteLockFailuresRollback(t *testing.T) {
	for _, failure := range []string{"acquire", "release", "held"} {
		t.Run(failure, func(t *testing.T) {
			w := newWorld(t, nil)
			w.items.Store = guardStore{Store: w.st, failure: failure}
			_, err := w.tryDeploy(w.c, "Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", ""), nil)
			if failure == "held" {
				if err == nil ||
					!strings.Contains(err.Error(), "held outside a write transaction") {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errFault) {
				t.Fatal(err)
			}
			if _, err := w.st.Items().
				Get(w.ctx, w.c.Key, false, "Foo.1.0.0.nupkg"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("failed write landed: %v", err)
			}
			leases, err := w.st.Leases().List(w.ctx)
			if err != nil || len(leases) != 0 {
				t.Fatalf("leaked lease: %v %v", leases, err)
			}
		})
	}
}

type guardStore struct {
	store.Store
	failure string
}

func (s guardStore) Leases() store.LeaseStore { return guardLeases{s.Store.Leases(), s.failure} }

type guardLeases struct {
	store.LeaseStore
	failure string
}

func (s guardLeases) Acquire(ctx context.Context, l types.Lease, now time.Time) (bool, error) {
	if s.failure == "acquire" {
		return false, errFault
	}
	if s.failure == "held" {
		return false, nil
	}
	return s.LeaseStore.Acquire(ctx, l, now) //nolint:wrapcheck // test pass-through
}

func (s guardLeases) Release(ctx context.Context, name, holder string) error {
	if s.failure == "release" {
		return errFault
	}
	return s.LeaseStore.Release(ctx, name, holder) //nolint:wrapcheck // test pass-through
}
