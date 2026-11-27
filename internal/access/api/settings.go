package api

import (
	"context"
	"time"
)

// Settings are the access options of the application configuration that
// other modules read. The zero value behaves as the defaults do.
type Settings struct {
	// NativeLoginEnabled opts into the shared native-client approval page.
	NativeLoginEnabled bool
	// NativeLoginOrigin is the canonical browser origin, never a request header.
	NativeLoginOrigin string
	// NativeLoginTokenExpiry uses npm.login_token_expiry for shared approval tokens.
	NativeLoginTokenExpiry time.Duration
	// AllowBasicAuthTokens lets a password sent with Basic create tokens
	// through POST /access/api/v1/tokens (token.allow_basic_auth); false
	// refuses it, as Artifactory 7.x does by default.
	AllowBasicAuthTokens bool
	// GlobalEnvironmentLimit caps the global environments, DEV and PROD
	// included (environments.global_limit); zero or less means Access's
	// default of 10.
	GlobalEnvironmentLimit int
}

// AnonymousAccess reads and switches the security.anonymous_access setting,
// which lets a request without credentials act as the anonymous user. Each
// call uses ctx for the store it reads or writes, so a call inside a store
// transaction takes part in it.
type AnonymousAccess interface {
	// Enabled reports whether anonymous access is on. A setting that was
	// never written is an error matching kernel.ErrNotFound, which callers
	// read as off.
	Enabled(ctx context.Context) (bool, error)
	// SetEnabled turns anonymous access on or off.
	SetEnabled(ctx context.Context, on bool) error
}
