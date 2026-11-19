package project

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ProjectsForRole returns a name-ordered page of projects with a direct user or
// group assignment of the named global role. The caller must be a platform
// admin. Cursors bind role, direction and the last display-name/key pair.
func (s Service) ProjectsForRole(ctx context.Context, name string, descending bool, page kernel.Page) ([]model.Project, string, error) {
	if page.Limit == 0 {
		page.Limit = 1000
	}
	if page.Limit < 1 {
		return nil, "", fmt.Errorf("project page limit must be positive: %w", ErrInvalid)
	}
	after, err := page.Key(4)
	if err != nil || (after != nil && (after[0] != projectCursorPart(name) || after[1] != strconv.FormatBool(descending))) {
		return nil, "", fmt.Errorf("project role cursor is not valid for this query: %w", ErrInvalid)
	}
	q := port.ProjectRoleQuery{RoleName: name, Descending: descending, Limit: page.Limit}
	if after != nil {
		lastName, nameErr := base64.RawURLEncoding.DecodeString(after[2])
		lastKey, keyErr := base64.RawURLEncoding.DecodeString(after[3])
		if nameErr != nil || keyErr != nil || len(lastKey) == 0 ||
			!utf8.Valid(lastName) || !utf8.Valid(lastKey) ||
			strings.IndexByte(string(lastName), 0) >= 0 || strings.IndexByte(string(lastKey), 0) >= 0 {
			return nil, "", fmt.Errorf("project role cursor has an invalid position: %w", ErrInvalid)
		}
		q.AfterName, q.AfterKey = string(lastName), string(lastKey)
	}
	var out []model.Project
	var next string
	err = s.tx(ctx, func(ctx context.Context) error {
		// Global role lookup takes the project guard even for built-ins, before
		// the usage query or any per-project ambiguity checks.
		if _, err := (roles.Service{Store: s.Store}).Role(ctx, name); errors.Is(err, roles.ErrNotFound) {
			return fmt.Errorf("global role %q: %w", name, ErrNotFound)
		} else if err != nil {
			return fmt.Errorf("project: read global role: %w", err)
		}
		rows, more, err := s.Store.Projects().RoleProjects(ctx, q)
		if err != nil {
			return fmt.Errorf("project: page global role usage: %w", err)
		}
		for _, p := range rows {
			if _, err := s.role(ctx, p.Key, name); err != nil {
				return err
			}
		}
		out = rows
		if more {
			last := rows[len(rows)-1]
			next = kernel.Cursor(projectCursorPart(name), strconv.FormatBool(descending),
				projectCursorPart(last.DisplayName), projectCursorPart(last.Key))
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// Inner encoding keeps the kernel's compound-cursor separator out of arbitrary
// stored role/display names and imported keys.
func projectCursorPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
