package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// BowerRegistrationStore persists immutable name-to-Git mappings. Create is
// exclusive on (repository, name); repository deletion removes registrations.
type BowerRegistrationStore interface {
	Create(context.Context, model.BowerRegistration) error
	Get(context.Context, string, string) (model.BowerRegistration, error)
	List(context.Context, string, kernel.Page) ([]model.BowerRegistration, string, error)
}
