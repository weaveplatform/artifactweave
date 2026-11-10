package access

import (
	"errors"
	"log/slog"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
)

// Store is the access data the bindings read and write: the access ports
// and the settings that hold the anonymous access switch. The application
// passes its store aggregate. Every call joins the transaction ctx carries.
type Store interface {
	port.Store
	// Settings holds the anonymous access switch, which AnonymousAccess
	// reads and writes and the authorizer reads.
	Settings() settings.SettingsStore
}

// errNoStore refuses Deps without a store, which every binding reads.
var errNoStore = errors.New("access: Deps.Store is required")

// Deps is what app passes New. The authenticator and the token service of
// access core are built by app, over the same store, until the module builds
// them itself.
type Deps struct {
	// Store holds the users, groups, permissions, tokens, keys, projects,
	// settings and API restrictions that the bindings read and write. It is
	// required.
	Store Store
	// Authenticator verifies credentials; nil leaves API().Authenticator nil,
	// as a server without authentication has none.
	Authenticator *authn.Authenticator
	// Tokens issues and revokes access tokens; nil leaves API().Tokens nil,
	// which the token endpoints answer 501.
	Tokens *token.Service
	// Settings are the access options that API() publishes to the other
	// modules.
	Settings accessapi.Settings
	// Now is the clock of the services the module builds itself; nil means
	// time.Now. New builds none: Authenticator and Tokens keep the clocks
	// app built them with, and every other binding takes the times it
	// compares from its caller, so New does not read it.
	Now func() time.Time
	// Log receives the logs of the services the module builds itself; nil
	// means slog.Default(). New builds none, and the bindings log nothing:
	// each returns its errors to its caller.
	Log *slog.Logger
}

// Module is the access module as app holds it. It is safe for concurrent
// use: its bindings hold no state of their own beyond what Deps gave them.
type Module struct {
	api accessapi.API
}

// New binds every access/api contract over d's store and services. It does
// no I/O, so it cannot fail on a store that is unreachable; it refuses only
// Deps without a store. The bindings keep every error text of the code they
// replace, and each call uses its caller's context, so a call inside a store
// transaction takes part in it.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	m := &Module{
		api: accessapi.API{
			Authorizer:      authorizer{st: d.Store},
			Keys:            KeysOf(d.Store),
			Identity:        identity{st: d.Store},
			Restrictions:    restrictions{st: d.Store},
			Projects:        ProjectsOf(d.Store),
			Grants:          grants{st: d.Store},
			AnonymousAccess: AnonymousAccessOf(d.Store),
			Settings:        d.Settings,
		},
	}
	// Assigned only when present: a nil pointer stored in the interface
	// would be a typed nil, which the callers' nil checks would let through.
	if d.Authenticator != nil {
		m.api.Authenticator = authenticator{a: d.Authenticator, st: d.Store}
	}
	if d.Tokens != nil {
		m.api.Tokens = tokens{svc: d.Tokens, st: d.Store}
	}
	return m, nil
}

// API returns the module's published surface: one binding of each access/api
// contract, with Authenticator and Tokens nil when New had no such service,
// and the Settings from Deps.
func (m *Module) API() accessapi.API { return m.api }

// KeysOf returns the Keys binding over st alone, for code that holds a store
// before any module exists, such as bootstrap and reseal. The errors are the
// store's, unwrapped.
func KeysOf(st port.Store) accessapi.Keys { return keys{st: st} }

// ProjectsOf returns the Projects binding over st alone, for code that holds
// a store before any module exists. The errors are the store's, unwrapped.
func ProjectsOf(st port.Store) accessapi.Projects { return projects{st: st} }

// AnonymousAccessOf returns the AnonymousAccess binding over st alone, for
// code that holds a store before any module exists, such as applying the
// bootstrap descriptor. The errors are the store's, unwrapped.
func AnonymousAccessOf(
	st interface{ Settings() settings.SettingsStore },
) accessapi.AnonymousAccess {
	return anonymousAccess{st: st}
}
