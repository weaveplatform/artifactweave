package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// indexRepositoryWhere is applied before LIMIT and before a version cutoff.
// The contexts are descriptive snapshots, so every read still binds their
// current repository/project/class/type identity in SQL.
func indexRepositoryWhere(ref api.IndexRepository) (string, []any) {
	return "r.key = ? AND r.project_key = ? AND r.rclass = ? AND r.package_type = ?", []any{ref.Key, ref.Project, string(ref.Class), string(ref.PackageType)}
}

func indexPackageWhere(ref api.IndexPackage) (string, []any) {
	where, args := indexRepositoryWhere(ref.Repository)
	return where + " AND p.id = ? AND p.name = ? AND p.package_type = r.package_type", append(args, ref.ID, ref.Name)
}

func (p packages) IndexPackages(ctx context.Context, ref api.IndexRepository, page kernel.Page) ([]api.IndexPackage, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index package page: %w", err)
	}
	cursor, cursorArgs, err := dialect.Keyset(page, []string{"p.name"}, nil, false)
	if err != nil {
		return nil, "", fmt.Errorf("index package cursor: %w", err)
	}
	where, args := indexRepositoryWhere(ref)
	var out []api.IndexPackage
	err = p.s.Rows(ctx, "index packages",
		"SELECT p.id, p.name FROM packages p JOIN repositories r ON r.key = p.repo_key WHERE "+where+
			" AND p.package_type = r.package_type AND "+cursor+" ORDER BY p.name LIMIT ?",
		append(append(args, cursorArgs...), page.Limit+1), func(row *sql.Rows) error {
			entry := api.IndexPackage{Repository: kernel.Clone(ref)}
			if err := row.Scan(&entry.ID, &entry.Name); err != nil {
				return err //nolint:wrapcheck // Rows supplies context
			}
			out = append(out, entry)
			return nil
		})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexPackage) []string { return []string{entry.Name} })
	return out, next, nil
}

func (p packages) IndexVersions(ctx context.Context, ref api.IndexPackage, keep int, page kernel.Page) ([]api.IndexVersion, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index version page: %w", err)
	}
	if keep < 0 {
		return nil, "", fmt.Errorf("negative version retention: %w", kernel.ErrInvalid)
	}
	cursor, cursorArgs, err := dialect.Keyset(page, []string{"v.version"}, nil, false)
	if err != nil {
		return nil, "", fmt.Errorf("index version cursor: %w", err)
	}
	where, args := indexPackageWhere(ref)
	// Traversal uses immutable version strings. Protection uses the existing
	// comparator index, including its version-string tie breaker, in this same
	// statement snapshot; inserting/deleting a version cannot shift a cursor.
	query := "WITH target AS (SELECT p.id FROM packages p JOIN repositories r ON r.key = p.repo_key WHERE " + where + "), " +
		"cutoff AS (SELECT order_key, version FROM package_versions WHERE package_id = (SELECT id FROM target) ORDER BY order_key DESC, version DESC LIMIT 1 OFFSET ?) " +
		"SELECT v.id, v.version, v.order_key, v.created_at, v.last_downloaded_at, " +
		"CASE WHEN cutoff.order_key IS NULL OR v.order_key > cutoff.order_key OR (v.order_key = cutoff.order_key AND v.version >= cutoff.version) THEN TRUE ELSE FALSE END " +
		"FROM package_versions v JOIN target ON target.id = v.package_id LEFT JOIN cutoff ON TRUE WHERE " + cursor + " ORDER BY v.version LIMIT ?"
	args = append(args, max(1, keep)-1)
	args = append(args, cursorArgs...)
	args = append(args, page.Limit+1)
	var out []api.IndexVersion
	err = p.s.Rows(ctx, "index versions", query, args, func(row *sql.Rows) error {
		entry := api.IndexVersion{Package: kernel.Clone(ref)}
		var created, downloaded sql.NullInt64
		if err := row.Scan(&entry.ID, &entry.Version, &entry.OrderKey, &created, &downloaded, &entry.Protected); err != nil {
			return err //nolint:wrapcheck // Rows supplies context
		}
		entry.Created, entry.LastDownloaded = dialect.FromMicros(created), dialect.FromMicros(downloaded)
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexVersion) []string { return []string{entry.Version} })
	return out, next, nil
}

// The file-page implementation is in package_selection_files.go. Its compile
// assertion holds all bounded reads to the same adapter capability.
var _ port.PackageIndexPages = packages{}
