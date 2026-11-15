package sql

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// UploadDigests returns the upload digest store: the SHA-1 and MD5 an
// upload computed, by the content's SHA-256 (migration 0005).
func (s *Store) UploadDigests() port.UploadDigestStore { return uploadDigests{s} }

type uploadDigests struct{ s *Store }

func (u uploadDigests) Put(ctx context.Context, d blob.Digests) error {
	if d.SHA256 == "" || d.SHA1 == "" || d.MD5 == "" {
		return fmt.Errorf("put upload digests: %w", kernel.ErrInvalid)
	}
	// The SHA-256 names the content, so a row already there holds the
	// same SHA-1 and MD5, and two uploads of one object may race.
	_, err := u.s.Exec(
		ctx,
		"put upload digests",
		dialect.InsertIgnore(
			"upload_digests",
			[]string{"sha256", "sha1", "md5"},
			[]string{"sha256"},
		),
		d.SHA256,
		d.SHA1,
		d.MD5,
	)
	return err
}

func (u uploadDigests) Get(ctx context.Context, sha256 string) (blob.Digests, error) {
	out := blob.Digests{SHA256: sha256}
	err := u.s.Q(ctx).
		QueryRowContext(ctx, u.s.Dialect().Rebind("SELECT sha1, md5 FROM upload_digests WHERE sha256 = ?"), sha256).
		Scan(&out.SHA1, &out.MD5)
	if err != nil {
		return blob.Digests{}, u.s.Wrap("get upload digests", err)
	}
	return out, nil
}
