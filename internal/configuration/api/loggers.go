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
// Set and Delete must not join an enclosing transaction on the module's
// metadata store; such calls return ErrInvalidLoggerSettings before changing
// a runtime decision or timer. Level may read within an existing transaction.
type Loggers interface {
	Set(ctx context.Context, classes map[string]string, seconds int) error
	Delete(ctx context.Context) error
	Level(ctx context.Context, name string) (string, error)
}
