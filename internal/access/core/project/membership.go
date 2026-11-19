package project

import (
	"context"
	"errors"
	"fmt"
	"slices"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func validPrincipalType(typ model.PrincipalType) bool {
	return typ == model.PrincipalUser || typ == model.PrincipalGroup
}

// Members returns direct memberships, not roles inherited through a group.
// The caller's HTTP privilege or project management scope is checked outside
// this domain service. A storage failure never produces a partial member list.
func (s Service) Members(ctx context.Context, key string, typ model.PrincipalType) ([]model.ProjectMember, error) {
	if !validPrincipalType(typ) {
		return nil, fmt.Errorf("unknown principal type %q: %w", typ, ErrInvalid)
	}
	var out []model.ProjectMember
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		var err error
		out, err = s.Store.Projects().Members(ctx, key, typ)
		if err != nil {
			return fmt.Errorf("project: read %s memberships in %q: %w", typ, key, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Member returns one direct membership. A user gets their group's roles through
// authorization, not by copying those roles into the user's membership row.
func (s Service) Member(ctx context.Context, key string, typ model.PrincipalType, name string) (model.ProjectMember, error) {
	var out model.ProjectMember
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		canonical, err := principalKey(typ, name)
		if err != nil {
			return err
		}
		out, err = s.Store.Projects().Member(ctx, key, typ, canonical)
		if errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("project %q %s %q: %w", key, typ, canonical, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("project: read %s %q in %q: %w", typ, canonical, key, err)
		}
		return nil
	})
	return out, err
}

// PutMember replaces a user's or group's direct role assignments. Identity and
// role definitions must already exist; every assigned role is checked before
// writing. Empty memberships are rejected by the agreed authored API contract;
// the precise vendor REST outcome remains qualified by the contract audit.
func (s Service) PutMember(ctx context.Context, key string, typ model.PrincipalType, name string, names []string) (model.ProjectMember, error) {
	var out model.ProjectMember
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		canonical, err := s.identity(ctx, typ, name)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("a project member needs at least one role: %w", ErrInvalid)
		}
		assigned := make([]string, 0, len(names))
		for _, roleName := range names {
			if _, err := s.role(ctx, key, roleName); errors.Is(err, ErrNotFound) {
				return fmt.Errorf("role %q does not exist in project %q: %w", roleName, key, ErrInvalid)
			} else if err != nil {
				return err
			}
			if !slices.Contains(assigned, roleName) {
				assigned = append(assigned, roleName)
			}
		}
		out = model.ProjectMember{ProjectKey: key, Type: typ, Name: canonical, Roles: assigned}
		if err := s.Store.Projects().PutMember(ctx, out); err != nil {
			return fmt.Errorf("project: write %s %q in %q: %w", typ, canonical, key, err)
		}
		return nil
	})
	if err != nil {
		return model.ProjectMember{}, err
	}
	return out, nil
}

// DeleteMember removes only the named direct assignment. Removing a user's
// assignment does not alter memberships or inherited roles of their groups.
func (s Service) DeleteMember(ctx context.Context, key string, typ model.PrincipalType, name string) error {
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		// Removal must still work if the platform identity was deleted after
		// assignment. Existing identity is a prerequisite for adding a member,
		// not for cleaning up an old direct assignment.
		canonical, err := principalKey(typ, name)
		if err != nil {
			return err
		}
		err = s.Store.Projects().DeleteMember(ctx, key, typ, canonical)
		if errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("project %q %s %q: %w", key, typ, canonical, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("project: delete %s %q in %q: %w", typ, canonical, key, err)
		}
		return nil
	})
}

func principalKey(typ model.PrincipalType, name string) (string, error) {
	switch typ {
	case model.PrincipalUser:
		return accessapi.FoldUsername(name), nil
	case model.PrincipalGroup:
		return name, nil
	default:
		return "", fmt.Errorf("unknown principal type %q: %w", typ, ErrInvalid)
	}
}

func (s Service) identity(ctx context.Context, typ model.PrincipalType, name string) (string, error) {
	lookup, err := principalKey(typ, name)
	if err != nil {
		return "", err
	}
	var canonical string
	switch typ {
	case model.PrincipalUser:
		var u model.User
		u, err = s.Store.Users().Get(ctx, lookup)
		canonical = u.Name
	case model.PrincipalGroup:
		var g model.Group
		g, err = s.Store.Groups().Get(ctx, lookup)
		canonical = g.Name
	default:
		return "", fmt.Errorf("unknown principal type %q: %w", typ, ErrInvalid)
	}
	if errors.Is(err, kernel.ErrNotFound) {
		return "", fmt.Errorf("%s %q: %w", typ, name, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("project: read %s %q: %w", typ, name, err)
	}
	return canonical, nil
}
