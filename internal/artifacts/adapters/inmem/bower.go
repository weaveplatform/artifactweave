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

// BowerRegistrations returns the Bower registration store: the immutable
// name-to-Git-URL registrations of Bower repositories.
func (s *Store) BowerRegistrations() port.BowerRegistrationStore { return bowerRegistrations{s} }

type bowerRegistrations struct{ s *Store }

func (b bowerRegistrations) rows() *memtx.Table[string, model.BowerRegistration] {
	return memtx.TableOf[string, model.BowerRegistration](b.s.Store, "bower_registrations")
}

func (b bowerRegistrations) Create(ctx context.Context, v model.BowerRegistration) error {
	if v.RepoKey == "" || v.Name == "" || v.URL == "" {
		return fmt.Errorf("create Bower registration: %w", kernel.ErrInvalid)
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
) (model.BowerRegistration, error) {
	var out model.BowerRegistration
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
	p kernel.Page,
) ([]model.BowerRegistration, string, error) {
	if repoKey == "" {
		return nil, "", fmt.Errorf("list Bower registrations: %w", kernel.ErrInvalid)
	}
	var out []model.BowerRegistration
	var next string
	err := b.s.Do(ctx, func() error {
		rows := b.rows().
			All(func(_ string, v model.BowerRegistration) bool { return v.RepoKey == repoKey }, func(a, b model.BowerRegistration) int { return cmp.Compare(a.Name, b.Name) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(v model.BowerRegistration) []any { return []any{v.Name} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}
