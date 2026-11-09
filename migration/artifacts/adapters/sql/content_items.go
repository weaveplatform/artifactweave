package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Items returns the item store: items in the canonical form of decision
// 0004, with their properties and download statistics, and the AQL item
// search (decision 0012).
func (s *Store) Items() port.ItemStore { return items{s} }

type items struct{ s *Store }

// Item types as the type column (and AQL) spell them.
const (
	typeFile   = "file"
	typeFolder = "folder"
)

var itemWriteCols = []string{
	"repo_key", "cached", "path", "name", "depth", "type", "size",
	"sha256", "sha512", "sha1", "md5",
	"original_sha256", "original_sha512", "original_sha1", "original_md5", "mime_type",
	"created_at", "created_by", "modified_at", "modified_by", "updated_at", "remote_url",
}

// itemCols are the columns scanItem reads. props_modified_at is not one of
// itemWriteCols: only the property methods write it, so a Put that
// replaces an item keeps it (model.Item.PropertiesModified).
var itemCols = "id, " + strings.Join(itemWriteCols, ", ") + ", props_modified_at"

func scanItem(row interface{ Scan(...any) error }) (model.Item, error) {
	var (
		it                                    model.Item
		typ                                   string
		created, modified, updated, propsTime sql.NullInt64
	)
	err := row.Scan(&it.ID, &it.RepoKey, &it.Cached, &it.Path, &it.Name, &it.Depth, &typ, &it.Size,
		&it.Actual.SHA256, &it.Actual.SHA512, &it.Actual.SHA1, &it.Actual.MD5,
		&it.Original.SHA256, &it.Original.SHA512, &it.Original.SHA1, &it.Original.MD5, &it.MimeType,
		&created, &it.CreatedBy, &modified, &it.ModifiedBy, &updated, &it.RemoteURL, &propsTime)
	it.Folder = typ == typeFolder
	it.Created, it.Modified, it.Updated = dialect.FromMicros(
		created,
	), dialect.FromMicros(
		modified,
	), dialect.FromMicros(
		updated,
	)
	it.PropertiesModified = dialect.FromMicros(propsTime)
	return it, err //nolint:wrapcheck // wrapped by the caller
}

// Search compiles q for the dialect and loads the properties of the hits
// in a second query, after offset and limit, as decision 0012 requires.
// The roots of q without a record are rows no table holds; q.WithRoots
// merges in those that match.
func (i items) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	d := aql.SQLite
	if i.s.Dialect().Name == "postgres" {
		d = aql.PostgreSQL
	}
	var hits []aql.Hit
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		roots, err := i.missingRoots(ctx, q, o)
		if err != nil {
			return err
		}
		hits, err = q.WithRoots(o, roots, func(q *aql.Query, o aql.Options) ([]aql.Hit, error) {
			return i.search(ctx, d, q, o)
		})
		if err != nil {
			return fmt.Errorf("search items with roots: %w", err)
		}
		if q.Properties() {
			if err := i.hitProperties(ctx, hits); err != nil {
				return err
			}
		}
		if len(q.Stats()) == 0 {
			return nil
		}
		return i.hitStats(ctx, hits)
	})
	return hits, err
}

// search runs q's compiled SQL over the items table.
func (i items) search(
	ctx context.Context,
	d aql.Dialect,
	q *aql.Query,
	o aql.Options,
) ([]aql.Hit, error) {
	c, err := q.Compile(d, o)
	if err != nil {
		return nil, fmt.Errorf("search items: %w", err)
	}
	var hits []aql.Hit
	err = i.s.Rows(ctx, "search items",
		"SELECT "+itemCols+" FROM items WHERE "+c.Where+c.Tail, c.Args,
		func(rs *sql.Rows) error {
			it, err := scanItem(rs)
			hits = append(hits, aql.Hit{Item: it})
			return err
		})
	return hits, err
}

