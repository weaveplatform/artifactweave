package sql

import "github.com/weaveplatform/artifactweave/internal/access/core/port"

// ProjectRolePageQuery exposes the exact production query to the plan test.
func ProjectRolePageQuery(q port.ProjectRoleQuery, postgres bool) (string, []any) {
	return projectRolePageQuery(q, postgres)
}
