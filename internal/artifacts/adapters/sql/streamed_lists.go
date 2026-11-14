package sql

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// The pages of the complete lists that are written as they are read
// (#1409): a folder's descendants in full path order, which the storage
// API's file list sorts by, a checksum's build dependencies in
// FindDependencies' order, and the trash can's tree in path order. Their
// queries seek the indexes of migration 0004_streamed_lists.

// fullPath is the third column of items_full_path, written as the index
// writes it: an item's full path, which for a root-level item (path ".")
// is its name. PostgreSQL needs the COLLATE written out as the index has
// it, so that the comparisons and the order are byte by byte.
func (i items) fullPath() string {
	const expr = "CASE WHEN path = '.' THEN name ELSE path || '/' || name END"
	if i.s.Dialect().Dollar {
		return "(" + expr + `) COLLATE "C"`
	}
	return expr
}

func (i items) DescendantsByFullPath(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	op := "list descendants of " + folder + " by full path"
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	query, args, err := i.fullPathQuery(repoKey, cached, folder, p)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	var out []model.Item
	err = i.s.Rows(ctx, op, query, args, func(rs *sql.Rows) error {
		it, err := scanItem(rs)
		out = append(out, it)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(out, p.Limit, func(it model.Item) []string {
		return []string{it.FullPath()}
	})
	return page, next, nil
}

// fullPathQuery is DescendantsByFullPath's query, which the plan tests
// explain. The descendants of a folder are exactly the full paths from
// "<folder>/" up to, not including, "<folder>0", since '0' follows '/'; a
// range the index seeks. The root's are every item but the root's record.
// p is normalized.
func (i items) fullPathQuery(
	repoKey string, cached bool, folder string, p kernel.Page,
) (string, []any, error) {
	after, err := p.Key(1)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // a kernel sentinel, wrapped by the caller
	}
	fp := i.fullPath()
	where := "repo_key = ? AND cached = ?"
	args := []any{repoKey, cached}
	low := ""
	if folder = folderPath(folder); folder == kernel.RootPath {
		where += " AND " + notRoot
	} else {
		low = folder + "/"
		where += " AND " + fp + " < ?"
		args = append(args, folder+"0")
	}
	if after != nil && after[0] > low {
		low = after[0]
	}
	if low != "" {
		where += " AND " + fp + " > ?"
		args = append(args, low)
	}
	return "SELECT " + itemCols + " FROM items WHERE " + where + " ORDER BY " + fp + " LIMIT ?",
		append(args, p.Limit+1), nil
}

func (b buildStore) DependencyPage(
	ctx context.Context,
	c kernel.Checksums,
	p kernel.Page,
) ([]model.BuildFileRef, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("page dependencies: %w", err)
	}
	query, args, err := dependencyPageQuery(c, p)
	if err != nil {
		return nil, "", fmt.Errorf("page dependencies: %w", err)
	}
	type row struct {
		ref           model.BuildFileRef
		module, place int
	}
	var out []row
	err = b.s.Rows(ctx, "page build_dependencies", query, args, func(r *sql.Rows) error {
		var (
			x       row
			started int64
			created sql.NullInt64
			scopes  sql.NullString
		)
		if err := r.Scan(
			&x.ref.Run.ID, &x.ref.Run.ProjectKey, &x.ref.Run.Name, &x.ref.Run.Number, &started,
			&created, &x.ref.Module, &x.module, &x.place, &x.ref.File.ID, &scopes, &x.ref.File.Name,
			&x.ref.File.Type, &x.ref.File.Path, &x.ref.File.Checksums.SHA1,
			&x.ref.File.Checksums.SHA256, &x.ref.File.Checksums.MD5,
		); err != nil {
			return err //nolint:wrapcheck // wrapped by Rows
		}
		x.ref.Run.Started, x.ref.Run.Created = time.UnixMicro(started).UTC(),
			dialect.FromMicros(created)
		if err := dialect.FromJSON(scopes, &x.ref.File.Scopes); err != nil {
			return err
		}
		out = append(out, x)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	slices.SortFunc(out, func(x, y row) int {
		return cmp.Or(cmp.Compare(x.ref.Run.ID, y.ref.Run.ID), cmp.Compare(x.module, y.module),
			cmp.Compare(x.place, y.place))
	})
	rows, next := kernel.Trim(out, p.Limit, func(x row) []string {
		return []string{
			dialect.Itoa(x.ref.Run.ID), strconv.Itoa(x.module), strconv.Itoa(x.place),
		}
	})
	refs := make([]model.BuildFileRef, 0, len(rows))
	for _, x := range rows {
		refs = append(refs, x.ref)
	}
	return refs, next, nil
}

// dependencyPageQuery is DependencyPage's query, which the plan tests
// explain: find's rows, a page at a time. The cursor is the last row's
// (run, module, file) position, compared as a row value so the engine
// seeks build_dependencies_sha1_order past it. The page is cut from
// build_dependencies alone before the joins: ordering the joined rows lets
// the planners drive the join from builds or build_modules and sort every
// later dependency for each page, so the caller orders the page instead.
// The inner joins keep every row: a dependency's module and run are
// foreign keys that cascade on delete. p is normalized.
func dependencyPageQuery(c kernel.Checksums, p kernel.Page) (string, []any, error) {
	if c.SHA1 == "" && c.SHA256 == "" && c.MD5 == "" {
		return "", nil, fmt.Errorf("build file lookup needs a checksum: %w", kernel.ErrInvalid)
	}
	after, err := p.Key(3)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // a kernel sentinel, wrapped by the caller
	}
	where := "1 = 1"
	var args []any
	for _, m := range []struct{ col, v string }{
		{"sha1", c.SHA1}, {"sha256", c.SHA256}, {"md5", c.MD5},
	} {
		if m.v != "" {
			where += " AND " + m.col + " = ?"
			args = append(args, m.v)
		}
	}
	if after != nil {
		pos := make([]any, 0, len(after))
		for _, part := range after {
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return "", nil, fmt.Errorf(
					"%w: %q is not an integer",
					kernel.ErrInvalidCursor,
					part,
				)
			}
			pos = append(pos, n)
		}
		where += " AND (build_id, module_position, position) > (?, ?, ?)"
		args = append(args, pos...)
	}
	return "SELECT b.id, b.project_key, b.name, b.number, b.started_at, b.created_at," +
		" m.module_id, f.module_position, f.position, f.dependency_id, f.scopes, f.name, f.type," +
		" f.path, f.sha1, f.sha256, f.md5" +
		" FROM (SELECT build_id, module_position, position, dependency_id, scopes, name, type," +
		" path, sha1, sha256, md5 FROM build_dependencies WHERE " + where +
		" ORDER BY build_id, module_position, position LIMIT ?) f" +
		" JOIN build_modules m ON m.build_id = f.build_id AND m.position = f.module_position" +
		" JOIN builds b ON b.id = f.build_id", append(args, p.Limit+1), nil
}

