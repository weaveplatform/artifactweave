package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BowerRegistrations implements store.Store.
func (s *Store) BowerRegistrations() store.BowerRegistrationStore { return bowerRegistrations{s} }

type bowerRegistrations struct{ s *Store }

func (b bowerRegistrations) Create(ctx context.Context, v types.BowerRegistration) error {
	if v.RepoKey == "" || v.Name == "" || v.URL == "" {
		return fmt.Errorf("create Bower registration: %w", store.ErrInvalid)
	}
	if _, err := b.s.Repos().Get(ctx, v.RepoKey); err != nil {
		return fmt.Errorf("create Bower registration: %w", err)
	}
	_, err := b.s.exec(
		ctx,
		"create Bower registration",
		Insert("bower_registrations", []string{"repo_key", "name", "url"}),
		v.RepoKey,
		v.Name,
		v.URL,
	)
	return err
}

func (b bowerRegistrations) Get(
	ctx context.Context,
	repoKey, name string,
) (types.BowerRegistration, error) {
	out := types.BowerRegistration{RepoKey: repoKey, Name: name}
	err := b.s.q(ctx).
		QueryRowContext(ctx, b.s.d.Rebind("SELECT url FROM bower_registrations WHERE repo_key = ? AND name = ?"), repoKey, name).
		Scan(&out.URL)
	return out, b.s.wrap("get Bower registration", err)
}

func (b bowerRegistrations) List(
	ctx context.Context,
	repoKey string,
	p store.Page,
) ([]types.BowerRegistration, string, error) {
	if repoKey == "" {
		return nil, "", fmt.Errorf("list Bower registrations: %w", store.ErrInvalid)
	}
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("list Bower registrations: %w", err)
	}
	cursor, err := p.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("list Bower registrations: %w", err)
	}
	where := " WHERE repo_key = ?"
	args := []any{repoKey}
	if cursor != nil {
		where += " AND name > ?"
		args = append(args, cursor[0])
	}
	var out []types.BowerRegistration
	err = b.s.rows(
		ctx,
		"list Bower registrations",
		"SELECT name, url FROM bower_registrations"+where+" ORDER BY name LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v := types.BowerRegistration{RepoKey: repoKey}
			if err := r.Scan(&v.Name, &v.URL); err != nil {
				return fmt.Errorf("scan Bower registration: %w", err)
			}
			out = append(out, v)
			return nil
		},
	)
	if err != nil {
		return nil, "", err
	}
	out, next := store.Trim(
		out,
		p.Limit,
		func(v types.BowerRegistration) []string { return []string{v.Name} },
	)
	return out, next, nil
}
