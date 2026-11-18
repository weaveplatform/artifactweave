package inmem

import (
	"cmp"
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// NPMExternals returns durable npm external dependency bindings.
func (s *Store) NPMExternals() port.NPMExternalStore { return npmExternals{s} }

type npmExternals struct{ s *Store }

func (n npmExternals) rows() *memtx.Table[string, model.NPMExternal] {
	return memtx.TableOf[string, model.NPMExternal](n.s.Store, "npm_externals")
}

func (n npmExternals) Put(ctx context.Context, v model.NPMExternal) error {
	if v.Virtual == "" || v.Name == "" || v.Spec == "" || v.URL == "" || v.File == "" {
		return fmt.Errorf("put npm external dependency: %w", kernel.ErrInvalid)
	}
	return n.s.Do(ctx, func() error {
		if !n.s.Repos().(repos).t().Has(v.Virtual) {
			return memtx.NotFound("repository", v.Virtual)
		}
		key := v.Virtual + "\x00" + v.Name + "\x00" + v.Spec
		if old, ok := n.rows().Get(key); ok {
			if old.URL != v.URL || old.File != v.File {
				return memtx.Conflict("npm external dependency", v.Spec)
			}
			if v.SHA256 == "" {
				v.SHA256 = old.SHA256
			}
		}
		n.rows().Put(key, v)
		return nil
	})
}

func (n npmExternals) Get(ctx context.Context, v, name, spec string) (model.NPMExternal, error) {
	var out model.NPMExternal
	err := n.s.Do(ctx, func() error {
		var ok bool
		out, ok = n.rows().Get(v + "\x00" + name + "\x00" + spec)
		if !ok {
			return memtx.NotFound("npm external dependency", spec)
		}
		return nil
	})
	return out, err
}

func (n npmExternals) List(ctx context.Context, v, name string) ([]model.NPMExternal, error) {
	var out []model.NPMExternal
	err := n.s.Do(ctx, func() error {
		out = n.rows().
			All(func(_ string, r model.NPMExternal) bool { return r.Virtual == v && r.Name == name }, func(a, b model.NPMExternal) int { return cmp.Compare(a.Spec, b.Spec) })
		return nil
	})
	return out, err
}
