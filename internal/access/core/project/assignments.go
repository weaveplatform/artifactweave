package project

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// AssignedRoles resolves current direct-user and group assignments in key.
// The caller supplies groups selected by current authentication and token scope;
// this method does not authorize a caller-supplied list of group names. Missing
// identities, memberships and deleted roles grant nothing. Storage failures and
// ambiguous names abort the entire result, never return partial permissions.
func (s Service) AssignedRoles(ctx context.Context, key, user string, groups []string) ([]model.Role, error) {
	var out []model.Role
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		var names []string
		principals := make([]model.ProjectMember, 0, 1+len(groups))
		if user != "" {
			principals = append(principals, model.ProjectMember{Type: model.PrincipalUser, Name: user})
		}
		for _, group := range groups {
			principals = append(principals, model.ProjectMember{Type: model.PrincipalGroup, Name: group})
		}
		for _, p := range principals {
			canonical, err := s.identity(ctx, p.Type, p.Name)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			member, err := s.Store.Projects().Member(ctx, key, p.Type, canonical)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("project: read role assignment of %s %q in %q: %w", p.Type, canonical, key, err)
			}
			for _, name := range member.Roles {
				if !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
		}
		var err error
		out, err = s.namedRoles(ctx, key, names)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// NamedRoles resolves definitions for an already authorized role selection,
// such as a validated project-role token. It deliberately does not require a
// stored-user membership: documented username-less OIDC mappings use named
// project roles. Token mint authority and mixed-scope restrictions belong to
// the authorizer, not to this definition lookup. Definitions are read at use
// time, so role changes and deletion take effect without reissuing the token.
func (s Service) NamedRoles(ctx context.Context, key string, names []string) ([]model.Role, error) {
	var out []model.Role
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		var err error
		out, err = s.namedRoles(ctx, key, names)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) namedRoles(ctx context.Context, key string, names []string) ([]model.Role, error) {
	out := make([]model.Role, 0, len(names))
	for _, name := range names {
		if slices.ContainsFunc(out, func(r model.Role) bool { return r.Name == name }) {
			continue
		}
		r, err := s.role(ctx, key, name)
		if errors.Is(err, ErrNotFound) {
			continue // deleting a definition revokes just that role's grants
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
