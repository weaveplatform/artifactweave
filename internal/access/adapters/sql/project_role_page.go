package sql

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (p projectStore) RoleProjects(ctx context.Context, q port.ProjectRoleQuery) ([]model.Project, bool, error) {
	if q.Limit < 1 {
		return nil, false, fmt.Errorf("project page limit must be positive: %w", kernel.ErrInvalid)
	}
	if err := p.guard(ctx); err != nil {
		return nil, false, err
	}
	query, args := projectRolePageQuery(q, p.s.Dialect().Dollar)
	var out []model.Project
	err := p.s.Rows(ctx, "page projects using role", query, args, func(r *sql.Rows) error {
		v, err := scanProject(r)
		out = append(out, v)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > q.Limit {
		return out[:q.Limit], true, nil
	}
	return out, false, nil
}

func projectRolePageQuery(q port.ProjectRoleQuery, postgres bool) (string, []any) {
	name := "p.display_name"
	// Preserve the JSON reader's empty/NULL array semantics for old rows.
	roles := "COALESCE(NULLIF(m.roles, ''), '[]')"
	array := "json_each(" + roles + ") r"
	value := "r.value"
	arrayType := " AND json_type(" + roles + ") = 'array'"
	if postgres {
		name += ` COLLATE "C"`
		array = "jsonb_array_elements_text(COALESCE(NULLIF(" + roles + "::jsonb, 'null'::jsonb), '[]'::jsonb)) r(value)"
		value += ` COLLATE "C"`
		arrayType = ""
	}
	where := "EXISTS (SELECT 1 FROM project_members m, " + array +
		" WHERE m.project_key = p.key AND m.principal_type IN ('user', 'group')" + arrayType + " AND " + value + " = ?)"
	args := []any{q.RoleName}
	order, op := " ASC", ">"
	if q.Descending {
		order, op = " DESC", "<"
	}
	if q.AfterKey != "" {
		where += " AND (" + name + ", p.key) " + op + " (?, ?)"
		args = append(args, q.AfterName, q.AfterKey)
	}
	limit := q.Limit
	if limit < math.MaxInt {
		limit++
	}
	columns := "p." + strings.ReplaceAll(projectCols, ", ", ", p.")
	return "SELECT " + columns + " FROM projects p WHERE " + where +
		" ORDER BY " + name + order + ", p.key" + order + " LIMIT ?", append(args, limit)
}
