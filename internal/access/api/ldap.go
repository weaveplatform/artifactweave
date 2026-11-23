package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// LDAPConnection is one LDAP server connection as other modules see it: the
// effective settings, never the search manager's password. The descriptor's
// security.ldapSettings section and the Access LDAP API read and write the
// same connections, so one value describes both.
type LDAPConnection struct {
	Key                      string
	Enabled                  bool
	URL                      string
	UserDNPattern            string
	EmailAttribute           string
	AutoCreateUser           bool
	PoisoningProtection      bool
	AllowUserToAccessProfile bool
	PagingSupportEnabled     bool
	SearchFilter             string
	SearchBase               string
	SearchSubTree            bool
	ManagerDN                string
}

// LDAPChange is one connection a descriptor creates, updates or deletes. A
// nil field is left as stored, or takes its default on a connection the
// change creates; a set string, even an empty one, replaces the stored
// value, so the descriptor's null on a string arrives here as "".
// ManagerPassword is the one secret: "" clears the stored password and nil
// keeps it.
type LDAPChange struct {
	Key    string
	Delete bool

	Enabled                  *bool
	URL                      *string
	UserDNPattern            *string
	EmailAttribute           *string
	AutoCreateUser           *bool
	PoisoningProtection      *bool
	AllowUserToAccessProfile *bool
	PagingSupportEnabled     *bool
	SearchFilter             *string
	SearchBase               *string
	SearchSubTree            *bool
	ManagerDN                *string
	ManagerPassword          *string
}

// LDAPOutcome is what a change does, or with a plan would do, to its
// connection.
type LDAPOutcome int

// The outcomes of an LDAPChange.
const (
	LDAPUnchanged LDAPOutcome = iota
	LDAPCreated
	LDAPUpdated
	LDAPDeleted
)

// LDAPResult names a change's connection and its outcome.
type LDAPResult struct {
	Key     string
	Outcome LDAPOutcome
}

// LDAPInvalid refuses a connection. Field is the Access API's name of the
// field it is about ("search.manager_dn"), or "" for the connection as a
// whole, so each surface can name the field in its own spelling. It matches
// kernel.ErrInvalid. Its text reaches clients and never carries a secret.
type LDAPInvalid struct {
	// Key is the connection refused, which a descriptor names; the Access
	// API addresses one connection per request, so its message leaves it out.
	Key    string
	Field  string
	Reason string
}

func (e *LDAPInvalid) Error() string {
	if e.Field == "" {
		return "the LDAP connection " + e.Reason
	}
	return e.Field + " " + e.Reason
}

// Unwrap makes a refusal match kernel.ErrInvalid.
func (e *LDAPInvalid) Unwrap() error { return kernel.ErrInvalid }

// LDAPSettings reads and writes the LDAP connections for the configuration
// descriptor. Each call uses ctx for the store it reads or writes, so a call
// inside a store transaction takes part in it and a refused or failed
// descriptor writes nothing. A refusal is an *LDAPInvalid.
type LDAPSettings interface {
	// Connections returns every connection, by key.
	Connections(ctx context.Context) ([]LDAPConnection, error)
	// Plan checks every change against the stored connections and returns
	// each one's outcome, in the order given, without writing.
	Plan(ctx context.Context, changes []LDAPChange) ([]LDAPResult, error)
	// Apply checks the changes as Plan does and writes those that change a
	// connection.
	Apply(ctx context.Context, changes []LDAPChange) ([]LDAPResult, error)
}
