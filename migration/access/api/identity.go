package api

import "context"

// userEnabled is the stored status of an enabled account, the user model's
// UserEnabled, which this package cannot import (decision 0044, dependency
// rule 5). A test in the model's package keeps the two equal.
const userEnabled = "enabled"

// UserInfo is what other modules read about a user account. The password
// hash and the account's other fields stay inside access.
type UserInfo struct {
	Name, Email string
	Admin       bool
	Groups      []string
	// Status is the Access account status: "enabled", "disabled", "locked"
	// or "invited", or empty for an account stored without one.
	Status string
}

// Enabled reports whether the account may act. An empty status counts as
// enabled, as it does everywhere in access, and any status other than
// "enabled", compared exactly, does not.
func (u UserInfo) Enabled() bool { return u.Status == "" || u.Status == userEnabled }

// Identity reads user accounts and the groups new users join, for modules
// that resolve a user named outside the request's principal, such as the
// subject of a signed URL. Each call uses ctx for the store it reads, so a
// call inside a store transaction takes part in it.
type Identity interface {
	// User returns the account called name; an error matching
	// kernel.ErrNotFound when there is none.
	User(ctx context.Context, name string) (UserInfo, error)
	// AutoJoinGroups returns the names of the groups new users join, in
	// order. A transient user, which a token names while no stored account
	// has its name, acts through these groups.
	AutoJoinGroups(ctx context.Context) ([]string, error)
}
