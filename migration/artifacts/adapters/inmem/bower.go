package inmem

import (
	"cmp"
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BowerRegistrations implements store.Store.
func (s *Store) BowerRegistrations() store.BowerRegistrationStore { return bowerRegistrations{s} }

type bowerRegistrations struct{ s *Store }

func (b bowerRegistrations) rows() *memtx.Table[string, types.BowerRegistration] {
	return memtx.TableOf[string, types.BowerRegistration](b.s.Store, "bower_registrations")
}

func (b bowerRegistrations) Create(ctx context.Context, v types.BowerRegistration) error {
	if v.RepoKey == "" || v.Name == "" || v.URL == "" {
		return fmt.Errorf("create Bower registration: %w", store.ErrInvalid)
	}
	return b.s.Do(ctx, func() error {
		if !b.s.Repos().(repos).t().Has(v.RepoKey) {
			return memtx.NotFound("repository", v.RepoKey)
		}
		key := v.RepoKey + "\x00" + v.Name
		if b.rows().Has(key) {
			return memtx.Conflict("Bower registration", v.Name)
		}
		b.rows().Put(key, v)
		return nil
	})
}

func (b bowerRegistrations) Get(
	ctx context.Context,
	repoKey, name string,
) (types.BowerRegistration, error) {
	var out types.BowerRegistration
	err := b.s.Do(ctx, func() error {
		var ok bool
		out, ok = b.rows().Get(repoKey + "\x00" + name)
		if !ok {
			return memtx.NotFound("Bower registration", name)
		}
		return nil
	})
	return out, err
}

func (b bowerRegistrations) List(
	ctx context.Context,
	repoKey string,
	p store.Page,
) ([]types.BowerRegistration, string, error) {
	if repoKey == "" {
		return nil, "", fmt.Errorf("list Bower registrations: %w", store.ErrInvalid)
	}
	var out []types.BowerRegistration
	var next string
	err := b.s.Do(ctx, func() error {
		rows := b.rows().
			All(func(_ string, v types.BowerRegistration) bool { return v.RepoKey == repoKey }, func(a, b types.BowerRegistration) int { return cmp.Compare(a.Name, b.Name) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(v types.BowerRegistration) []any { return []any{v.Name} },
		)
		return err
	})
	return out, next, err
}
