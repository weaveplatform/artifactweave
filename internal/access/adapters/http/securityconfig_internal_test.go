package accesshttp

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// securityConfig fails with the store's error whichever setting it cannot
// read, and reads a never-written anonymous or hiding switch as off.
func TestUnitSecurityConfigStoreFailures(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	doc, err := securityConfig(ctx, db.Settings())
	if err != nil || doc.AnonAccessEnabled || !doc.BasicAuthEnabled || doc.HideUnauthorizedResources ||
		doc.PasswordSettings.EncryptionPolicy != "SUPPORTED" {
		t.Fatalf("defaults %+v %v", doc, err)
	}
	boom := errors.New("settings down")
	for _, key := range []string{
		settings.SettingAnonymousAccess, settings.SettingBasicAuthEnabled, settings.SettingUserLockPolicy,
		settings.SettingPasswordExpirationPolicy, settings.SettingPasswordEncryption,
		settings.SettingHideUnauthorizedResources,
	} {
		if _, err := securityConfig(ctx, keyedSettings{db.Settings(), key, boom}); !errors.Is(err, boom) {
			t.Errorf("%s failing: %v", key, err)
		}
	}
}

// keyedSettings fails reading one setting.
type keyedSettings struct {
	settings.SettingsStore
	key string
	err error
}

func (s keyedSettings) Get(ctx context.Context, key string, into any) error {
	if key == s.key {
		return s.err
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // a pass-through test double
}
