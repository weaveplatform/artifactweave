package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Uploads implements store.Store.
func (s *Store) Uploads() store.UploadStore { return uploads{s} }

// --- Uploads ---------------------------------------------------------------

type uploads struct{ s *Store }

var uploadCols = []string{
	"id",
	"backend",
	"repo_key",
	"path",
	"state",
	"bytes",
	"started_at",
	"updated_at",
}

func scanUpload(row interface{ Scan(...any) error }) (store.Upload, error) {
	var (
		u                store.Upload
		started, updated sql.NullInt64
	)
	err := row.Scan(&u.ID, &u.Backend, &u.RepoKey, &u.Path, &u.State, &u.Bytes, &started, &updated)
	u.Started, u.Updated = fromMicros(started), fromMicros(updated)
	if len(u.State) == 0 {
		u.State = nil
	}
	return u, err //nolint:wrapcheck // wrapped by the caller
}

func (u uploads) Put(ctx context.Context, up store.Upload) error {
	if up.ID == "" {
		return fmt.Errorf("put upload: id is required: %w", store.ErrInvalid)
	}
	var state any
	if len(up.State) > 0 {
		state = up.State
	}
	_, err := u.s.exec(
		ctx,
		"put upload "+up.ID,
		Upsert("uploads", uploadCols, []string{"id"}),
		up.ID,
		up.Backend,
		up.RepoKey,
		up.Path,
		state,
		up.Bytes,
		micros(up.Started),
		micros(up.Updated),
	)
	return err
}

func (u uploads) Get(ctx context.Context, id string) (store.Upload, error) {
	up, err := scanUpload(u.s.q(ctx).QueryRowContext(ctx, u.s.d.Rebind(
		"SELECT "+strings.Join(uploadCols, ", ")+" FROM uploads WHERE id = ?"), id))
	return up, u.s.wrap("get upload "+id, err)
}

func (u uploads) Delete(ctx context.Context, id string) error {
	return u.s.execOne(ctx, "delete upload "+id, "DELETE FROM uploads WHERE id = ?", id)
}

func (u uploads) Stale(ctx context.Context, before time.Time, limit int) ([]store.Upload, error) {
	const at = "COALESCE(updated_at, started_at, 0)"
	var out []store.Upload
	err := u.s.rows(
		ctx,
		"stale uploads",
		"SELECT "+strings.Join(
			uploadCols,
			", ",
		)+" FROM uploads WHERE "+at+" < ? ORDER BY "+at+", id LIMIT ?",
		[]any{before.UnixMicro(), limitOr(limit)},
		func(rs *sql.Rows) error {
			up, err := scanUpload(rs)
			out = append(out, up)
			return err
		},
	)
	return out, err
}
