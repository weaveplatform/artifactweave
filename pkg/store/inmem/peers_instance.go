package inmem

import (
	"context"

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

func (i instanceStore) t() *table[int, types.InstanceIdentity] {
	return tableOf[int, types.InstanceIdentity](i.s, tInstance)
}

func (i instanceStore) Init(ctx context.Context, id types.InstanceIdentity) error {
	return i.s.do(ctx, func() error {
		if i.t().has(instanceRowKey) {
			return conflict("instance identity", id.InstanceID)
		}
		i.t().put(instanceRowKey, id)
		return nil
	})
}

func (i instanceStore) Get(ctx context.Context) (types.InstanceIdentity, error) {
	var out types.InstanceIdentity
	err := i.s.do(ctx, func() error {
		var ok bool
		if out, ok = i.t().get(instanceRowKey); !ok {
			return notFound("instance identity", "")
		}
		return nil
	})
	return out, err
}

func (i instanceStore) Update(ctx context.Context, id types.InstanceIdentity) error {
	return i.s.do(ctx, func() error {
		if !i.t().has(instanceRowKey) {
			return notFound("instance identity", "")
		}
		i.t().put(instanceRowKey, id)
		return nil
	})
}
