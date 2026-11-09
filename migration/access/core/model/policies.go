package model

// UserLockPolicy locks an account after repeated failed logins.
type UserLockPolicy struct {
	Enabled       bool `json:"enabled"`
	LoginAttempts int  `json:"login_attempts"`
}

// PasswordExpirationPolicy expires internal passwords after MaxAgeDays.
type PasswordExpirationPolicy struct {
	Enabled       bool `json:"enabled"`
	MaxAgeDays    int  `json:"max_age_days"`
	NotifyByEmail bool `json:"notify_by_email"`
}

// PasswordPolicy constrains new internal passwords.
//
// DefaultPasswordPolicy is in force while none is stored, and a replacing
// PUT resets every field it leaves out to it.
type PasswordPolicy struct {
	Uppercase   int  `json:"uppercase"`
	Lowercase   int  `json:"lowercase"`
	Digit       int  `json:"digit"`
	SpecialChar int  `json:"special_char"`
	Length      int  `json:"length"`
	NotMatchOld bool `json:"not_match_old"`
}

// DefaultPasswordPolicy is Access's documented default: no character
// classes required, at least 4 characters, and not the old password
// (docs.jfrog.com, supported Access configurations, security.password-policy).
// The documented rule that a password cannot match the username has no
// field and always applies.
func DefaultPasswordPolicy() PasswordPolicy { return PasswordPolicy{Length: 4, NotMatchOld: true} }

// AccessLockSettings are the Access user lock policy's fields beyond the
// lock limit (UserLockPolicy) and the password expiry
// (PasswordExpirationPolicy). The four login delay fields drive the
// temporary login suspension after failed Basic logins; SecondsToUnlock and
// AdminPasswordExpirable are stored and returned only, as the Access
// OpenAPI source says field by field.
type AccessLockSettings struct {
	SecondsToUnlock                int   `json:"seconds_to_unlock"`
	AdminPasswordExpirable         bool  `json:"admin_password_expirable"`
	MaxLoginDelayIncorrectAttempts int   `json:"max_login_delay_incorrect_attempts"`
	MaxLoginDelayMillis            int64 `json:"max_login_delay_millis"`
	BaseLoginDelayMillis           int64 `json:"base_login_delay_millis"`
	ClientLoginSuspensionEnabled   bool  `json:"client_login_suspension_enabled"`
}

// DefaultAccessLockSettings is in force while none is stored: Access's
// server defaults. docs.jfrog.com, supported Access configurations
// (security.user-lock-policy), documents the two delays and
// seconds-to-unlock. The JFrog KB article on intermittent 403 errors gives
// the 2 failed attempts before a suspension. The getAccessUserLockPolicy
// reference page, the source of the suspension switch, is gone; the
// endpoint is now documented only in the migration notes of the deprecated
// JFrog APIs page.
func DefaultAccessLockSettings() AccessLockSettings {
	return AccessLockSettings{
		SecondsToUnlock:                1,
		MaxLoginDelayIncorrectAttempts: 2,
		MaxLoginDelayMillis:            60000,
		BaseLoginDelayMillis:           1000,
		ClientLoginSuspensionEnabled:   true,
	}
}
