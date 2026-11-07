// Bridge (P9d): declared in migration/platform/settings; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/settings"

const (
	SettingUserLockPolicy           = settings.SettingUserLockPolicy
	SettingPasswordExpirationPolicy = settings.SettingPasswordExpirationPolicy
	SettingPasswordPolicy           = settings.SettingPasswordPolicy
	SettingBasicAuthEnabled         = settings.SettingBasicAuthEnabled
	SettingDefaultTokenExpiry       = settings.SettingDefaultTokenExpiry
	SettingAnonymousAccess          = settings.SettingAnonymousAccess
	SettingTrashcan                 = settings.SettingTrashcan
	SettingFolderDownload           = settings.SettingFolderDownload
	SettingBundleRetention          = settings.SettingBundleRetention
	SettingSeeded                   = settings.SettingSeeded
	SettingBootstrapDescriptor      = settings.SettingBootstrapDescriptor
	SettingAccessLockSettings       = settings.SettingAccessLockSettings
)
