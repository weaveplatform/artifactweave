package ldap

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	platformmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitLoginsEnabled(t *testing.T) {
	w := newLoginWorld(t)
	ctx := t.Context()
	for _, step := range []struct {
		set  func(*accessapi.LDAPChange)
		want bool
	}{
		{nil, false},
		{func(ch *accessapi.LDAPChange) { ch.Enabled = ptr(false) }, false},
		{func(ch *accessapi.LDAPChange) { ch.Key = "on" }, true},
	} {
		if step.set != nil {
			ch := searchChange("off")
			step.set(&ch)
			if _, err := w.svc.Create(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		if on, err := w.logins.Enabled(ctx); err != nil || on != step.want {
			t.Fatalf("Enabled %t %v, want %t", on, err, step.want)
		}
	}
	svc := w.svc
	svc.Configs = failingConfigs{ConfigStore: w.svc.Configs, op: "list"}
	if _, err := NewLogins(svc, w.dir, LoginOptions{}).Enabled(ctx); !errors.Is(err, errStore) {
		t.Fatalf("Enabled with a failing list: %v", err)
	}
}

// TestUnitEnablingTurnsInternalPasswordsOff: writing an enabled connection
// turns internal password login off while the switch was never written, in
// the connection's own write, and never overwrites an administrator's
// choice; a disabled connection leaves the switch alone.
func TestUnitEnablingTurnsInternalPasswordsOff(t *testing.T) {
	ctx := context.Background()
	db := memtx.New()
	store := platformmem.New(db)
	svc := Service{Configs: store.Configs(), Settings: store.Settings()}
	stored := func() (bool, error) {
		var on bool
		err := store.Settings().Get(ctx, settings.SettingBasicAuthEnabled, &on)
		return on, err
	}
	off := searchChange("off")
	off.Enabled = ptr(false)
	if _, err := svc.Create(ctx, off); err != nil {
		t.Fatal(err)
	}
	if _, err := stored(); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("a disabled connection wrote the switch", err)
	}
	if _, err := svc.Apply(ctx, []accessapi.LDAPChange{searchChange("corp")}); err != nil {
		t.Fatal(err)
	}
	if on, err := stored(); err != nil || on {
		t.Fatalf("after the first enabled connection: %t %v", on, err)
	}
	// An administrator turns internal passwords back on; later writes keep it.
	if err := store.Settings().Put(ctx, settings.SettingBasicAuthEnabled, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Replace(ctx, searchChange("corp")); err != nil {
		t.Fatal(err)
	}
	if on, err := stored(); err != nil || !on {
		t.Fatalf("a later write overwrote the administrator's choice: %t %v", on, err)
	}
	// Without Settings nothing is read or written.
	if _, err := (Service{Configs: store.Configs()}).Create(ctx, searchChange("bare")); err != nil {
		t.Fatal(err)
	}
	for op, broken := range map[string]Service{
		"get": {Configs: store.Configs(), Settings: failingSettings{store.Settings(), "get"}},
		"put": {Configs: store.Configs(), Settings: failingSettings{store.Settings(), "put"}},
	} {
		if err := store.Settings().Delete(ctx, settings.SettingBasicAuthEnabled); err != nil &&
			!errors.Is(err, kernel.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := broken.Replace(ctx, searchChange("corp")); !errors.Is(err, errStore) {
			t.Fatalf("a failing %s: %v", op, err)
		}
	}
}

type failingSettings struct {
	settings.SettingsStore
	op string
}

func (f failingSettings) Get(ctx context.Context, key string, v any) error {
	if f.op == "get" {
		return errStore
	}
	return f.SettingsStore.Get(ctx, key, v) //nolint:wrapcheck // test double
}

func (f failingSettings) Put(ctx context.Context, key string, v any) error {
	if f.op == "put" {
		return errStore
	}
	return f.SettingsStore.Put(ctx, key, v) //nolint:wrapcheck // test double
}
