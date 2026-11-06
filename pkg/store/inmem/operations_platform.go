package inmem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Settings implements store.Store.
func (s *Store) Settings() store.SettingsStore { return settingStore{s} }

// settingStore keeps values as JSON, so a read decodes exactly what the SQL
// stores would.
type settingStore struct{ s *Store }

func (st settingStore) values() *table[string, string] {
	return tableOf[string, string](st.s, "settings")
}

func (st settingStore) Get(ctx context.Context, key string, into any) error {
	return st.s.do(ctx, func() error {
		v, ok := st.values().get(key)
		if !ok {
			return notFound("setting", key)
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
	return st.s.do(ctx, func() error {
		st.values().put(key, string(b))
		return nil
	})
}

func (st settingStore) Delete(ctx context.Context, key string) error {
	return st.s.do(ctx, func() error {
		if !st.values().del(key) {
			return notFound("setting", key)
		}
		return nil
	})
}

func (st settingStore) Keys(ctx context.Context) ([]string, error) {
	var out []string
	err := st.s.do(ctx, func() error {
		for k := range st.values().rows {
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

func (l leaseStore) leases() *table[string, types.Lease] {
	return tableOf[string, types.Lease](l.s, "leases")
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
	err := l.s.do(ctx, func() error {
		cur, ok := l.leases().get(lease.Name)
		// Compare at stored precision, as the SQL condition expires_at <= now does.
		if ok && cur.Holder != lease.Holder && cur.Expires.UnixMicro() > now.UnixMicro() {
			return nil
		}
		l.leases().put(lease.Name, lease)
		took = true
		return nil
	})
	return took, err
}

func (l leaseStore) Release(ctx context.Context, name, holder string) error {
	return l.s.do(ctx, func() error {
		if cur, ok := l.leases().get(name); ok && cur.Holder == holder {
			l.leases().del(name)
		}
		return nil
	})
}

func (l leaseStore) List(ctx context.Context) ([]types.Lease, error) {
	var out []types.Lease
	err := l.s.do(ctx, func() error {
		out = l.leases().
			all(nil, func(a, b types.Lease) int { return strings.Compare(a.Name, b.Name) })
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

func (c configStore) docs() *table[configKey, types.ConfigDocument] {
	return tableOf[configKey, types.ConfigDocument](c.s, "config_documents")
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
		old, exists := c.docs().get(configKey{d.Kind, d.Key})
		// The SQL stores keep no row for an empty secret, so neither does this.
		secrets := make(map[string]string, len(d.Secrets))
		for k, v := range d.Secrets {
			if v != "" {
				secrets[k] = v
			}
		}
		d.Secrets = secrets
		c.docs().put(configKey{d.Kind, d.Key}, d)
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
	err := c.s.do(ctx, func() error {
		var ok bool
		if out, ok = c.docs().get(configKey{kind, key}); !ok {
			return notFound("config "+string(kind), key)
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
	err := c.s.do(ctx, func() error {
		out = c.docs().all(func(k configKey, _ types.ConfigDocument) bool { return k.kind == kind },
			func(a, b types.ConfigDocument) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c configStore) Delete(ctx context.Context, kind types.ConfigKind, key string) error {
	return c.s.Tx(ctx, func(ctx context.Context) error {
		if !c.docs().del(configKey{kind, key}) {
			return notFound("config "+string(kind), key)
		}
		if kind == types.ConfigLayout {
			return store.FenceLayoutSources(ctx, c.s, remoteRecords{c.s}, key)
		}
		return nil
	})
}