// missingRoots returns the roots of q that have no record and that q's
// criteria match, as aql.RootHit makes them. A record is looked up by the
// natural key, searchBatch repositories at a time.
func (i items) missingRoots(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	refs := q.Roots()
	recorded := map[aql.RepoRef]bool{}
	for start := 0; start < len(refs); start += searchBatch {
		batch := refs[start:min(start+searchBatch, len(refs))]
		args := []any{kernel.RootPath, kernel.RootPath}
		for _, r := range batch {
			args = append(args, r.Key)
		}
		in := "IN (" + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		if err := i.s.Rows(ctx, "search item roots",
			"SELECT repo_key, cached FROM items WHERE path = ? AND name = ? AND repo_key "+in,
			args,
			func(rs *sql.Rows) error {
				var r aql.RepoRef
				err := rs.Scan(&r.Key, &r.Cached)
				recorded[r] = true
				return err //nolint:wrapcheck // wrapped by Rows
			}); err != nil {
			return nil, err
		}
	}
	return q.MissingRoots(o, func(r aql.RepoRef) bool { return recorded[r] }), nil
}

// searchBatch bounds the IN list of one property or statistics query, well
// under SQLite's and PostgreSQL's parameter limits.
const searchBatch = 500

// eachBatch calls load with an "IN (?,...)" list and its arguments for the
// IDs of hits, searchBatch at a time.
func eachBatch(hits []aql.Hit, load func(in string, args []any) error) error {
	for start := 0; start < len(hits); start += searchBatch {
		batch := hits[start:min(start+searchBatch, len(hits))]
		args := make([]any, len(batch))
		for n, h := range batch {
			args[n] = h.Item.ID
		}
		in := "IN (" + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		if err := load(in, args); err != nil {
			return err
		}
	}
	return nil
}

func hitIndex(hits []aql.Hit) map[int64]int {
	byID := make(map[int64]int, len(hits))
	for n, h := range hits {
		byID[h.Item.ID] = n
	}
	return byID
}

// hitProperties fills each hit's properties.
func (i items) hitProperties(ctx context.Context, hits []aql.Hit) error {
	byID := hitIndex(hits)
	return eachBatch(hits, func(in string, args []any) error {
		return i.s.Rows(ctx, "search item properties",
			"SELECT item_id, key, value FROM item_properties WHERE item_id "+in+
				" ORDER BY item_id, key, value",
			args,
			func(rs *sql.Rows) error {
				var (
					id   int64
					k, v string
				)
				if err := rs.Scan(&id, &k, &v); err != nil {
					return err //nolint:wrapcheck // wrapped by Rows
				}
				h := &hits[byID[id]]
				if h.Properties == nil {
					h.Properties = model.Properties{}
				}
				h.Properties[k] = append(h.Properties[k], v)
				return nil
			})
	})
}

// hitStats fills the statistics of each hit that has a record.
func (i items) hitStats(ctx context.Context, hits []aql.Hit) error {
	byID := hitIndex(hits)
	return eachBatch(hits, func(in string, args []any) error {
		return i.s.Rows(ctx, "search item statistics",
			"SELECT "+strings.Join(statsCols, ", ")+" FROM item_stats WHERE item_id "+in,
			args,
			func(rs *sql.Rows) error {
				var (
					id               int64
					st               model.ItemStats
					last, remoteLast sql.NullInt64
				)
				if err := rs.Scan(&id, &st.DownloadCount, &last, &st.LastDownloadedBy,
					&st.RemoteDownloadCount, &remoteLast, &st.RemoteLastDownloadedBy,
					&st.RemoteOrigin, &st.RemotePath); err != nil {
					return err //nolint:wrapcheck // wrapped by Rows
				}
				st.LastDownloaded = dialect.FromMicros(last)
				st.RemoteLastDownloaded = dialect.FromMicros(remoteLast)
				hits[byID[id]].Stats = &st
				return nil
			})
	})
}

// canonical derives Path, Name and Depth from the item's full path.
func canonical(it model.Item) (model.Item, error) {
	if it.RepoKey == "" {
		return it, fmt.Errorf("item: repository key is required: %w", kernel.ErrInvalid)
	}
	parent, name, depth, err := kernel.SplitItemPath(it.FullPath())
	if err != nil {
		return it, fmt.Errorf("item: %w: %w", kernel.ErrInvalid, err)
	}
	it.Path, it.Name, it.Depth = parent, name, depth
	return it, nil
}

// folderPath normalises a folder argument: "" and "." are the root.
func folderPath(folder string) string {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return kernel.RootPath
	}
	return folder
}

