package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func (p projectStore) storage() *memtx.Table[string, model.ProjectStorage] {
	return memtx.TableOf[string, model.ProjectStorage](p.s.Store, "project_storage")
}

func (p projectStore) Storage(ctx context.Context, key string) (model.ProjectStorage, bool, error) {
	var out model.ProjectStorage
	var found bool
	err := p.s.Do(ctx, func() error {
		if !p.projects().Has(key) {
			return memtx.NotFound("project", key)
		}
		out, found = p.storage().Get(key)
		return nil
	})
	return out, found, err
}

func (p projectStore) SetStorage(ctx context.Context, key string, value model.ProjectStorage) error {
	if value.Bytes < 0 {
		return fmt.Errorf("negative project storage: %w", kernel.ErrInvalid)
	}
	return p.s.Do(ctx, func() error {
		if !p.projects().Has(key) {
			return memtx.NotFound("project", key)
		}
		p.storage().Put(key, value)
		return nil
	})
}
