package ldap

import (
	"context"
	"errors"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

// Entry is a user's directory entry as a login needs it.
type Entry struct {
	// DN is the entry the password was bound as.
	DN string
	// Email is the connection's email attribute, or "" when the entry has
	// none.
	Email string
}

// The refusals a Directory answers. Any other error means the directory
// could not answer at all: it could not be reached, its certificate did not
// verify, it timed out, or it answered something malformed.
var (
	// ErrUnknownUser reports that no lookup path found exactly one entry
	// for the name, or that poisoning protection refused the one found.
	ErrUnknownUser = errors.New("ldap: no directory entry for that user")
	// ErrRefused reports that the directory refused the password.
	ErrRefused = errors.New("ldap: the directory refused the password")
)

// DirectoryError is a directory failure in words a client may read: what
// was being done and why it failed. Neither ever carries the user's or the
// manager's password; Err keeps the cause for the log.
type DirectoryError struct {
	// Op names the step: "connect", "tls", "bind", "manager bind",
	// "search" or "read".
	Op string
	// Reason says why it failed.
	Reason string
	Err    error
}

func (e *DirectoryError) Error() string { return e.Op + ": " + e.Reason }

// Unwrap returns the cause.
func (e *DirectoryError) Unwrap() error { return e.Err }

// Directory is the LDAP directory client: access/adapters/ldap implements
// it, and core reaches the network only through it (decision 0044). Every
// call does network I/O, so none is ever made inside a transaction, and each
// is bounded by its context and the client's own timeouts.
type Directory interface {
	// Authenticate finds name's entry through c's lookup paths, the user DN
	// pattern first and then the search, and binds as it with password. It
	// answers ErrUnknownUser or ErrRefused for a refusal, and a
	// *DirectoryError when the directory could not answer.
	Authenticate(ctx context.Context, c Connection, name, password string) (Entry, error)
	// Test connects as c says, binds as its manager when it has one, and
	// reads the bases its lookups start from, answering the first
	// *DirectoryError.
	Test(ctx context.Context, c Connection) error
	// Lookup finds name's entry through c's lookup paths without a
	// password, bound as c's manager or anonymously, for a sync that acts
	// for a user who is not logging in. It answers ErrUnknownUser when no
	// path finds exactly one entry.
	Lookup(ctx context.Context, c Connection, name string) (Entry, error)
	// Groups runs g's group search through c: every entry under g's base
	// that its filter selects and that has a name.
	Groups(ctx context.Context, c Connection, g accessapi.LDAPGroupSetting) ([]DirectoryGroup, error)
	// GroupsNamed runs g's group search for the groups whose name attribute
	// equals name, compared by the directory's own matching rule for that
	// attribute (case-insensitive for cn), as Create a Group's import asks.
	GroupsNamed(ctx context.Context, c Connection, g accessapi.LDAPGroupSetting, name string) ([]DirectoryGroup, error)
	// UserGroups returns the groups g's strategy finds for the user name
	// whose entry is dn. It never follows one group to another: nesting is
	// the directory's own (a matching rule in the member attribute), so a
	// cycle in the directory cannot make it recurse.
	UserGroups(ctx context.Context, c Connection, g accessapi.LDAPGroupSetting, name, dn string) ([]DirectoryGroup, error)
}

// DirectoryGroup is a group a group setting found, as the directory spells
// it.
type DirectoryGroup struct {
	// Name is the value of the setting's name attribute.
	Name string
	// DN is the group's entry.
	DN string
	// Description is the value of the setting's description attribute, or
	// "".
	Description string
}