func (i items) Put(ctx context.Context, it model.Item) (model.Item, error) {
	return i.put(
		ctx,
		it,
		dialect.Upsert("items", itemWriteCols, []string{"repo_key", "cached", "path", "name"}),
	)
}

func (i items) Create(ctx context.Context, it model.Item) (model.Item, error) {
	return i.put(ctx, it, dialect.Insert("items", itemWriteCols))
}

func (i items) put(ctx context.Context, it model.Item, statement string) (model.Item, error) {
	it, err := canonical(it)
	if err != nil {
		return it, err
	}
	typ := typeFile
	if it.Folder {
		typ = typeFolder
	}
	id, err := i.s.InsertID(
		ctx,
		"put item "+it.FullPath(),
		statement,
		it.RepoKey,
		it.Cached,
		it.Path,
		it.Name,
		it.Depth,
		typ,
		it.Size,
		it.Actual.SHA256,
		it.Actual.SHA512,
		it.Actual.SHA1,
		it.Actual.MD5,
		it.Original.SHA256,
		it.Original.SHA512,
		it.Original.SHA1,
		it.Original.MD5,
		it.MimeType,
		dialect.Micros(
			it.Created,
		),
		it.CreatedBy,
		dialect.Micros(it.Modified),
		it.ModifiedBy,
		dialect.Micros(it.Updated),
		it.RemoteURL,
	)
	if err != nil {
		return it, err
	}
	return i.GetByID(ctx, id)
}

func (i items) Get(
	ctx context.Context,
	repoKey string,
	cached bool,
	fullPath string,
) (model.Item, error) {
	parent, name, _, err := kernel.SplitItemPath(fullPath)
	if err != nil {
		return model.Item{}, fmt.Errorf("get item %q: %w", fullPath, kernel.ErrNotFound)
	}
	it, err := scanItem(i.s.Q(ctx).QueryRowContext(ctx, i.s.Dialect().Rebind(
		"SELECT "+itemCols+" FROM items WHERE repo_key = ? AND cached = ? AND path = ? AND name = ?",
	),
		repoKey, cached, parent, name))
	return it, i.s.Wrap(fmt.Sprintf("get item %s:%s", repoKey, fullPath), err)
}

func (i items) GetByID(ctx context.Context, id int64) (model.Item, error) {
	it, err := scanItem(i.s.Q(ctx).QueryRowContext(ctx, i.s.Dialect().Rebind(
		"SELECT "+itemCols+" FROM items WHERE id = ?"+i.s.LockClause(ctx)), id))
	return it, i.s.Wrap(fmt.Sprintf("get item %d", id), err)
}

