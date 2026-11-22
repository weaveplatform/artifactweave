package api

import (
	"context"
	"errors"
)

// ErrInvalidLoggerSettings reports a refused temporary logger request.
var ErrInvalidLoggerSettings = errors.New("invalid logger settings")

// Loggers administers one temporary, process-local logger session. Replacing a
// session requires Delete before Set. Names unknown to the process have no
// effect and read back the current root level. No setting is persisted.
// Calls are serialized with session changes and expiry and require a current
// administrator, including the credential's current token ceilings. Authority
// failures retain kernel.ErrUnauthenticated, kernel.ErrBadCredentials or
// kernel.ErrForbidden. Unavailable controls and closed owners return
// kernel.ErrStore; metadata errors remain wrapped.
// Every operation requires a standalone call: a context inside a transaction
// on the module's metadata store returns ErrInvalidLoggerSettings before taking
// a logger lock or changing a runtime decision or timer.
type Loggers interface {
	// Set applies 1 to 100 named levels for a positive number of seconds that
	// fits time.Duration. Levels are trace, debug, info, warn, error or fatal;
	// trace maps to debug and fatal to error. Invalid input or an existing
	// session returns ErrInvalidLoggerSettings without changing levels or timers.
	// Success takes effect immediately and expiry restores the current baseline.
	Set(ctx context.Context, classes map[string]string, seconds int) error
	// Delete restores the current baseline and joins the session's timer callback
	// before returning. An absent session succeeds after the authority check;
	// a refused call preserves the active levels and timer.
	Delete(ctx context.Context) error
	// Level returns debug, info, warn or error for name, using the current root
	// level for an unknown name. A refused call returns an empty string and its
	// classified error without changing the session.
	Level(ctx context.Context, name string) (string, error)
}
