package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

func (p packages) IndexFiles(ctx context.Context, ref api.IndexVersion, properties map[string][]string, page kernel.Page) ([]api.IndexFile, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index file page: %w", err)
	}
	cursor, cursorArgs, err := dialect.Keyset(page, []string{"f.item_id"}, map[string]bool{"f.item_id": true}, false)
	if err != nil {
		return nil, "", fmt.Errorf("index file cursor: %w", err)
	}
	where, args := indexPackageWhere(ref.Package)
	projection, propertyArgs := p.indexPropertiesSQL(properties)
	// Content, properties and access time share one statement snapshot. Reading
	// an entire Files() list or loading properties after releasing this snapshot
	// would lose both the page bound and coherent overwrite/download evidence.
	query := "WITH target AS (SELECT v.id FROM package_versions v JOIN packages p ON p.id = v.package_id JOIN repositories r ON r.key = p.repo_key WHERE " + where + " AND v.id = ? AND v.version = ?), " +
		"selected AS (SELECT f.item_id FROM package_version_files f JOIN target ON target.id = f.version_id JOIN items i ON i.id = f.item_id " +
		"WHERE i.repo_key = ? AND i.cached = ? AND i.type = 'file' AND " + cursor + " ORDER BY f.item_id LIMIT ?) " +
		"SELECT i.id, i.path, i.name, i.cached, i.size, i.sha256, i.sha512, i.sha1, i.md5, " +
		"i.created_at, i.modified_at, i.updated_at, i.props_modified_at, s.last_downloaded_at, " + projection +
		" FROM selected f JOIN items i ON i.id = f.item_id LEFT JOIN item_stats s ON s.item_id = i.id ORDER BY i.id"
	args = append(args, ref.ID, ref.Version, ref.Package.Repository.Key, ref.Package.Repository.Class == repo.Remote)
	args = append(args, cursorArgs...)
	args = append(args, page.Limit+1)
	args = append(args, propertyArgs...)
	var out []api.IndexFile
	err = p.s.Rows(ctx, "index files", query, args, func(row *sql.Rows) error {
		entry := api.IndexFile{Version: kernel.Clone(ref)}
		var path, name, encoded string
		var created, modified, updated, propertiesModified, downloaded sql.NullInt64
		if err := row.Scan(&entry.ID, &path, &name, &entry.Cached, &entry.Size,
			&entry.Actual.SHA256, &entry.Actual.SHA512, &entry.Actual.SHA1, &entry.Actual.MD5,
			&created, &modified, &updated, &propertiesModified, &downloaded, &encoded); err != nil {
			return err //nolint:wrapcheck // Rows supplies context
		}
		entry.Path = name
		if path != "" && path != kernel.RootPath {
			entry.Path = path + "/" + name
		}
		entry.Created, entry.Modified, entry.Updated = dialect.FromMicros(created), dialect.FromMicros(modified), dialect.FromMicros(updated)
		entry.PropertiesModified, entry.LastDownloaded = dialect.FromMicros(propertiesModified), dialect.FromMicros(downloaded)
		var pairs [][2]string
		if err := json.Unmarshal([]byte(encoded), &pairs); err != nil {
			return fmt.Errorf("decode selected index properties: %w", err)
		}
		if len(pairs) > 0 {
			entry.Properties = make(map[string][]string)
			for _, pair := range pairs {
				entry.Properties[pair[0]] = append(entry.Properties[pair[0]], pair[1])
			}
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexFile) []string { return []string{strconv.FormatInt(entry.ID, 10)} })
	return out, next, nil
}

func (p packages) indexPropertiesSQL(wanted map[string][]string) (string, []any) {
	keys := make([]string, 0, len(wanted))
	for key, values := range wanted {
		if len(values) > 0 {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return "'[]'", nil
	}
	clauses := make([]string, 0, len(keys))
	var args []any
	for _, key := range keys {
		values := slices.Clone(wanted[key])
		slices.Sort(values)
		values = slices.Compact(values)
		clauses = append(clauses, "(property.key = ? AND property.value IN ("+dialect.Placeholders(len(values))+"))")
		args = append(args, key)
		for _, value := range values {
			args = append(args, value)
		}
	}
	where := "property.item_id = i.id AND (" + strings.Join(clauses, " OR ") + ")"
	if p.s.Dialect().Name == "postgres" {
		return "COALESCE((SELECT jsonb_agg(jsonb_build_array(property.key, property.value) ORDER BY property.key, property.value) FROM item_properties property WHERE " + where + "), '[]'::jsonb)::text", args
	}
	return "COALESCE((SELECT json_group_array(json_array(key, value)) FROM (SELECT property.key, property.value FROM item_properties property WHERE " + where + " ORDER BY property.key, property.value)), '[]')", args
}
