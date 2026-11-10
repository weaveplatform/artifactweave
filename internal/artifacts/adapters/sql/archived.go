package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// ArchivedItems returns the archived item store: the records archived
// items leave behind (decision 0024).
func (s *Store) ArchivedItems() port.ArchivedItemStore { return archivedItemStore{s} }

// ColdBlobs returns the cold tier's blob index: the blob index code over
// the cold_blobs table (decision 0024).
func (s *Store) ColdBlobs() port.BlobIndex { return blobIndex{s: s, table: "cold_blobs"} }

// ColdReplicas returns the cold tier's replica index: the replica index
// code over the cold_blob_replicas table (decision 0024).
func (s *Store) ColdReplicas() port.ReplicaIndex {
	return replicaIndex{s: s, table: "cold_blob_replicas"}
}

type archivedItemStore struct{ s *Store }

var archivedCols = []string{
	"namespace", "repo_key", "path", "name", "depth", "sha256", "sha512", "sha1", "md5", "size",
	"created_at", "created_by", "modified_at", "modified_by", "package_type", "package_name",
	"package_version", "policy_key", "execution_id", "archived_at", "expires_at",
	"cold_blob_sha256", "item", "properties", "stats",
}

// splitArchived returns the canonical location of an item's original path.
func splitArchived(full string) (parent, name string, depth int, err error) {
	parent, name, depth, err = kernel.SplitItemPath(full)
	if err != nil {
		return "", "", 0, fmt.Errorf("archived item: %w: %w", kernel.ErrInvalid, err)
	}
	return parent, name, depth, nil
}

func archivedRow(a model.ArchivedItem) ([]any, error) {
	parent, name, depth, err := splitArchived(a.Item.FullPath())
	if err != nil {
		return nil, err
	}
	it := a.Item
	return []any{
		a.Namespace,
		it.RepoKey,
		parent,
		name,
		depth,
		it.Actual.SHA256,
		it.Actual.SHA512,
		it.Actual.SHA1,
		it.Actual.MD5,
		it.Size,
		dialect.Micros(it.Created),
		it.CreatedBy,
		dialect.Micros(it.Modified),
		it.ModifiedBy,
		a.PackageType,
		a.PackageName,
		a.PackageVersion,
		a.PolicyKey,
		a.ExecutionID,
		dialect.Micros(a.ArchivedAt),
		dialect.Micros(a.ExpiresAt),
		a.ColdBlobSHA256,
		dialect.MustJSON(a.Item),
		dialect.MustJSON(a.Properties),
		dialect.MustJSON(a.Stats),
	}, nil
}

// archivedSelect reads the columns the record is rebuilt from; the item's
// own fields come from its document.
const archivedSelect = "SELECT id, namespace, package_type, package_name, package_version, policy_key," +
	" execution_id, archived_at, expires_at, cold_blob_sha256, item, properties, stats FROM archived_items"

func scanArchived(r dialect.EntRow) (model.ArchivedItem, error) {
	var (
		a                  model.ArchivedItem
		archived, expires  sql.NullInt64
		item, props, stats sql.NullString
	)
	if err := r.Scan(&a.ID, &a.Namespace, &a.PackageType, &a.PackageName, &a.PackageVersion,
		&a.PolicyKey, &a.ExecutionID, &archived, &expires, &a.ColdBlobSHA256,
		&item, &props, &stats); err != nil {
		return a, err //nolint:wrapcheck // wrapped by the caller
	}
	a.ArchivedAt, a.ExpiresAt = dialect.FromMicros(archived), dialect.FromMicros(expires)
	if err := dialect.FromJSON(item, &a.Item); err != nil {
		return a, err
	}
	if err := dialect.FromJSON(props, &a.Properties); err != nil {
		return a, err
	}
	return a, dialect.FromJSON(stats, &a.Stats)
}

func (a archivedItemStore) AddItem(
	ctx context.Context,
	it model.ArchivedItem,
) (model.ArchivedItem, error) {
	it = kernel.Normalise(it)
	row, err := archivedRow(it)
	if err != nil {
		return model.ArchivedItem{}, err
	}
	if it.ID, err = a.s.InsertID(
		ctx,
		"add archived item",
		dialect.Insert("archived_items", archivedCols),
		row...); err != nil {
		return model.ArchivedItem{}, err
	}
	return it, nil
}

func (a archivedItemStore) Item(ctx context.Context, id int64) (model.ArchivedItem, error) {
	out, err := scanArchived(
		a.s.Q(ctx).QueryRowContext(ctx, a.s.Dialect().Rebind(archivedSelect+" WHERE id = ?"), id),
	)
	return out, a.s.Wrap(fmt.Sprintf("get archived item %d", id), err)
}

// archived runs an archived-item query.
func (a archivedItemStore) archived(
	ctx context.Context,
	q string,
	args ...any,
) ([]model.ArchivedItem, error) {
	var out []model.ArchivedItem
	err := a.s.Rows(ctx, "list archived items", q, args, func(r *sql.Rows) error {
		v, err := scanArchived(r)
		out = append(out, v)
		return err
	})
	return out, err
}

func (a archivedItemStore) FindItems(
	ctx context.Context,
	repoKey, fullPath string,
) ([]model.ArchivedItem, error) {
	parent, name, _, err := splitArchived(fullPath)
	if err != nil {
		return nil, err
	}
	return a.archived(
		ctx,
		archivedSelect+" WHERE repo_key = ? AND path = ? AND name = ? ORDER BY id DESC",
		repoKey,
		parent,
		name,
	)
}

func (a archivedItemStore) Items(
	ctx context.Context, f port.ArchivedFilter, p kernel.Page,
) ([]model.ArchivedItem, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, []string{"id"}, map[string]bool{"id": true}, false)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	if f.RepoKey != "" {
		where = append(where, "repo_key = ?")
		args = append(args, f.RepoKey)
	}
	if f.PolicyKey != "" {
		where = append(where, "policy_key = ?")
		args = append(args, f.PolicyKey)
	}
	if f.ExecutionID != 0 {
		where = append(where, "execution_id = ?")
		args = append(args, f.ExecutionID)
	}
	out, err := a.archived(
		ctx,
		archivedSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?",
		append(args, p.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(v model.ArchivedItem) []string { return []string{dialect.Itoa(v.ID)} },
	)
	return page, next, nil
}

func (a archivedItemStore) DeleteItem(ctx context.Context, id int64) error {
	return a.s.ExecOne(
		ctx,
		fmt.Sprintf("delete archived item %d", id),
		"DELETE FROM archived_items WHERE id = ?",
		id,
	)
}

func (a archivedItemStore) ExpiredItems(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]model.ArchivedItem, error) {
	return a.archived(ctx, archivedSelect+
		" WHERE expires_at IS NOT NULL AND expires_at < ? ORDER BY expires_at, id LIMIT ?",
		before.UnixMicro(), dialect.LimitOr(limit))
}
