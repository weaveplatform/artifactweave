package ldap

import (
	"context"
	"errors"
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
}
