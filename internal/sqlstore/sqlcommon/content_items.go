package sqlcommon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type items struct{ s *Store }

// Item types as the type column (and AQL) spell them.
const (
	typeFile   = "file"
	typeFolder = "folder"
)

var itemWriteCols = []string{
	"repo_key", "cached", "path", "name", "depth", "type", "size",
	"sha256", "sha1", "md5", "original_sha256", "original_sha1", "original_md5", "mime_type",
	"created_at", "created_by", "modified_at", "modified_by", "updated_at", "remote_url",
}

var itemCols = "id, " + strings.Join(itemWriteCols, ", ")

func scanItem(row interface{ Scan(...any) error }) (types.Item, error) {
	var (
		it                         types.Item
		typ                        string
		created, modified, updated sql.NullInt64
	)
	err := row.Scan(&it.ID, &it.RepoKey, &it.Cached, &it.Path, &it.Name, &it.Depth, &typ, &it.Size,
		&it.Actual.SHA256, &it.Actual.SHA1, &it.Actual.MD5,
		&it.Original.SHA256, &it.Original.SHA1, &it.Original.MD5, &it.MimeType,
		&created, &it.CreatedBy, &modified, &it.ModifiedBy, &updated, &it.RemoteURL)
	it.Folder = typ == typeFolder
	it.Created, it.Modified, it.Updated = fromMicros(
		created,
	), fromMicros(
		modified,
	), fromMicros(
		updated,
	)
	return it, err //nolint:wrapcheck // wrapped by the caller
}

// Search compiles q for the dialect and loads the properties of the hits
// in a second query, after offset and limit, as decision 0012 requires.
func (i items) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	d := aql.SQLite
	if i.s.d.Name == "postgres" {
		d = aql.PostgreSQL
	}
	c, err := q.Compile(d, o)
	if err != nil {
		return nil, fmt.Errorf("search items: %w", err)
	}
	var hits []aql.Hit
	err = i.s.Tx(ctx, func(ctx context.Context) error {
		hits = nil
		if err := i.s.rows(ctx, "search items",
			"SELECT "+itemCols+" FROM items WHERE "+c.Where+c.Tail, c.Args,
			func(rs *sql.Rows) error {
				it, err := scanItem(rs)
				hits = append(hits, aql.Hit{Item: it})
				return err
			}); err != nil {
			return err
		}
		if !q.Properties() {
			return nil
		}
		return i.hitProperties(ctx, hits)
	})
	return hits, err
}

// searchPropertyBatch bounds the IN list of one property query, well under
// SQLite's and PostgreSQL's parameter limits.
const searchPropertyBatch = 500

// hitProperties fills each hit's properties.
func (i items) hitProperties(ctx context.Context, hits []aql.Hit) error {
	byID := make(map[int64]int, len(hits))
	for n, h := range hits {
		byID[h.Item.ID] = n
	}
	for start := 0; start < len(hits); start += searchPropertyBatch {
		batch := hits[start:min(start+searchPropertyBatch, len(hits))]
		args := make([]any, len(batch))
		for n, h := range batch {
			args[n] = h.Item.ID
		}
		err := i.s.rows(ctx, "search item properties",
			"SELECT item_id, key, value FROM item_properties WHERE item_id IN ("+
				strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+
				") ORDER BY item_id, key, value",
			args,
			func(rs *sql.Rows) error {
				var (
					id   int64
					k, v string
				)
				if err := rs.Scan(&id, &k, &v); err != nil {
					return err //nolint:wrapcheck // wrapped by rows
				}
				h := &hits[byID[id]]
				if h.Properties == nil {
					h.Properties = types.Properties{}
				}
				h.Properties[k] = append(h.Properties[k], v)
				return nil
			})
		if err != nil {
			return err
		}
	}
	return nil
}

// canonical derives Path, Name and Depth from the item's full path.
func canonical(it types.Item) (types.Item, error) {
	if it.RepoKey == "" {
		return it, fmt.Errorf("item: repository key is required: %w", store.ErrInvalid)
	}
	parent, name, depth, err := types.SplitItemPath(it.FullPath())
	if err != nil {
		return it, fmt.Errorf("item: %w: %w", store.ErrInvalid, err)
	}
	it.Path, it.Name, it.Depth = parent, name, depth
	return it, nil
}

// folderPath normalises a folder argument: "" and "." are the root.
func folderPath(folder string) string {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return types.RootPath
	}
	return folder
}

