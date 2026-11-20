package inmem

import (
	"cmp"
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// VirtualCopies returns the metadata of virtual-owned transformed files.
func (s *Store) VirtualCopies() virtual.CopyStore {
	return storekit.NewVirtualCopiesWithStorageWatch(s, copyRecords{s}, s.storage)
}

type copyRecords struct{ s *Store }

func (c copyRecords) rows() *memtx.Table[string, virtual.Copy] {
	return memtx.TableOf[string, virtual.Copy](c.s.Store, "virtual_copies")
}

func (c copyRecords) Lock(ctx context.Context, key string) error {
	return c.s.Do(ctx, func() error {
		if !repos(c).t().Has(key) {
			return memtx.NotFound("virtual copy repository", key)
		}
		return nil
	})
}

func (c copyRecords) Read(ctx context.Context, key, path string) (virtual.Copy, error) {
	var v virtual.Copy
	err := c.s.Do(ctx, func() error {
		var ok bool
		v, ok = c.rows().Get(key + "\x00" + path)
		if !ok {
			return memtx.NotFound("virtual copy", path)
		}
		return nil
	})
	return v, err
}

func (c copyRecords) Write(ctx context.Context, v virtual.Copy) error {
	return c.s.Do(ctx, func() error { c.rows().Put(v.Virtual+"\x00"+v.Path, v); return nil })
}

func (c copyRecords) List(ctx context.Context, key string) ([]virtual.Copy, error) {
	var out []virtual.Copy
	err := c.s.Do(ctx, func() error {
		out = c.rows().All(func(_ string, v virtual.Copy) bool { return v.Virtual == key }, func(a, b virtual.Copy) int { return cmp.Compare(a.Path, b.Path) })
		return nil
	})
	return out, err
}

func (c copyRecords) Delete(ctx context.Context, key string) error {
	return c.s.Do(ctx, func() error {
		c.rows().DeleteWhere(func(_ string, v virtual.Copy) bool { return v.Virtual == key })
		return nil
	})
}