// list reads one page of items matching where, ordered by cols.
func (i items) list(
	ctx context.Context,
	op, where string,
	args []any,
	p kernel.Page,
	cols []string,
	ints map[string]bool,
	key func(model.Item) []string,
) ([]model.Item, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	cond, cargs, err := dialect.Keyset(p, cols, ints, false)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	var out []model.Item
	err = i.s.Rows(ctx, op, "SELECT "+itemCols+" FROM items WHERE "+where+" AND "+cond+
		dialect.OrderBy(cols, false)+" LIMIT ?", append(append(args, cargs...), p.Limit+1),
		func(rs *sql.Rows) error {
			it, err := scanItem(rs)
			out = append(out, it)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(out, p.Limit, key)
	return page, next, nil
}

func (i items) Children(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	return i.list(
		ctx,
		"list children of "+folder,
		"repo_key = ? AND cached = ? AND path = ? AND "+notRoot,
		[]any{repoKey, cached, folderPath(folder)},
		p,
		[]string{"name"},
		nil,
		func(it model.Item) []string { return []string{it.Name} },
	)
}

// notRoot leaves out a repository root's record (model.RootItem), which
// has the root's path but is not inside the root. Depth is enough: every
// other item is at least one segment deep.
const notRoot = "depth > 0"

// under returns the condition and arguments selecting every item below
// folder (not folder itself).
func under(folder string) (string, []any) {
	folder = folderPath(folder)
	if folder == kernel.RootPath {
		return notRoot, nil
	}
	prefix := folder + "/"
	// substr counts characters on both engines; LIKE would need escaping of
	// the wildcards paths may contain.
	return "(path = ? OR substr(path, 1, ?) = ?)", []any{
		folder,
		utf8.RuneCountInString(prefix),
		prefix,
	}
}

func (i items) Descendants(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	cond, args := under(folder)
	return i.list(
		ctx,
		"list descendants of "+folder,
		"repo_key = ? AND cached = ? AND "+cond,
		append([]any{repoKey, cached}, args...),
		p,
		[]string{"path", "name"},
		nil,
		func(it model.Item) []string { return []string{it.Path, it.Name} },
	)
}

func (i items) Delete(
	ctx context.Context, repoKey string, cached bool, fullPath string,
) (port.DeleteResult, error) {
	res := port.DeleteResult{BlobRefs: map[string]int64{}}
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		where := "repo_key = ? AND cached = ? AND " + notRoot
		args := []any{repoKey, cached}
		if folderPath(fullPath) != kernel.RootPath {
			it, err := i.Get(ctx, repoKey, cached, fullPath)
			if err != nil {
				return err
			}
			if it.Folder {
				cond, cargs := under(it.FullPath())
				where += " AND (id = ? OR " + cond + ")"
				args = append(append(args, it.ID), cargs...)
			} else {
				where += " AND id = ?"
				args = append(args, it.ID)
			}
		}
		err := i.s.Rows(
			ctx,
			"delete items",
			"SELECT type, size, sha256 FROM items WHERE "+where,
			args,
			func(rs *sql.Rows) error {
				var (
					typ, sha string
					size     int64
				)
				if err := rs.Scan(&typ, &size, &sha); err != nil {
					return err //nolint:wrapcheck // wrapped by Rows
				}
				if typ == typeFolder {
					res.Folders++
					return nil
				}
				res.Files++
				res.Bytes += size
				if sha != "" {
					res.BlobRefs[sha]++
				}
				return nil
			},
		)
		if err != nil {
			return err
		}
		_, err = i.s.Exec(ctx, "delete items", "DELETE FROM items WHERE "+where, args...)
		return err
	})
	if err != nil {
		return port.DeleteResult{}, err
	}
	return res, nil
}

func (i items) Root(ctx context.Context, repoKey string, cached bool) (model.Item, error) {
	it, err := scanItem(i.s.Q(ctx).QueryRowContext(ctx, i.s.Dialect().Rebind(
		"SELECT "+itemCols+" FROM items WHERE repo_key = ? AND cached = ? AND path = ? AND name = ?",
	),
		repoKey, cached, kernel.RootPath, kernel.RootPath))
	return it, i.s.Wrap("get the root of "+repoKey, err)
}

func (i items) EnsureRoot(ctx context.Context, r model.Item) (model.Item, error) {
	if r.RepoKey == "" {
		return r, fmt.Errorf("repository root: repository key is required: %w", kernel.ErrInvalid)
	}
	root := model.RootItem(r.RepoKey, r.Cached)
	var out model.Item
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		// Ignoring the conflict keeps an existing record as it was, and two
		// requests creating it at once end with one.
		if _, err := i.s.Exec(
			ctx,
			"create the root of "+r.RepoKey,
			dialect.InsertIgnore(
				"items",
				itemWriteCols,
				[]string{"repo_key", "cached", "path", "name"},
			),
			root.RepoKey,
			root.Cached,
			root.Path,
			root.Name,
			root.Depth,
			typeFolder,
			int64(0),
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			dialect.Micros(r.Created),
			r.CreatedBy,
			dialect.Micros(r.Modified),
			r.ModifiedBy,
			dialect.Micros(r.Updated),
			"",
		); err != nil {
			return err
		}
		var err error
		out, err = i.Root(ctx, r.RepoKey, r.Cached)
		return err
	})
	return out, err
}

func (i items) DeleteRoot(ctx context.Context, repoKey string, cached bool) error {
	_, err := i.s.Exec(ctx, "delete the root of "+repoKey,
		"DELETE FROM items WHERE repo_key = ? AND cached = ? AND path = ? AND name = ?",
		repoKey, cached, kernel.RootPath, kernel.RootPath)
	return err
}

