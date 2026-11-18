package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// NPMExternalStore persists external dependency identities. Put refuses a
// different URL/file for an existing (virtual,name,spec); an empty digest keeps
// the previously admitted digest. Repository deletion removes its mappings.
type NPMExternalStore interface {
	Put(context.Context, model.NPMExternal) error
	Get(context.Context, string, string, string) (model.NPMExternal, error)
	List(context.Context, string, string) ([]model.NPMExternal, error)
}
