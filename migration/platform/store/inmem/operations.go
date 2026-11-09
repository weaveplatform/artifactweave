package inmem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// Settings returns the settings store, which keeps each value as the JSON
// it encodes to, so a read decodes what the SQL store's would.
func (s *Store) Settings() settings.SettingsStore { return settingStore{s} }

type settingStore struct{ s *Store }

func (st settingStore) values() *memtx.Table[string, string] {
	return memtx.TableOf[string, string](st.s.Store, "settings")
}

func (st settingStore) Get(ctx context.Context, key string, into any) error {
	return st.s.Do(ctx, func() error {
		v, ok := st.values().Get(key)
		if !ok {
			return memtx.NotFound("setting", key)
		}
		if err := json.Unmarshal([]byte(v), into); err != nil {
			return fmt.Errorf("decode setting %s: %w", key, err)
		}
		return nil
	})
}

func (st settingStore) Put(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	return st.s.Do(ctx, func() error {
		st.values().Put(key, string(b))
		return nil
	})
}

func (st settingStore) Delete(ctx context.Context, key string) error {
	return st.s.Do(ctx, func() error {
		if !st.values().Del(key) {
			return memtx.NotFound("setting", key)
		}
		return nil
	})
}

func (st settingStore) Keys(ctx context.Context) ([]string, error) {
	var out []string
	err := st.s.Do(ctx, func() error {
		for k := range st.values().Rows {
			out = append(out, k)
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

// Leases returns the lease store.
func (s *Store) Leases() work.LeaseStore { return leaseStore{s} }

type leaseStore struct{ s *Store }

func (l leaseStore) leases() *memtx.Table[string, work.Lease] {
	return memtx.TableOf[string, work.Lease](l.s.Store, "leases")
}

func (l leaseStore) Acquire(ctx context.Context, lease work.Lease, now time.Time) (bool, error) {
	if lease.Name == "" || lease.Holder == "" || lease.Expires.IsZero() {
		return false, fmt.Errorf(
			"lease %q: name, holder and expiry are required: %w",
			lease.Name,
			kernel.ErrInvalid,
		)
	}
	var took bool
	err := l.s.Do(ctx, func() error {
		cur, ok := l.leases().Get(lease.Name)
		// Compare at stored precision, as the SQL condition expires_at <= now does.
		if ok && cur.Holder != lease.Holder && cur.Expires.UnixMicro() > now.UnixMicro() {
			return nil
		}
		l.leases().Put(lease.Name, lease)
		took = true
		return nil
	})
	return took, err
}

func (l leaseStore) Release(ctx context.Context, name, holder string) error {
	return l.s.Do(ctx, func() error {
		if cur, ok := l.leases().Get(name); ok && cur.Holder == holder {
			l.leases().Del(name)
		}
		return nil
	})
}

func (l leaseStore) List(ctx context.Context) ([]work.Lease, error) {
	var out []work.Lease
	err := l.s.Do(ctx, func() error {
		out = l.leases().
			All(nil, func(a, b work.Lease) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

// Configs returns the config document store. Its writes call the watches
// given to New, as configdoc.Watch describes.
func (s *Store) Configs() configdoc.ConfigStore { return configStore{s} }

type configStore struct{ s *Store }

type configKey struct {
	kind configdoc.ConfigKind
	key  string
}

func (c configStore) docs() *memtx.Table[configKey, configdoc.ConfigDocument] {
	return memtx.TableOf[configKey, configdoc.ConfigDocument](c.s.Store, "config_documents")
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
	return c.s.Tx(ctx, func(ctx context.Context) error {
		old, exists := c.docs().Get(configKey{d.Kind, d.Key})
		// The SQL stores keep no row for an empty secret, so neither does this.
		secrets := make(map[string]string, len(d.Secrets))
		for k, v := range d.Secrets {
			if v != "" {
				secrets[k] = v
			}
		}
		d.Secrets = secrets
		c.docs().Put(configKey{d.Kind, d.Key}, d)
		if !exists || !bytes.Equal(old.Document, d.Document) {
			return c.s.notify(ctx, d.Kind, d.Key)
		}
		return nil
	})
}

func (c configStore) Get(
	ctx context.Context,
	kind configdoc.ConfigKind,
	key string,
) (configdoc.ConfigDocument, error) {
	var out configdoc.ConfigDocument
	err := c.s.Do(ctx, func() error {
		var ok bool
		if out, ok = c.docs().Get(configKey{kind, key}); !ok {
			return memtx.NotFound("config "+string(kind), key)
		}
		return nil
	})
	return out, err
}

func (c configStore) List(
	ctx context.Context,
	kind configdoc.ConfigKind,
) ([]configdoc.ConfigDocument, error) {
	var out []configdoc.ConfigDocument
	err := c.s.Do(ctx, func() error {
		out = c.docs().
			All(func(k configKey, _ configdoc.ConfigDocument) bool { return k.kind == kind },
				func(a, b configdoc.ConfigDocument) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c configStore) Delete(ctx context.Context, kind configdoc.ConfigKind, key string) error {
	return c.s.Tx(ctx, func(ctx context.Context) error {
		if !c.docs().Del(configKey{kind, key}) {
			return memtx.NotFound("config "+string(kind), key)
		}
		return c.s.notify(ctx, kind, key)
	})
}
