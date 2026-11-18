package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// ErrInvalidKeyEntry reports a key entry that is not a JSON object.
var ErrInvalidKeyEntry = errors.New("invalid signing key entry")

// KeysMerger keeps each first complete key entry in effective member order.
// Raw JSON preserves upstream expiry, algorithm and future fields. A row whose
// keyid is not a string stays opaque, as on the established direct remote route.
// [I] Public 7.x documentation does not settle keyid collision ownership (#216).
type KeysMerger struct{}

// Merge returns an immutable first-keyid union without reinterpreting key fields.
func (KeysMerger) Merge(ctx context.Context, parts []virtual.Part[[]json.RawMessage]) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("npm keys merge: %w", err)
	}
	seen := map[string]bool{}
	out := []json.RawMessage{}
	for _, part := range parts {
		for _, row := range part.Doc {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("npm keys merge: %w", err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(row, &fields) != nil || fields == nil {
				return nil, fmt.Errorf("npm keys merge: %w", ErrInvalidKeyEntry)
			}
			var id *string
			if json.Unmarshal(fields["keyid"], &id) == nil && id != nil {
				if seen[*id] {
					continue
				}
				seen[*id] = true
			}
			out = append(out, bytes.Clone(row))
		}
	}
	return out, nil
}
