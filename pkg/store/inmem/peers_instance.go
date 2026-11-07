package inmem

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Table names of the peers group; the same names as the SQL tables.
const (
	tInstance      = "instance"
	instanceRowKey = 1
)

// Instance implements store.Store.
func (s *Store) Instance() store.InstanceStore { return instanceStore{s} }

type instanceStore struct{ s *Store }

func (i instanceStore) t() *memtx.Table[int, types.InstanceIdentity] {
	return memtx.TableOf[int, types.InstanceIdentity](i.s.Store, tInstance)
}

func (i instanceStore) Init(ctx context.Context, id types.InstanceIdentity) error {
	return i.s.Do(ctx, func() error {
		if i.t().Has(instanceRowKey) {
			return memtx.Conflict("instance identity", id.InstanceID)
		}
		i.t().Put(instanceRowKey, id)
		return nil
	})
}

func (i instanceStore) Get(ctx context.Context) (types.InstanceIdentity, error) {
	var out types.InstanceIdentity
	err := i.s.Do(ctx, func() error {
		var ok bool
		if out, ok = i.t().Get(instanceRowKey); !ok {
			return memtx.NotFound("instance identity", "")
		}
		return nil
	})
	return out, err
}

func (i instanceStore) Update(ctx context.Context, id types.InstanceIdentity) error {
	return i.s.Do(ctx, func() error {
		if !i.t().Has(instanceRowKey) {
			return memtx.NotFound("instance identity", "")
		}
		i.t().Put(instanceRowKey, id)
		return nil
	})
}
