package store

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BowerRegistrationStore persists immutable name-to-Git mappings. Create is
// exclusive on (repository, name); repository deletion removes registrations.
type BowerRegistrationStore interface {
	Create(context.Context, types.BowerRegistration) error
	Get(context.Context, string, string) (types.BowerRegistration, error)
	List(context.Context, string, Page) ([]types.BowerRegistration, string, error)
}
