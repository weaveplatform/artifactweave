package inmem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Settings implements store.Store.
func (s *Store) Settings() store.SettingsStore { return settingStore{s} }

// settingStore keeps values as JSON, so a read decodes exactly what the SQL
// stores would.
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

// Leases implements store.Store.
func (s *Store) Leases() store.LeaseStore { return leaseStore{s} }

type leaseStore struct{ s *Store }

func (l leaseStore) leases() *memtx.Table[string, types.Lease] {
	return memtx.TableOf[string, types.Lease](l.s.Store, "leases")
}

func (l leaseStore) Acquire(ctx context.Context, lease types.Lease, now time.Time) (bool, error) {
	if lease.Name == "" || lease.Holder == "" || lease.Expires.IsZero() {
		return false, fmt.Errorf(
			"lease %q: name, holder and expiry are required: %w",
			lease.Name,
			store.ErrInvalid,
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

func (l leaseStore) List(ctx context.Context) ([]types.Lease, error) {
	var out []types.Lease
	err := l.s.Do(ctx, func() error {
		out = l.leases().
			All(nil, func(a, b types.Lease) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

// Configs implements store.Store.
func (s *Store) Configs() store.ConfigStore { return configStore{s} }

type configStore struct{ s *Store }

type configKey struct {
	kind types.ConfigKind
	key  string
}

func (c configStore) docs() *memtx.Table[configKey, types.ConfigDocument] {
	return memtx.TableOf[configKey, types.ConfigDocument](c.s.Store, "config_documents")
}

func (c configStore) Put(ctx context.Context, d types.ConfigDocument) error {
	if d.Kind == "" || d.Key == "" {
		return fmt.Errorf(
			"config document %q/%q: kind and key are required: %w",
			d.Kind,
			d.Key,
			store.ErrInvalid,
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
		if d.Kind == types.ConfigLayout && (!exists || !bytes.Equal(old.Document, d.Document)) {
			return store.FenceLayoutSources(ctx, c.s, remoteRecords{c.s}, d.Key)
		}
		return nil
	})
}

func (c configStore) Get(
	ctx context.Context,
	kind types.ConfigKind,
	key string,
) (types.ConfigDocument, error) {
	var out types.ConfigDocument
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
	kind types.ConfigKind,
) ([]types.ConfigDocument, error) {
	var out []types.ConfigDocument
	err := c.s.Do(ctx, func() error {
		out = c.docs().All(func(k configKey, _ types.ConfigDocument) bool { return k.kind == kind },
			func(a, b types.ConfigDocument) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c configStore) Delete(ctx context.Context, kind types.ConfigKind, key string) error {
	return c.s.Tx(ctx, func(ctx context.Context) error {
		if !c.docs().Del(configKey{kind, key}) {
			return memtx.NotFound("config "+string(kind), key)
		}
		if kind == types.ConfigLayout {
			return store.FenceLayoutSources(ctx, c.s, remoteRecords{c.s}, key)
		}
		return nil
	})
}
