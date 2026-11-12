package sql

import (
	"context"
	"database/sql"
	"fmt"
)

// versionNamesQuery reads only columns of package_versions_order
// (package_id, order_key, version), and orders by that index's own
// columns after the equality on package_id, so both engines answer it from
// the index in its order, with no sort and no read of the rows' metadata.
// It is Versions' ORDER BY, collation included, so the names come in the
// order Versions returns the versions.
const versionNamesQuery = "SELECT version FROM package_versions WHERE package_id = ? " +
	"ORDER BY order_key, version"

func (p packages) VersionNames(ctx context.Context, packageID int64) ([]string, error) {
	var out []string
	err := p.s.Rows(
		ctx,
		fmt.Sprintf("list package %d version names", packageID),
		versionNamesQuery,
		[]any{packageID},
		func(rs *sql.Rows) error {
			var v string
			err := rs.Scan(&v)
			out = append(out, v)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}