// trashPath is the first column of trash_path, written as the index writes
// it: model.TrashEntry.TrashPath, the original repository key, a slash and
// the original full path, which for a root-level item (path ".") is its
// name. PostgreSQL needs the COLLATE written out as the index has it.
func (t trash) trashPath() string {
	const expr = "original_repo_key || '/' || CASE WHEN original_path = '.' THEN original_name" +
		" ELSE original_path || '/' || original_name END"
	if t.s.Dialect().Dollar {
		return "(" + expr + `) COLLATE "C"`
	}
	return expr
}

func (t trash) BelowByPath(
	ctx context.Context,
	dir string,
	p kernel.Page,
) ([]model.TrashEntry, string, error) {
	op := "list trash below " + dir + " by path"
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	query, args, err := t.belowByPathQuery(dir, p)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	out, err := t.query(ctx, op, query, args...)
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(out, p.Limit, func(e model.TrashEntry) []string {
		return []string{e.TrashPath(), dialect.Itoa(e.ID)}
	})
	return page, next, nil
}

// belowByPathQuery is BelowByPath's query, which the plan tests explain.
// The entries below dir are exactly the paths from "<dir>/" up to, not
// including, "<dir>0", since '0' follows '/'; a range trash_path seeks.
// The cursor is the last entry's (path, ID): the seek starts at its path,
// and the few entries at that path up to its ID are passed over. The two
// lower bounds are folded into one, since SQLite seeks by only one of them
// and would read every entry before the cursor for each page. p is
// normalized.
func (t trash) belowByPathQuery(dir string, p kernel.Page) (string, []any, error) {
	after, err := p.Key(2)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // a kernel sentinel, wrapped by the caller
	}
	tp := t.trashPath()
	where := "1 = 1"
	var args []any
	low := ""
	if dir = strings.Trim(dir, "/"); dir != "" {
		low = dir + "/"
		where += " AND " + tp + " < ?"
		args = append(args, dir+"0")
	}
	if after != nil && after[0] > low {
		low = after[0]
	}
	if low != "" {
		where += " AND " + tp + " >= ?"
		args = append(args, low)
	}
	if after != nil {
		id, err := strconv.ParseInt(after[1], 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %q is not an ID", kernel.ErrInvalidCursor, after[1])
		}
		where += " AND (" + tp + " > ? OR id > ?)"
		args = append(args, after[0], id)
	}
	return "SELECT " + trashCols + " FROM trash WHERE " + where +
		" ORDER BY " + tp + ", id LIMIT ?", append(args, p.Limit+1), nil
}
