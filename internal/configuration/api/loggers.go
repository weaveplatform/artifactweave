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
type Loggers interface {
	Set(ctx context.Context, classes map[string]string, seconds int) error
	Delete(ctx context.Context) error
	Level(ctx context.Context, name string) (string, error)
}
