package settings

// Setting keys of the singleton settings table.
const (
	SettingUserLockPolicy           = "security.user_lock_policy"
	SettingPasswordExpirationPolicy = "security.password_expiration_policy"
	SettingPasswordPolicy           = "security.password_policy"
	SettingBasicAuthEnabled         = "security.basic_auth_enabled"
	SettingDefaultTokenExpiry       = "token.default_expiry"
	SettingAnonymousAccess          = "security.anonymous_access"
	SettingTrashcan                 = "trashcan"
	// SettingFolderDownload holds FolderDownloadSettings
	// (folderDownloadConfig in the descriptor).
	SettingFolderDownload  = "folder_download"
	SettingBundleRetention = "distribution.bundle_retention"
	// SettingSeeded marks that the built-in principals were created, so a
	// later start never recreates one an administrator removed.
	SettingSeeded = "bootstrap.seeded"
	// SettingBootstrapDescriptor marks that bootstrap.descriptor was
	// applied, so it is applied once.
	SettingBootstrapDescriptor = "bootstrap.descriptor_applied"
	// SettingAccessLockSettings holds AccessLockSettings, the Access user
	// lock policy's fields that the lock and expiration policies have no
	// place for.
	SettingAccessLockSettings = "security.access_lock_settings"
)
