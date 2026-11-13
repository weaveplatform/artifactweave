package sql

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// DockerPages returns the image layout's page reader. Its queries read the
// partial indexes items_docker_tags and items_docker_images of migration
// 0002_docker_page.
func (i items) DockerPages() port.DockerPageStore { return dockerPages(i) }

type dockerPages struct{ s *Store }

// storedManifests is the predicate of both partial indexes, written as they
// write it: a planner uses a partial index only for a query whose conditions
// contain the index's own.
const storedManifests = "type = 'file' AND name IN ('manifest.json', 'list.manifest.json')"

// foldedPath is the third column of items_docker_tags, written as the index
// writes it. PostgreSQL needs the COLLATE written out as the index has it;
// lower() of a COLLATE "C" column (items.path is one) folds A to Z only, as
// SQLite's lower() does, which is port.DockerFold.
func (p dockerPages) foldedPath() string {
	if p.s.Dialect().Dollar {
		return `lower(path) COLLATE "C"`
	}
	return "lower(path)"
}

// rawPath is the fourth column of items_docker_tags and items_docker_images.
func (p dockerPages) rawPath() string {
	if p.s.Dialect().Dollar {
		return `path COLLATE "C"`
	}
	return "path"
}

// imageOf is the third column of items_docker_images: the path without its
// last segment. It must match the index character for character. Every
// function in it is IMMUTABLE on PostgreSQL and deterministic on SQLite.
func (p dockerPages) imageOf() string {
	if p.s.Dialect().Dollar {
		return `left(path, length(path) - strpos(reverse(path), '/')) COLLATE "C"`
	}
	return "substr(path, 1, length(rtrim(path, replace(path, '/', ''))) - 1)"
}

// limitClause limits a query to limit rows, or to none when it is not
// positive.
func limitClause(limit int) (string, []any) {
	if limit <= 0 {
		return "", nil
	}
	return " LIMIT ?", []any{limit}
}

func (p dockerPages) ManifestFolders(
	ctx context.Context, q port.DockerFolderQuery,
) ([]string, error) {
	query, args := p.folderQuery(q)
	prefix := q.Image + "/"
	var out []string
	err := p.s.Rows(ctx, "list Docker manifest folders of "+q.Image, query, args,
		func(r *sql.Rows) error {
			var fold, path string
			if err := r.Scan(&fold, &path); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out = append(out, path[len(prefix):])
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// folderQuery is ManifestFolders' query, which the plan tests explain.
func (p dockerPages) folderQuery(q port.DockerFolderQuery) (string, []any) {
	fold, raw := p.foldedPath(), p.rawPath()
	prefix := q.Image + "/"
	folded := port.DockerFold(q.Image)
	// The folded paths below the image run from <image>/ up to, not
	// including, <image>0: '0' follows '/'. A path of another image that
	// folds alike, App/v1 for app, lies in the range too, so the prefix is
	// checked as written; substr counts characters on both engines.
	low, cursor := folded+"/", ""
	args := []any{q.RepoKey, false}
	if q.AfterFold != "" || q.After != "" {
		low += q.AfterFold
		cursor = " AND (" + fold + " > ? OR " + raw + " > ?)"
	}
	args = append(args, low, folded+"0")
	if cursor != "" {
		args = append(args, folded+"/"+q.AfterFold, prefix+q.After)
	}
	args = append(args, utf8.RuneCountInString(prefix), prefix, strings.Count(q.Image, "/")+3)
	limit, largs := limitClause(q.Limit)
	return "SELECT DISTINCT " + fold + ", " + raw +
		" FROM items WHERE repo_key = ? AND cached = ? AND " + storedManifests +
		" AND " + fold + " >= ? AND " + fold + " < ?" + cursor +
		" AND substr(path, 1, ?) = ? AND depth = ?" +
		" ORDER BY " + fold + ", " + raw + limit, append(args, largs...)
}

func (p dockerPages) ImageManifests(
	ctx context.Context, q port.DockerImageQuery,
) ([]port.DockerImageManifest, error) {
	query, args := p.imageQuery(q)
	var out []port.DockerImageManifest
	err := p.s.Rows(ctx, "list Docker image manifests of "+q.RepoKey, query, args,
		func(r *sql.Rows) error {
			var m port.DockerImageManifest
			var path string
			if err := r.Scan(&m.Image, &path); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			m.Folder = path[len(m.Image)+1:]
			out = append(out, m)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// imageQuery is ImageManifests' query, which the plan tests explain.
func (p dockerPages) imageQuery(q port.DockerImageQuery) (string, []any) {
	image, raw := p.imageOf(), p.rawPath()
	args := []any{q.RepoKey, false}
	// The range on the image is what seeks; the tie-break inside After only
	// filters the rows the seek reaches.
	cursor := " AND " + image + " > ?"
	args = append(args, q.After)
	if q.Within {
		cursor = " AND " + image + " >= ? AND (" + image + " > ? OR " + raw + " > ?)"
		args = append(args, q.After, q.After+"/"+q.AfterFolder)
	}
	limit, largs := limitClause(q.Limit)
	// depth 3 or more leaves out a manifest in a top-level folder, which has
	// no image above it.
	return "SELECT DISTINCT " + image + ", " + raw +
		" FROM items WHERE repo_key = ? AND cached = ? AND " + storedManifests + cursor +
		" AND depth >= 3 ORDER BY " + image + ", " + raw + limit, append(args, largs...)
}
