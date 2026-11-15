package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// UploadDigests returns the upload digest store: the SHA-1 and MD5 an
// upload computed, by the content's SHA-256.
func (s *Store) UploadDigests() port.UploadDigestStore { return uploadDigests{s} }

type uploadDigests struct{ s *Store }

func (u uploadDigests) rows() *memtx.Table[string, blob.Digests] {
	return memtx.TableOf[string, blob.Digests](u.s.Store, "upload_digests")
}

func (u uploadDigests) Put(ctx context.Context, d blob.Digests) error {
	if d.SHA256 == "" || d.SHA1 == "" || d.MD5 == "" {
		return fmt.Errorf("put upload digests: %w", kernel.ErrInvalid)
	}
	return u.s.Do(ctx, func() error {
		if !u.rows().Has(d.SHA256) {
			u.rows().Put(d.SHA256, blob.Digests{SHA256: d.SHA256, SHA1: d.SHA1, MD5: d.MD5})
		}
		return nil
	})
}

func (u uploadDigests) Get(ctx context.Context, sha256 string) (blob.Digests, error) {
	var out blob.Digests
	err := u.s.Do(ctx, func() error {
		var ok bool
		out, ok = u.rows().Get(sha256)
		if !ok {
			return memtx.NotFound("upload digests", sha256)
		}
		return nil
	})
	return out, err
}
