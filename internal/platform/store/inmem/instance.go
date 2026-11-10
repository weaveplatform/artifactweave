package inmem

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// The table has the SQL table's name and holds the identity as its one row.
const (
	tInstance      = "instance"
	instanceRowKey = 1
)

// Instance returns the store of this instance's identity (decision 0021).
func (s *Store) Instance() instance.InstanceStore { return instanceStore{s} }

type instanceStore struct{ s *Store }

func (i instanceStore) t() *memtx.Table[int, instance.InstanceIdentity] {
	return memtx.TableOf[int, instance.InstanceIdentity](i.s.Store, tInstance)
}

func (i instanceStore) Init(ctx context.Context, id instance.InstanceIdentity) error {
	return i.s.Do(ctx, func() error {
		if i.t().Has(instanceRowKey) {
			return memtx.Conflict("instance identity", id.InstanceID)
		}
		i.t().Put(instanceRowKey, id)
		return nil
	})
}

func (i instanceStore) Get(ctx context.Context) (instance.InstanceIdentity, error) {
	var out instance.InstanceIdentity
	err := i.s.Do(ctx, func() error {
		var ok bool
		if out, ok = i.t().Get(instanceRowKey); !ok {
			return memtx.NotFound("instance identity", "")
		}
		return nil
	})
	return out, err
}

func (i instanceStore) Update(ctx context.Context, id instance.InstanceIdentity) error {
	return i.s.Do(ctx, func() error {
		if !i.t().Has(instanceRowKey) {
			return memtx.NotFound("instance identity", "")
		}
		i.t().Put(instanceRowKey, id)
		return nil
	})
}
