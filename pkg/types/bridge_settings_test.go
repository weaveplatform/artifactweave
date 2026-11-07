package types_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A constant has no identity beyond its type and value, so the bridged keys
// are compared on both. The keys are stored with their values in the settings
// table, and as callers move to the settings names, one may write a setting
// under one name that another reads under the other: a copy that drifted would
// put the two on different rows, and the reader would silently fall back to
// the default.
func TestUnitBridgeSettingsKeys(t *testing.T) {
	for _, c := range []struct{ legacy, target any }{
		{types.SettingUserLockPolicy, settings.SettingUserLockPolicy},
		{types.SettingPasswordExpirationPolicy, settings.SettingPasswordExpirationPolicy},
		{types.SettingPasswordPolicy, settings.SettingPasswordPolicy},
		{types.SettingBasicAuthEnabled, settings.SettingBasicAuthEnabled},
		{types.SettingDefaultTokenExpiry, settings.SettingDefaultTokenExpiry},
		{types.SettingAnonymousAccess, settings.SettingAnonymousAccess},
		{types.SettingTrashcan, settings.SettingTrashcan},
		{types.SettingFolderDownload, settings.SettingFolderDownload},
		{types.SettingBundleRetention, settings.SettingBundleRetention},
		{types.SettingSeeded, settings.SettingSeeded},
		{types.SettingBootstrapDescriptor, settings.SettingBootstrapDescriptor},
		{types.SettingAccessLockSettings, settings.SettingAccessLockSettings},
	} {
		if c.legacy != c.target {
			t.Errorf("types holds %#[1]v (%[1]T), settings holds %#[2]v (%[2]T)",
				c.legacy, c.target)
		}
	}
}
