package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// guard serializes transactional project snapshots and mutations before they
// take any project, assignment, role, environment or member row lock. The one
// schema-owned row also covers an empty project set and concurrent creation.
// SQLite's immediate transactions already serialize these operations.
//
// Future project-aware publication must acquire this guard before artifact
// repository/source lifetime and cache/item locks. Do not first take those
// locks and then add a project check: environment rename also updates artifact
// repositories in its enclosing transaction. That P11 composition is not wired
// into the current external-only source/publication path.
func (p projectStore) guard(ctx context.Context) error {
	if !p.s.InTx(ctx) {
		return nil
	}
	var id int
	err := p.s.Q(ctx).QueryRowContext(ctx,
		"SELECT id FROM project_authority_guard WHERE id = 1"+p.s.LockClause(ctx)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("project authority guard is missing: %w", kernel.ErrStore)
	}
	return p.s.Wrap("lock project authority guard", err)
}

func (p projectStore) tx(ctx context.Context, fn func(context.Context) error) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.guard(ctx); err != nil {
			return err
		}
		return fn(ctx)
	})
}

func (p projectStore) Authority(ctx context.Context, key string) (model.ProjectAuthority, error) {
	var out model.ProjectAuthority
	if err := p.guard(ctx); err != nil {
		return out, err
	}
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT incarnation, policy_revision FROM projects WHERE key = ?"+p.s.LockClause(ctx)), key).
		Scan(&out.Incarnation, &out.Revision)
	return out, p.s.Wrap("get project authority "+key, err)
}

func (p projectStore) SharingAuthority(ctx context.Context, key string) (model.SharingAuthority, error) {
	var out model.SharingAuthority
	if err := p.guard(ctx); err != nil {
		return out, err
	}
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT sharing_revision FROM project_repositories WHERE repo_key = ?"+p.s.LockClause(ctx)), key).
		Scan(&out.Revision)
	return out, p.s.Wrap("get sharing authority "+key, err)
}

// rotatePolicy joins the mutation's transaction. A global role or environment
// can affect every project's roles, so an empty scope fences all projects.
func (p projectStore) rotatePolicy(ctx context.Context, key string) error {
	q := "UPDATE projects SET policy_revision = ?"
	args := []any{port.NewProjectAuthority().Revision}
	if key != "" {
		q += " WHERE key = ?"
		args = append(args, key)
	}
	_, err := p.s.Exec(ctx, "rotate project policy", q, args...)
	return err
}

// backfillProjectAuthority fills only absent authority values, one bounded page
// per transaction. Reopening or retrying preserves every allocated value.
func (s *Store) backfillProjectAuthority(ctx context.Context) error {
	for {
		var keys []string
		err := (projectStore{s}).tx(ctx, func(ctx context.Context) error {
			if err := s.Rows(ctx, "read project authority backfill",
				"SELECT key FROM projects WHERE incarnation = '' OR policy_revision = '' ORDER BY key LIMIT ?"+s.LockClause(ctx),
				[]any{kernel.MaxLimit}, func(r *sql.Rows) error {
					var key string
					if err := r.Scan(&key); err != nil {
						return err //nolint:wrapcheck // Rows supplies context
					}
					keys = append(keys, key)
					return nil
				}); err != nil {
				return err
			}
			for _, key := range keys {
				a := port.NewProjectAuthority()
				if _, err := s.Exec(ctx, "backfill project authority",
					"UPDATE projects SET incarnation = CASE WHEN incarnation = '' THEN ? ELSE incarnation END, "+
						"policy_revision = CASE WHEN policy_revision = '' THEN ? ELSE policy_revision END WHERE key = ?",
					a.Incarnation, a.Revision, key); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(keys) < kernel.MaxLimit {
			break
		}
	}
	for {
		var keys []string
		err := (projectStore{s}).tx(ctx, func(ctx context.Context) error {
			if err := s.Rows(ctx, "read sharing authority backfill",
				"SELECT repo_key FROM project_repositories WHERE sharing_revision = '' ORDER BY repo_key LIMIT ?"+s.LockClause(ctx),
				[]any{kernel.MaxLimit}, func(r *sql.Rows) error {
					var key string
					if err := r.Scan(&key); err != nil {
						return err //nolint:wrapcheck // Rows supplies context
					}
					keys = append(keys, key)
					return nil
				}); err != nil {
				return err
			}
			for _, key := range keys {
				if _, err := s.Exec(ctx, "backfill sharing authority",
					"UPDATE project_repositories SET sharing_revision = ? WHERE repo_key = ? AND sharing_revision = ''",
					port.NewSharingAuthority().Revision, key); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(keys) < kernel.MaxLimit {
			return nil
		}
	}
}