func (i items) FindByChecksum(
	ctx context.Context,
	c kernel.Checksums,
	p kernel.Page,
) ([]model.Item, string, error) {
	var col, val string
	switch {
	case c.SHA256 != "":
		col, val = "sha256", c.SHA256
	case c.SHA1 != "":
		col, val = "sha1", c.SHA1
	case c.MD5 != "":
		col, val = "md5", c.MD5
	default:
		return nil, "", fmt.Errorf("find items: no checksum given: %w", kernel.ErrInvalid)
	}
	return i.list(ctx, "find items by "+col, "type = ? AND "+col+" = ?", []any{typeFile, val},
		p, []string{"id"}, map[string]bool{"id": true},
		func(it model.Item) []string { return []string{dialect.Itoa(it.ID)} })
}

func (i items) Usage(ctx context.Context) ([]port.RepoUsage, error) {
	var out []port.RepoUsage
	err := i.s.Rows(ctx, "repository usage",
		"SELECT repo_key, cached,"+
			// Cast because PostgreSQL sums BIGINT into NUMERIC.
			" CAST(SUM(CASE WHEN type = 'file' THEN 1 ELSE 0 END) AS BIGINT),"+
			" CAST(SUM(CASE WHEN type = 'folder' THEN 1 ELSE 0 END) AS BIGINT),"+
			" CAST(SUM(CASE WHEN type = 'file' THEN size ELSE 0 END) AS BIGINT)"+
			" FROM items WHERE "+notRoot+" GROUP BY repo_key, cached ORDER BY repo_key, cached", nil,
		func(rs *sql.Rows) error {
			var u port.RepoUsage
			err := rs.Scan(&u.RepoKey, &u.Cached, &u.Files, &u.Folders, &u.Bytes)
			out = append(out, u)
			return err //nolint:wrapcheck // wrapped by Rows
		})
	return out, err
}

// requireItem fails with ErrNotFound when no item has id.
func (i items) requireItem(ctx context.Context, id int64) error {
	ok, err := i.s.Exists(
		ctx,
		"find item",
		"SELECT 1 FROM items WHERE id = ?"+i.s.LockClause(ctx),
		id,
	)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("item %d: %w", id, kernel.ErrNotFound)
	}
	return nil
}

func (i items) Properties(ctx context.Context, id int64) (model.Properties, error) {
	var out model.Properties
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		var err error
		out, err = i.props(ctx, id)
		return err
	})
	return out, err
}

// props reads the properties of the item with id, which must exist.
func (i items) props(ctx context.Context, id int64) (model.Properties, error) {
	out := model.Properties{}
	err := i.s.Rows(
		ctx,
		"item properties",
		"SELECT key, value FROM item_properties WHERE item_id = ? ORDER BY key, value",
		[]any{id},
		func(rs *sql.Rows) error {
			var k, v string
			if err := rs.Scan(&k, &v); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out[k] = append(out[k], v)
			return nil
		},
	)
	return out, err
}

// changeProperties runs write, which changes the properties of the item
// with id, and stamps the item with at when they changed
// (port.ItemStore). The properties are read back rather than predicted,
// so the stamp follows what the columns' collation and the truncation kept.
func (i items) changeProperties(
	ctx context.Context,
	id int64,
	at time.Time,
	write func(context.Context) error,
) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		before, err := i.props(ctx, id)
		if err != nil {
			return err
		}
		if err := write(ctx); err != nil {
			return err
		}
		after, err := i.props(ctx, id)
		if err != nil || after.Equal(before) {
			return err
		}
		stamp := dialect.Micros(at)
		if len(after) == 0 {
			stamp = nil
		}
		_, err = i.s.Exec(ctx, "stamp properties",
			"UPDATE items SET props_modified_at = ? WHERE id = ?", stamp, id)
		return err
	})
}

