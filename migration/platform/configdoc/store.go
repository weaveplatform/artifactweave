package configdoc

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ConfigStore persists configuration documents. Their secrets are sealed;
// a secret with an empty value is not stored.
type ConfigStore interface {
	// Put creates or replaces the document (d.Kind, d.Key) and its secrets;
	// ErrInvalid when the kind or key is empty.
	Put(ctx context.Context, d types.ConfigDocument) error
	// Get returns one document, secrets included.
	Get(ctx context.Context, kind types.ConfigKind, key string) (types.ConfigDocument, error)
	// List returns the documents of a kind ordered by key.
	List(ctx context.Context, kind types.ConfigKind) ([]types.ConfigDocument, error)
	// Delete removes a document.
	Delete(ctx context.Context, kind types.ConfigKind, key string) error
}
