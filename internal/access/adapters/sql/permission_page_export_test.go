package sql

import "github.com/weaveplatform/artifactweave/internal/access/core/port"

// PermissionPageQuery is the statement Permissions().Page sends for q, so
// the plan test explains exactly that.
func PermissionPageQuery(q port.PermissionQuery) (string, []any) { return permissionPageQuery(q) }