func (i items) Put(ctx context.Context, it types.Item) (types.Item, error) {
	it, err := canonical(it)
	if err != nil {
		return it, err
	}
	typ := typeFile
	if it.Folder {
		typ = typeFolder
	}
	id, err := i.s.insertID(
		ctx,
		"put item "+it.FullPath(),
		Upsert("items", itemWriteCols, []string{"repo_key", "cached", "path", "name"}),
		it.RepoKey,
		it.Cached,
		it.Path,
		it.Name,
		it.Depth,
		typ,
		it.Size,
		it.Actual.SHA256,
		it.Actual.SHA1,
		it.Actual.MD5,
		it.Original.SHA256,
		it.Original.SHA1,
		it.Original.MD5,
		it.MimeType,
		micros(
			it.Created,
		),
		it.CreatedBy,
		micros(it.Modified),
		it.ModifiedBy,
		micros(it.Updated),
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
) (types.Item, error) {
	parent, name, _, err := types.SplitItemPath(fullPath)
	if err != nil {
		return types.Item{}, fmt.Errorf("get item %q: %w", fullPath, store.ErrNotFound)
	}
	it, err := scanItem(i.s.q(ctx).QueryRowContext(ctx, i.s.d.Rebind(
		"SELECT "+itemCols+" FROM items WHERE repo_key = ? AND cached = ? AND path = ? AND name = ?",
	),
		repoKey, cached, parent, name))
	return it, i.s.wrap(fmt.Sprintf("get item %s:%s", repoKey, fullPath), err)
}

func (i items) GetByID(ctx context.Context, id int64) (types.Item, error) {
	it, err := scanItem(i.s.q(ctx).QueryRowContext(ctx, i.s.d.Rebind(
		"SELECT "+itemCols+" FROM items WHERE id = ?"), id))
	return it, i.s.wrap(fmt.Sprintf("get item %d", id), err)
}

// list reads one page of items matching where, ordered by cols.
func (i items) list(
	ctx context.Context,
	op, where string,
	args []any,
	p store.Page,
	cols []string,
	ints map[string]bool,
	key func(types.Item) []string,
) ([]types.Item, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	cond, cargs, err := keyset(p, cols, ints, false)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	var out []types.Item
	err = i.s.rows(ctx, op, "SELECT "+itemCols+" FROM items WHERE "+where+" AND "+cond+
		orderBy(cols, false)+" LIMIT ?", append(append(args, cargs...), p.Limit+1),
		func(rs *sql.Rows) error {
			it, err := scanItem(rs)
			out = append(out, it)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, key)
	return page, next, nil
}

func (i items) Children(
	ctx context.Context, repoKey string, cached bool, folder string, p store.Page,
) ([]types.Item, string, error) {
	return i.list(ctx, "list children of "+folder,
		"repo_key = ? AND cached = ? AND path = ?", []any{repoKey, cached, folderPath(folder)},
		p, []string{"name"}, nil, func(it types.Item) []string { return []string{it.Name} })
}

// under returns the condition and arguments selecting every item below
// folder (not folder itself).
func under(folder string) (string, []any) {
	folder = folderPath(folder)
	if folder == types.RootPath {
		return "1 = 1", nil
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
	ctx context.Context, repoKey string, cached bool, folder string, p store.Page,
) ([]types.Item, string, error) {
	cond, args := under(folder)
	return i.list(
		ctx,
		"list descendants of "+folder,
		"repo_key = ? AND cached = ? AND "+cond,
		append([]any{repoKey, cached}, args...),
		p,
		[]string{"path", "name"},
		nil,
		func(it types.Item) []string { return []string{it.Path, it.Name} },
	)
}

func (i items) Delete(
	ctx context.Context, repoKey string, cached bool, fullPath string,
) (store.DeleteResult, error) {
	res := store.DeleteResult{BlobRefs: map[string]int64{}}
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		where := "repo_key = ? AND cached = ?"
		args := []any{repoKey, cached}
		if folderPath(fullPath) != types.RootPath {
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
		err := i.s.rows(
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
					return err //nolint:wrapcheck // wrapped by rows
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
		_, err = i.s.exec(ctx, "delete items", "DELETE FROM items WHERE "+where, args...)
		return err
	})
	if err != nil {
		return store.DeleteResult{}, err
	}
	return res, nil
}

func (i items) FindByChecksum(
	ctx context.Context,
	c types.Checksums,
	p store.Page,
) ([]types.Item, string, error) {
	var col, val string
	switch {
	case c.SHA256 != "":
		col, val = "sha256", c.SHA256
	case c.SHA1 != "":
		col, val = "sha1", c.SHA1
	case c.MD5 != "":
		col, val = "md5", c.MD5
	default:
		return nil, "", fmt.Errorf("find items: no checksum given: %w", store.ErrInvalid)
	}
	return i.list(ctx, "find items by "+col, "type = ? AND "+col+" = ?", []any{typeFile, val},
		p, []string{"id"}, map[string]bool{"id": true},
		func(it types.Item) []string { return []string{itoa(it.ID)} })
}

func (i items) Usage(ctx context.Context) ([]store.RepoUsage, error) {
	var out []store.RepoUsage
	err := i.s.rows(ctx, "repository usage",
		"SELECT repo_key, cached,"+
			// Cast because PostgreSQL sums BIGINT into NUMERIC.
			" CAST(SUM(CASE WHEN type = 'file' THEN 1 ELSE 0 END) AS BIGINT),"+
			" CAST(SUM(CASE WHEN type = 'folder' THEN 1 ELSE 0 END) AS BIGINT),"+
			" CAST(SUM(CASE WHEN type = 'file' THEN size ELSE 0 END) AS BIGINT)"+
			" FROM items GROUP BY repo_key, cached ORDER BY repo_key, cached", nil,
		func(rs *sql.Rows) error {
			var u store.RepoUsage
			err := rs.Scan(&u.RepoKey, &u.Cached, &u.Files, &u.Folders, &u.Bytes)
			out = append(out, u)
			return err //nolint:wrapcheck // wrapped by rows
		})
	return out, err
}

// requireItem fails with ErrNotFound when no item has id.
func (i items) requireItem(ctx context.Context, id int64) error {
	ok, err := i.s.exists(ctx, "find item", "SELECT 1 FROM items WHERE id = ?", id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("item %d: %w", id, store.ErrNotFound)
	}
	return nil
}

func (i items) Properties(ctx context.Context, id int64) (types.Properties, error) {
	var out types.Properties
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		out = types.Properties{}
		return i.s.rows(
			ctx,
			"item properties",
			"SELECT key, value FROM item_properties WHERE item_id = ? ORDER BY key, value",
			[]any{id},
			func(rs *sql.Rows) error {
				var k, v string
				if err := rs.Scan(&k, &v); err != nil {
					return err //nolint:wrapcheck // wrapped by rows
				}
				out[k] = append(out[k], v)
				return nil
			},
		)
	})
	return out, err
}

// setKeys replaces the values of every key in props.
func (i items) setKeys(ctx context.Context, id int64, props types.Properties) error {
	for k, values := range props {
		if _, err := i.s.exec(ctx, "set property "+k,
			"DELETE FROM item_properties WHERE item_id = ? AND key = ?", id, k); err != nil {
			return err
		}
		for _, v := range values {
			if _, err := i.s.exec(
				ctx,
				"set property "+k,
				InsertIgnore(
					"item_properties",
					[]string{"item_id", "key", "value"},
					[]string{"item_id", "key", "value"},
				),
				id,
				k,
				v,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i items) SetProperties(ctx context.Context, id int64, props types.Properties) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		if _, err := i.s.exec(ctx, "clear properties",
			"DELETE FROM item_properties WHERE item_id = ?", id); err != nil {
			return err
		}
		return i.setKeys(ctx, id, props)
	})
}

func (i items) PatchProperties(
	ctx context.Context,
	id int64,
	set types.Properties,
	remove []string,
) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		for _, k := range remove {
			if _, err := i.s.exec(ctx, "remove property "+k,
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

func (i items) Stats(ctx context.Context, id int64) (types.ItemStats, error) {
	var out types.ItemStats
	err := i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		var last, remoteLast sql.NullInt64
		err := i.s.q(ctx).QueryRowContext(ctx, i.s.d.Rebind(
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
		out.LastDownloaded, out.RemoteLastDownloaded = fromMicros(last), fromMicros(remoteLast)
		return i.s.wrap("item statistics", err)
	})
	return out, err
}

func (i items) RecordDownload(ctx context.Context, id int64, d store.Download) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		if _, err := i.s.exec(ctx, "record download",
			InsertIgnore("item_stats", []string{"item_id"}, []string{"item_id"}), id); err != nil {
			return err
		}
		if d.Remote {
			_, err := i.s.exec(ctx, "record download",
				"UPDATE item_stats SET remote_download_count = remote_download_count + 1,"+
					" remote_last_downloaded_at = ?, remote_last_downloaded_by = ?, remote_origin = ?, remote_path = ?"+
					" WHERE item_id = ?", micros(d.At), d.By, d.Origin, d.Path, id)
			return err
		}
		_, err := i.s.exec(
			ctx,
			"record download",
			"UPDATE item_stats SET download_count = download_count + 1, last_downloaded_at = ?, last_downloaded_by = ?"+
				" WHERE item_id = ?",
			micros(d.At),
			d.By,
			id,
		)
		return err
	})
}

func (i items) PutStats(ctx context.Context, id int64, st types.ItemStats) error {
	return i.s.Tx(ctx, func(ctx context.Context) error {
		if err := i.requireItem(ctx, id); err != nil {
			return err
		}
		_, err := i.s.exec(
			ctx,
			"put statistics",
			Upsert("item_stats", statsCols, []string{"item_id"}),
			id,
			st.DownloadCount,
			micros(st.LastDownloaded),
			st.LastDownloadedBy,
			st.RemoteDownloadCount,
			micros(st.RemoteLastDownloaded),
			st.RemoteLastDownloadedBy,
			st.RemoteOrigin,
			st.RemotePath,
		)
		return err
	})
}
