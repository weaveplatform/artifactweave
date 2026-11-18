package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// NPMExternals returns durable npm external dependency bindings.
func (s *Store) NPMExternals() port.NPMExternalStore { return npmExternals{s} }

type npmExternals struct{ s *Store }

func (n npmExternals) Put(ctx context.Context, v model.NPMExternal) error {
	if v.Virtual == "" || v.Name == "" || v.Spec == "" || v.URL == "" || v.File == "" {
		return fmt.Errorf("put npm external dependency: %w", kernel.ErrInvalid)
	}
	query := dialect.Insert(
		"npm_externals",
		[]string{"virtual_key", "name", "spec", "source_url", "file", "sha256"},
	) + ` ON CONFLICT (virtual_key,name,spec) DO UPDATE SET sha256 = CASE WHEN excluded.sha256 = '' THEN npm_externals.sha256 ELSE excluded.sha256 END WHERE npm_externals.source_url = excluded.source_url AND npm_externals.file = excluded.file`
	result, err := n.s.Exec(ctx, "put npm external dependency", query, v.Virtual, v.Name, v.Spec, v.URL, v.File, v.SHA256)
	if err != nil {
		return err
	}
	if result == 0 {
		return fmt.Errorf("put npm external dependency: %w", kernel.ErrConflict)
	}
	return nil
}

func (n npmExternals) Get(ctx context.Context, v, name, spec string) (model.NPMExternal, error) {
	out := model.NPMExternal{Virtual: v, Name: name, Spec: spec}
	err := n.s.Q(ctx).
		QueryRowContext(ctx, n.s.Dialect().Rebind("SELECT source_url, file, sha256 FROM npm_externals WHERE virtual_key = ? AND name = ? AND spec = ?"), v, name, spec).
		Scan(&out.URL, &out.File, &out.SHA256)
	return out, n.s.Wrap("get npm external dependency", err)
}

func (n npmExternals) List(ctx context.Context, v, name string) ([]model.NPMExternal, error) {
	var out []model.NPMExternal
	err := n.s.Rows(
		ctx,
		"list npm external dependencies",
		"SELECT spec, source_url, file, sha256 FROM npm_externals WHERE virtual_key = ? AND name = ? ORDER BY spec",
		[]any{v, name},
		func(row *sql.Rows) error {
			r := model.NPMExternal{Virtual: v, Name: name}
			if err := row.Scan(&r.Spec, &r.URL, &r.File, &r.SHA256); err != nil {
				return fmt.Errorf("scan npm external dependency: %w", err)
			}
			out = append(out, r)
			return nil
		},
	)
	return out, err
}
