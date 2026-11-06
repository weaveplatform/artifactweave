package settings

import "context"

// SettingsStore persists the singleton settings (types.Setting* keys) as
// JSON values.
type SettingsStore interface {
	// Get decodes the value stored under key into into; ErrNotFound when
	// nothing is stored, which callers read as "use the default".
	Get(ctx context.Context, key string, into any) error
	// Put stores v under key, replacing any value.
	Put(ctx context.Context, key string, v any) error
	// Delete removes the value, restoring the default; ErrNotFound when
	// nothing was stored.
	Delete(ctx context.Context, key string) error
	// Keys returns the keys with stored values, ordered.
	Keys(ctx context.Context) ([]string, error)
}
