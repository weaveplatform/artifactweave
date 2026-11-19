package inmem

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func (p projectStore) authorities() *memtx.Table[string, model.ProjectAuthority] {
	return memtx.TableOf[string, model.ProjectAuthority](p.s.Store, "project_authority")
}

func (p projectStore) sharingAuthorities() *memtx.Table[string, model.SharingAuthority] {
	return memtx.TableOf[string, model.SharingAuthority](p.s.Store, "project_sharing_authority")
}

func (p projectStore) Authority(ctx context.Context, key string) (model.ProjectAuthority, error) {
	var out model.ProjectAuthority
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.authorities().Get(key); !ok || !p.projects().Has(key) {
			return memtx.NotFound("project authority", key)
		}
		return nil
	})
	return out, err
}

func (p projectStore) SharingAuthority(ctx context.Context, key string) (model.SharingAuthority, error) {
	var out model.SharingAuthority
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.sharingAuthorities().Get(key); !ok || !p.assignments().Has(key) {
			return memtx.NotFound("sharing authority", key)
		}
		return nil
	})
	return out, err
}

// rotatePolicy runs under the same gate as the mutation. Global role and
// environment changes fence every project that could consume them.
func (p projectStore) rotatePolicy(key string) {
	for k, a := range p.authorities().Rows {
		if key == "" || key == k {
			a.Revision = port.NewProjectAuthority().Revision
			p.authorities().Put(k, a)
		}
	}
}
