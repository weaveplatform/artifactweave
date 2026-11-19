package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RemovePrincipalMemberships removes direct assignments of this principal from
// every project. Identity deletion calls it in the same transaction, before
// locking identity rows: project snapshots and identity cleanup must take the
// project guard first. A later same-name identity must not acquire these roles.
// The other principal type's same-name assignments are independent.
func (s Service) RemovePrincipalMemberships(ctx context.Context, typ model.PrincipalType, name string) error {
	canonical, err := principalKey(typ, name)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(ctx context.Context) error {
		// List takes the permanent guard even when no projects exist. That
		// also serializes assignment creation with the enclosing deletion.
		projects, err := s.Store.Projects().List(ctx)
		if err != nil {
			return fmt.Errorf("project: list projects for identity cleanup: %w", err)
		}
		for _, p := range projects {
			// A failed nested mutation dooms the enclosing SQL transaction,
			// even if its not-found error is ignored. Read first under this
			// same guard; project writers cannot remove the row between calls.
			_, err := s.Store.Projects().Member(ctx, p.Key, typ, canonical)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("project: read %s %q assignment in %q: %w", typ, canonical, p.Key, err)
			}
			if err := s.Store.Projects().DeleteMember(ctx, p.Key, typ, canonical); err != nil {
				return fmt.Errorf("project: remove %s %q from %q: %w", typ, canonical, p.Key, err)
			}
		}
		return nil
	})
}
