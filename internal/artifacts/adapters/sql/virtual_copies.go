package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// VirtualCopies returns the durable metadata of virtual-owned transformed files.
func (s *Store) VirtualCopies() virtual.CopyStore {
	return storekit.NewVirtualCopies(s, copyRecords{s})
}

type copyRecords struct{ s *Store }

func (c copyRecords) Lock(ctx context.Context, key string) error {
	var found string
	err := c.s.Q(ctx).QueryRowContext(ctx, c.s.Dialect().Rebind("SELECT key FROM repositories WHERE key = ?"+c.s.LockClause(ctx)), key).Scan(&found)
	return c.s.Wrap("lock virtual copy repository", err)
}

func (c copyRecords) Read(ctx context.Context, key, path string) (virtual.Copy, error) {
	var data string
	err := c.s.Q(ctx).QueryRowContext(ctx, c.s.Dialect().Rebind("SELECT document FROM virtual_copies WHERE virtual_key = ? AND path = ?"), key, path).Scan(&data)
	if err != nil {
		return virtual.Copy{}, c.s.Wrap("get virtual copy", err)
	}
	var out virtual.Copy
	if err = json.Unmarshal([]byte(data), &out); err != nil {
		return out, fmt.Errorf("decode virtual copy: %w", err)
	}
	return out, nil
}

func (c copyRecords) Write(ctx context.Context, v virtual.Copy) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode virtual copy: %w", err)
	}
	_, err = c.s.Exec(
		ctx,
		"put virtual copy",
		dialect.Upsert("virtual_copies", []string{"virtual_key", "path", "document"}, []string{"virtual_key", "path"}),
		v.Virtual,
		v.Path,
		string(data),
	)
	return err
}

func (c copyRecords) List(ctx context.Context, key string) ([]virtual.Copy, error) {
	var out []virtual.Copy
	err := c.s.Rows(ctx, "list virtual copies", "SELECT document FROM virtual_copies WHERE virtual_key = ? ORDER BY path", []any{key}, func(r *sql.Rows) error {
		var data string
		if err := r.Scan(&data); err != nil {
			return fmt.Errorf("scan virtual copy: %w", err)
		}
		var v virtual.Copy
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			return fmt.Errorf("decode virtual copy: %w", err)
		}
		out = append(out, v)
		return nil
	})
	return out, err
}

func (c copyRecords) Delete(ctx context.Context, key string) error {
	_, err := c.s.Exec(ctx, "delete virtual copies", "DELETE FROM virtual_copies WHERE virtual_key = ?", key)
	return err
}
