package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// BindToken captures current project lifetimes. Its caller owns the enclosing
// transaction and takes the project guard before identity or token writes.
// Revisions deliberately stay live: changing a role must affect the next use.
func BindToken(ctx context.Context, projects port.ProjectStore, token *model.Token) error {
	keys := token.ProjectKeys()
	if len(keys) == 0 {
		return nil
	}
	token.ProjectIncarnations = make(map[string]string, len(keys))
	for _, key := range keys {
		authority, err := projects.Authority(ctx, key)
		if err != nil {
			return fmt.Errorf("token project %q: %w", key, err)
		}
		token.ProjectIncarnations[key] = string(authority.Incarnation)
	}
	return nil
}

// TokenCurrent checks stored bindings, including reference/refresh credentials.
// An old unbound project scope stays denied on upgrade. Publication callers
// acquire the project guard before any identity, repository or source locks.
func TokenCurrent(ctx context.Context, projects port.ProjectStore, token model.Token) (bool, error) {
	for _, key := range token.ProjectKeys() {
		bound := token.ProjectIncarnations[key]
		if bound == "" {
			return false, nil
		}
		authority, err := projects.Authority(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("current token project %q: %w", key, err)
		}
		if string(authority.Incarnation) != bound {
			return false, nil
		}
	}
	return true, nil
}