// setKeys replaces the values of every key in props, each cut to
// model.PropertyValueMax; values that become equal are stored once.
func (i items) setKeys(ctx context.Context, id int64, props model.Properties) error {
	for k, values := range props {
		if _, err := i.s.Exec(ctx, "set property "+k,
			"DELETE FROM item_properties WHERE item_id = ? AND key = ?", id, k); err != nil {
			return err
		}
		for _, v := range values {
			if _, err := i.s.Exec(
				ctx,
				"set property "+k,
				dialect.InsertIgnore("item_properties", []string{"item_id", "key", "value"}, nil),
				id,
				k,
				model.TruncatePropertyValue(v, model.PropertyValueMax),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i items) SetProperties(
	ctx context.Context,
	id int64,
	props model.Properties,
	at time.Time,
) error {
	if err := port.CheckPropertyKeys(props); err != nil {
		return fmt.Errorf("properties of item %d: %w", id, err)
	}
	return i.changeProperties(ctx, id, at, func(ctx context.Context) error {
		if _, err := i.s.Exec(ctx, "clear properties",
			"DELETE FROM item_properties WHERE item_id = ?", id); err != nil {
			return err
		}
		return i.setKeys(ctx, id, props)
	})
}

func (i items) PatchProperties(
	ctx context.Context,
	id int64,
	set model.Properties,
	remove []string,
	at time.Time,
) error {
	if err := port.CheckPropertyKeys(set); err != nil {
		return fmt.Errorf("properties of item %d: %w", id, err)
	}
	return i.changeProperties(ctx, id, at, func(ctx context.Context) error {
		for _, k := range remove {
			if _, err := i.s.Exec(ctx, "remove property "+k,
				"DELETE FROM item_properties WHERE item_id = ? AND key = ?", id, k); err != nil {
				return err
			}
		}
		return i.setKeys(ctx, id, set)
	})
}

var statsCols = []string{
	"item_id", "download_count", "last_downloaded_at", "last_downloaded_by",
	"remote_download_count", "remote_last_downloaded_at", "remote_last_downloaded_by",
	"remote_origin", "remote_path",
}

func (i items) Stats(ctx context.Context, id int64) (model.ItemStats, error) {
	var out model.ItemStats
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		var last, remoteLast sql.NullInt64
		err := i.s.Q(ctx).QueryRowContext(ctx, i.s.Dialect().Rebind(
			"SELECT "+strings.Join(
				statsCols[1:],
				", ",
			)+" FROM item_stats WHERE item_id = ?",
		), id).Scan(
			&out.DownloadCount, &last, &out.LastDownloadedBy,
			&out.RemoteDownloadCount, &remoteLast, &out.RemoteLastDownloadedBy,
			&out.RemoteOrigin, &out.RemotePath)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		out.LastDownloaded, out.RemoteLastDownloaded = dialect.FromMicros(last),
			dialect.FromMicros(remoteLast)
		return i.s.Wrap("item statistics", err)
	})
	return out, err
}

func (i items) RecordDownload(ctx context.Context, id int64, d port.Download) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		if _, err := i.s.Exec(
			ctx,
			"record download",
			dialect.InsertIgnore(
				"item_stats",
				[]string{"item_id"},
				[]string{"item_id"},
			),
			id,
		); err != nil {
			return err
		}
		if d.Remote {
			_, err := i.s.Exec(ctx, "record download",
				"UPDATE item_stats SET remote_download_count = remote_download_count + 1,"+
					" remote_last_downloaded_at = ?, remote_last_downloaded_by = ?, remote_origin = ?, remote_path = ?"+
					" WHERE item_id = ?", dialect.Micros(d.At), d.By, d.Origin, d.Path, id)
			return err
		}
		_, err := i.s.Exec(
			ctx,
			"record download",
			"UPDATE item_stats SET download_count = download_count + 1, last_downloaded_at = ?, last_downloaded_by = ?"+
				" WHERE item_id = ?",
			dialect.Micros(d.At),
			d.By,
			id,
		)
		return err
	})
}

func (i items) PutStats(ctx context.Context, id int64, st model.ItemStats) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		_, err := i.s.Exec(
			ctx,
			"put statistics",
			dialect.Upsert("item_stats", statsCols, []string{"item_id"}),
			id,
			st.DownloadCount,
			dialect.Micros(st.LastDownloaded),
			st.LastDownloadedBy,
			st.RemoteDownloadCount,
			dialect.Micros(st.RemoteLastDownloaded),
			st.RemoteLastDownloadedBy,
			st.RemoteOrigin,
			st.RemotePath,
		)
		return err
	})
}
