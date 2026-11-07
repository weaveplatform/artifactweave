package configdoc

import "context"

// ConfigStore persists configuration documents. Their secrets are sealed;
// a secret with an empty value is not stored.
type ConfigStore interface {
	// Put creates or replaces the document (d.Kind, d.Key) and its secrets;
	// kernel.ErrInvalid when the kind or key is empty.
	Put(ctx context.Context, d ConfigDocument) error
	// Get returns one document, secrets included; kernel.ErrNotFound when
	// there is none.
	Get(ctx context.Context, kind ConfigKind, key string) (ConfigDocument, error)
	// List returns the documents of a kind ordered by key.
	List(ctx context.Context, kind ConfigKind) ([]ConfigDocument, error)
	// Delete removes a document; kernel.ErrNotFound when there is none.
	Delete(ctx context.Context, kind ConfigKind, key string) error
}
