package remote_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// stateFaults is the in-memory store whose reads of a repository's
// availability state fail with read, and whose writes of it fail with
// write, when they are set.
type stateFaults struct {
	*inmem.Store
	read, write error
}

func (s *stateFaults) RemoteCache() port.RemoteCacheStore {
	return stateFaultCache{RemoteCacheStore: s.Store.RemoteCache(), s: s}
}

type stateFaultCache struct {
	port.RemoteCacheStore
	s *stateFaults
}

func (c stateFaultCache) State(ctx context.Context, key string) (model.RemoteSourceState, error) {
	if c.s.read != nil {
		return model.RemoteSourceState{}, c.s.read
	}
	return c.RemoteCacheStore.State(ctx, key)
}

func (c stateFaultCache) PutState(
	ctx context.Context,
	key string,
	s model.RemoteSourceState,
) error {
	if c.s.write != nil {
		return c.s.write
	}
	return c.RemoteCacheStore.PutState(ctx, key, s)
}

// A statement the store fails to run as the policy reads the repository's
// availability state before its upstream exchange, or records the
// upstream's answer after it, is the server's storage fault on a remote
// that stores artifacts and on one that does not: the upstream is not
// asked when the read fails, and the circuit is untouched. It was the raw
// store error, or a 502 protocol failure once the write failed. Any other
// failure of the store answers as it did (#1489).
func TestAccPolicyFailedAvailabilityStateIsAStorageFault(t *testing.T) {
	stored := fmt.Errorf("sqlite: read remote source: %w", errors.Join(kernel.ErrStore, errDropped))
	for _, store := range []bool{true, false} {
		for _, c := range []struct {
			name   string
			read   bool
			status int
		}{
			{"read before the exchange", true, http.StatusOK},
			{"write after a 200", false, http.StatusOK},
			{"write after a 404", false, http.StatusNotFound},
		} {
			t.Run(fmt.Sprintf("store=%v/%s", store, c.name), func(t *testing.T) {
				w := policyFixture(t)
				faults := &stateFaults{Store: w.db}
				w.p.Store = faults
				for i, err := range []error{stored, errDropped} {
					// A file of its own for each fault, as a 200 whose
					// bookkeeping failed is already kept.
					file := fmt.Sprintf("file-%d.bin", i)
					w.up.Set(
						"/"+file,
						fakeupstream.Resource{Status: c.status, Body: []byte("content")},
					)
					q := w.request(file, source.ResourceKindArtifact)
					q.Repo.StoreArtifactsLocally = ptr(store)
					if c.read {
						faults.read = err
					} else {
						faults.write = err
					}
					result, got := w.read(q)
					if got == nil && result.Stream != nil {
						_ = result.Stream.Body.Close()
					}
					switch {
					case errors.Is(err, kernel.ErrStore):
						wantStorageFault(t, got)
					case c.read:
						if !errors.Is(got, errDropped) || errors.Is(got, kernel.ErrStore) {
							t.Fatalf(
								"another failed read = %v, want the store's error as before",
								got,
							)
						}
					default:
						var failure *source.HTTPError
						if !errors.As(got, &failure) || failure.Status != http.StatusBadGateway ||
							failure.Kind != source.FailureKindProtocol {
							t.Fatalf(
								"another failed write = %v, want the 502 protocol failure",
								got,
							)
						}
					}
					faults.read, faults.write = nil, nil
					if n := w.circuit(); n != 0 {
						t.Fatalf("circuit %d after %v", n, err)
					}
					if asked := len(w.up.Requests()); c.read && asked != 0 {
						t.Fatalf(
							"the upstream was asked %d times while the state could not be read",
							asked,
						)
					}
				}
			})
		}
	}
}
