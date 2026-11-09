package sql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// Settings returns the settings store, which keeps each value as the JSON
// it encodes to.
func (s *Store) Settings() settings.SettingsStore { return settingStore{s} }

// settingStore keeps the settings table.
type settingStore struct{ s *Store }

func (st settingStore) Get(ctx context.Context, key string, into any) error {
	var value string
	err := st.s.Q(ctx).
		QueryRowContext(ctx, st.s.Dialect().Rebind("SELECT value FROM settings WHERE key = ?"), key).
		Scan(&value)
	if err != nil {
		return st.s.Wrap("get setting "+key, err)
	}
	if err := json.Unmarshal([]byte(value), into); err != nil {
		return fmt.Errorf("decode setting %s: %w", key, err)
	}
	return nil
}

func (st settingStore) Put(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	_, err = st.s.Exec(
		ctx,
		"put setting "+key,
		dialect.Upsert("settings", []string{"key", "value"}, []string{"key"}),
		key,
		string(b),
	)
	return err
}

func (st settingStore) Delete(ctx context.Context, key string) error {
	return st.s.ExecOne(ctx, "delete setting "+key, "DELETE FROM settings WHERE key = ?", key)
}

func (st settingStore) Keys(ctx context.Context) ([]string, error) {
	var out []string
	err := st.s.Rows(
		ctx,
		"list settings",
		"SELECT key FROM settings ORDER BY key",
		nil,
		func(r *sql.Rows) error {
			var k string
			err := r.Scan(&k)
			out = append(out, k)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

// Leases returns the lease store.
func (s *Store) Leases() work.LeaseStore { return leaseStore{s} }

// leaseStore keeps the leases table.
type leaseStore struct{ s *Store }

func (l leaseStore) Acquire(ctx context.Context, lease work.Lease, now time.Time) (bool, error) {
	if lease.Name == "" || lease.Holder == "" || lease.Expires.IsZero() {
		return false, fmt.Errorf(
			"lease %q: name, holder and expiry are required: %w",
			lease.Name,
			kernel.ErrInvalid,
		)
	}
	var took bool
	err := l.s.Tx(ctx, func(ctx context.Context) error {
		// Insert-if-absent, then a conditional update: on PostgreSQL a racing
		// acquirer either waits for the insert and then fails the condition,
		// or never sees a free row, so exactly one of them takes the lease.
		cols := []string{"name", "holder", "task", "acquired_at", "expires_at"}
		args := []any{
			lease.Name,
			lease.Holder,
			lease.Task,
			dialect.Micros(lease.Acquired),
			lease.Expires.UnixMicro(),
		}
		if _, err := l.s.Exec(
			ctx,
			"acquire lease "+lease.Name,
			dialect.InsertIgnore("leases", cols, []string{"name"}),
			args...); err != nil {
			return err
		}
		n, err := l.s.Exec(
			ctx,
			"acquire lease "+lease.Name,
			"UPDATE leases SET holder = ?, task = ?, "+
				"acquired_at = ?, expires_at = ? WHERE name = ? AND (holder = ? OR expires_at <= ?)",
			lease.Holder,
			lease.Task,
			dialect.Micros(lease.Acquired),
			lease.Expires.UnixMicro(),
			lease.Name,
			lease.Holder,
			now.UnixMicro(),
		)
		took = n == 1
		return err
	})
	return took, err
}

func (l leaseStore) Release(ctx context.Context, name, holder string) error {
	_, err := l.s.Exec(
		ctx,
		"release lease "+name,
		"DELETE FROM leases WHERE name = ? AND holder = ?",
		name,
		holder,
	)
	return err
}

func (l leaseStore) List(ctx context.Context) ([]work.Lease, error) {
	var out []work.Lease
	err := l.s.Rows(
		ctx,
		"list leases",
		"SELECT name, holder, task, acquired_at, expires_at FROM leases ORDER BY name",
		nil,
		func(r *sql.Rows) error {
			var (
				v                 work.Lease
				acquired, expires sql.NullInt64
			)
			err := r.Scan(&v.Name, &v.Holder, &v.Task, &acquired, &expires)
			v.Acquired, v.Expires = dialect.FromMicros(acquired), dialect.FromMicros(expires)
			out = append(out, v)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

// Configs returns the config document store. Its writes call the watches
// given to New, as configdoc.Watch describes.
func (s *Store) Configs() configdoc.ConfigStore { return configStore{s} }

// configStore keeps config_documents and config_secrets: one table for every
// configuration document kind in place of research 15's per-provider and
// descriptor_* tables, since none of those documents is queried by field.
type configStore struct{ s *Store }

// purposeConfigSecret is the sealing purpose of config_secrets.value.
const purposeConfigSecret = "config_secrets.value" // #nosec G101 -- a column name, not a credential

func configSecretID(kind configdoc.ConfigKind, key, name string) string {
	return string(kind) + "/" + key + "/" + name
}

func (c configStore) Put(ctx context.Context, d configdoc.ConfigDocument) error {
	if d.Kind == "" || d.Key == "" {
		return fmt.Errorf(
			"config document %q/%q: kind and key are required: %w",
			d.Kind,
			d.Key,
			kernel.ErrInvalid,
		)
	}
	op := fmt.Sprintf("put config %s/%s", d.Kind, d.Key)
	return c.s.Tx(ctx, func(ctx context.Context) error {
		changed := false
		if c.s.watched(d.Kind) {
			old, err := c.Get(ctx, d.Kind, d.Key)
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return err
			}
			changed = errors.Is(err, kernel.ErrNotFound) || !bytes.Equal(old.Document, d.Document)
		}
		if _, err := c.s.Exec(ctx, op, dialect.Upsert("config_documents",
			[]string{"kind", "key", "enabled", "document", "updated_at"}, []string{"kind", "key"}),
			d.Kind, d.Key, d.Enabled, dialect.RawJSON(d.Document),
			dialect.Micros(d.Updated)); err != nil {
			return err
		}
		if _, err := c.s.Exec(ctx, op, "DELETE FROM config_secrets WHERE kind = ? AND key = ?",
			d.Kind, d.Key); err != nil {
			return err
		}
		for name, value := range d.Secrets {
			if value == "" {
				continue
			}
			sealed, err := c.s.Seal(purposeConfigSecret, configSecretID(d.Kind, d.Key, name), value)
			if err != nil {
				return err
			}
			if _, err := c.s.Exec(
				ctx,
				op,
				dialect.Insert("config_secrets", []string{"kind", "key", "name", "value"}),
				d.Kind,
				d.Key,
				name,
				sealed,
			); err != nil {
				return err
			}
		}
		if changed {
			return c.s.notify(ctx, d.Kind, d.Key)
		}
		return nil
	})
}

// list reads documents matching where with their secrets.
func (c configStore) list(
	ctx context.Context,
	op, where string,
	args ...any,
) ([]configdoc.ConfigDocument, error) {
	var out []configdoc.ConfigDocument
	err := c.s.Rows(
		ctx,
		op,
		"SELECT kind, key, enabled, document, updated_at FROM config_documents WHERE "+
			where+" ORDER BY key",
		args,
		func(r *sql.Rows) error {
			var (
				d       configdoc.ConfigDocument
				doc     sql.NullString
				updated sql.NullInt64
			)
			err := r.Scan(&d.Kind, &d.Key, &d.Enabled, &doc, &updated)
			d.Document, d.Updated = dialect.FromRaw(doc), dialect.FromMicros(updated)
			out = append(out, d)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	if err != nil {
		return nil, err
	}
	for i := range out {
		d := &out[i]
		type sealedSecret struct {
			name  string
			value []byte
		}
		var sealed []sealedSecret
		err := c.s.Rows(
			ctx,
			op,
			"SELECT name, value FROM config_secrets WHERE kind = ? AND key = ?",
			[]any{d.Kind, d.Key},
			func(r *sql.Rows) error {
				var v sealedSecret
				err := r.Scan(&v.name, &v.value)
				sealed = append(sealed, v)
				return err //nolint:wrapcheck // wrapped by Rows
			},
		)
		if err != nil {
			return nil, err
		}
		for _, v := range sealed {
			plain, err := c.s.Unseal(
				purposeConfigSecret,
				configSecretID(d.Kind, d.Key, v.name),
				v.value,
			)
			if err != nil {
				return nil, err
			}
			if d.Secrets == nil {
				d.Secrets = map[string]string{}
			}
			d.Secrets[v.name] = plain
		}
	}
	return out, nil
}

func (c configStore) Get(
	ctx context.Context,
	kind configdoc.ConfigKind,
	key string,
) (configdoc.ConfigDocument, error) {
	op := fmt.Sprintf("get config %s/%s", kind, key)
	got, err := c.list(ctx, op, "kind = ? AND key = ?", kind, key)
	if err != nil {
		return configdoc.ConfigDocument{}, err
	}
	if len(got) == 0 {
		return configdoc.ConfigDocument{}, fmt.Errorf("%s: %w", op, kernel.ErrNotFound)
	}
	return got[0], nil
}

func (c configStore) List(
	ctx context.Context,
	kind configdoc.ConfigKind,
) ([]configdoc.ConfigDocument, error) {
	return c.list(ctx, "list config "+string(kind), "kind = ?", kind)
}

func (c configStore) Delete(ctx context.Context, kind configdoc.ConfigKind, key string) error {
	op := fmt.Sprintf("delete config %s/%s", kind, key)
	return c.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := c.s.Exec(
			ctx,
			op,
			"DELETE FROM config_secrets WHERE kind = ? AND key = ?",
			kind,
			key,
		); err != nil {
			return err
		}
		if err := c.s.ExecOne(
			ctx,
			op,
			"DELETE FROM config_documents WHERE kind = ? AND key = ?",
			kind,
			key,
		); err != nil {
			return err
		}
		return c.s.notify(ctx, kind, key)
	})
}
