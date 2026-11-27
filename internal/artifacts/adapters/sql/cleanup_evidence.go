package sql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

var (
	_ port.CleanupMemberReader     = repos{}
	_ port.PackageDownloadEvidence = packages{}
)

func (r repos) CleanupMembers(ctx context.Context, key string) ([]repo.Member, error) {
	expression := "COALESCE(json_extract(doc, '$.members'), '[]')"
	if r.s.Dialect().Name == "postgres" {
		expression = "COALESCE(NULLIF(doc::jsonb -> 'members', 'null'::jsonb), '[]'::jsonb)::text"
	}
	var encoded string
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind(
		"SELECT CASE WHEN LENGTH("+expression+") <= ? THEN "+expression+" ELSE '' END FROM repositories WHERE key = ?"),
		repo.MaxMembers*8192, key).Scan(&encoded)
	if err != nil {
		return nil, r.s.Wrap("read cleanup repository members", err)
	}
	var out []repo.Member
	if encoded == "" || json.Unmarshal([]byte(encoded), &out) != nil || len(out) > repo.MaxMembers {
		return nil, fmt.Errorf("cleanup repository membership exceeds its metadata bound: %w", kernel.ErrStore)
	}
	return out, nil
}

func (p packages) VersionDownloads(ctx context.Context, key string, versions []api.DownloadVersion) ([]api.VersionDownload, error) {
	if len(versions) == 0 || len(versions) > api.MaxDownloadVersions {
		return nil, fmt.Errorf("version download query exceeds its bound: %w", kernel.ErrInvalid)
	}
	conditions := make([]string, 0, len(versions))
	args := []any{key}
	for _, version := range versions {
		conditions = append(conditions, "(p.name = ? AND v.version = ?)")
		args = append(args, version.Package, version.Version)
	}
	// Only index-linked hosted files participate. Cached/foreign links cannot
	// contribute another repository's statistics. All fields share one SQL
	// statement snapshot, including item and version download generations.
	query := "SELECT p.name, v.version, v.id, v.created_at, v.last_downloaded_at, v.download_revision, v.admitted_at, v.admission_revision, c.admitted_at, COALESCE(c.admission_revision, 'initial'), " +
		"MAX(s.last_downloaded_at), MAX(s.remote_last_downloaded_at) FROM package_versions v " +
		"JOIN packages p ON p.id = v.package_id JOIN repositories r ON r.key = p.repo_key " +
		"JOIN package_version_files f ON f.version_id = v.id " +
		"JOIN items i ON i.id = f.item_id AND i.repo_key = r.key AND i.cached = FALSE AND i.type = 'file' " +
		"LEFT JOIN item_stats s ON s.item_id = i.id " +
		"LEFT JOIN artifact_cleanup_state c ON c.repo_key = r.key " +
		"WHERE r.key = ? AND p.package_type = r.package_type AND (" + strings.Join(
		conditions,
		" OR ",
	) + ") " +
		"GROUP BY p.name, v.version, v.id, v.created_at, v.last_downloaded_at, v.download_revision, v.admitted_at, v.admission_revision, c.admitted_at, c.admission_revision ORDER BY p.name, v.version"
	var out []api.VersionDownload
	err := p.s.Rows(ctx, "read version download evidence", query, args, func(row *sql.Rows) error {
		var value api.VersionDownload
		var created, versionDownload, itemDownload, remoteDownload, admitted, repositoryAdmitted sql.NullInt64
		var admissionRevision, repositoryAdmissionRevision string
		if err := row.Scan(
			&value.Package,
			&value.Version,
			&value.ID,
			&created,
			&versionDownload,
			&value.Revision,
			&admitted,
			&admissionRevision,
			&repositoryAdmitted,
			&repositoryAdmissionRevision,
			&itemDownload,
			&remoteDownload,
		); err != nil {
			return err //nolint:wrapcheck // Rows supplies the evidence-query context
		}
		value.Created = dialect.FromMicros(created)
		value.AdmittedAt = dialect.FromMicros(admitted)
		if at := dialect.FromMicros(repositoryAdmitted); at.After(value.AdmittedAt) {
			value.AdmittedAt = at
		}
		value.AdmissionRevision = admissionRevision + "/" + repositoryAdmissionRevision
		for _, at := range []sql.NullInt64{versionDownload, itemDownload, remoteDownload} {
			if candidate := dialect.FromMicros(at); candidate.After(value.LastDownloaded) {
				value.LastDownloaded = candidate
			}
		}
		out = append(out, value)
		return nil
	})
	return out, err
}

func (s *Store) itemVersionsDownloaded(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, "invalidate linked version download observations",
		"UPDATE package_versions SET mutation_revision = ?, download_revision = ? WHERE id IN "+
			"(SELECT version_id FROM package_version_files WHERE item_id = ?)", rand.Text(), rand.Text(), id)
	return err
}
