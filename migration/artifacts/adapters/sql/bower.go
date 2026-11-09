package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// BowerRegistrations returns the Bower registration store: the immutable
// name-to-Git-URL registrations of Bower repositories.
func (s *Store) BowerRegistrations() port.BowerRegistrationStore { return bowerRegistrations{s} }

type bowerRegistrations struct{ s *Store }

func (b bowerRegistrations) Create(ctx context.Context, v model.BowerRegistration) error {
	if v.RepoKey == "" || v.Name == "" || v.URL == "" {
		return fmt.Errorf("create Bower registration: %w", kernel.ErrInvalid)
	}
	if _, err := b.s.Repos().Get(ctx, v.RepoKey); err != nil {
		return fmt.Errorf("create Bower registration: %w", err)
	}
	_, err := b.s.Exec(
		ctx,
		"create Bower registration",
		dialect.Insert("bower_registrations", []string{"repo_key", "name", "url"}),
		v.RepoKey,
		v.Name,
		v.URL,
	)
	return err
}

func (b bowerRegistrations) Get(
	ctx context.Context,
	repoKey, name string,
) (model.BowerRegistration, error) {
	out := model.BowerRegistration{RepoKey: repoKey, Name: name}
	err := b.s.Q(ctx).
		QueryRowContext(ctx, b.s.Dialect().Rebind("SELECT url FROM bower_registrations WHERE repo_key = ? AND name = ?"), repoKey, name).
		Scan(&out.URL)
	return out, b.s.Wrap("get Bower registration", err)
}

func (b bowerRegistrations) List(
	ctx context.Context,
	repoKey string,
	p kernel.Page,
) ([]model.BowerRegistration, string, error) {
	if repoKey == "" {
		return nil, "", fmt.Errorf("list Bower registrations: %w", kernel.ErrInvalid)
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
	var out []model.BowerRegistration
	err = b.s.Rows(
		ctx,
		"list Bower registrations",
		"SELECT name, url FROM bower_registrations"+where+" ORDER BY name LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v := model.BowerRegistration{RepoKey: repoKey}
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
	out, next := kernel.Trim(
		out,
		p.Limit,
		func(v model.BowerRegistration) []string { return []string{v.Name} },
	)
	return out, next, nil
}
