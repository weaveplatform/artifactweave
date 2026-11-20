package terraformhttp

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

var errFixtureBlobsClosed = errors.New("terraform fixture: blob writes closed")

type fixtureBlobs struct {
	blob.Store
	mu       sync.Mutex
	isClosed bool
	writes   sync.WaitGroup
}

// A cancelled request can return before its shared cache worker. Fence new
// filesystem mutations and finish admitted ones before TempDir removes the store.
func (b *fixtureBlobs) close() {
	b.mu.Lock()
	b.isClosed = true
	b.mu.Unlock()
	b.writes.Wait()
}

func (b *fixtureBlobs) enter() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.isClosed {
		return false
	}
	b.writes.Add(1)
	return true
}

func (b *fixtureBlobs) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if !b.enter() {
		return nil, errFixtureBlobsClosed
	}
	defer b.writes.Done()
	upload, err := b.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return fixtureUpload{Upload: upload, blobs: b}, nil
}

func (b *fixtureBlobs) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if !b.enter() {
		return nil, errFixtureBlobsClosed
	}
	defer b.writes.Done()
	upload, err := b.Store.ResumeUpload(ctx, id)
	if err != nil {
		return nil, err
	}
	return fixtureUpload{Upload: upload, blobs: b}, nil
}

func (b *fixtureBlobs) Delete(ctx context.Context, digest string) error {
	if !b.enter() {
		return errFixtureBlobsClosed
	}
	defer b.writes.Done()
	return b.Store.Delete(ctx, digest)
}

type fixtureUpload struct {
	blob.Upload
	blobs *fixtureBlobs
}

func (u fixtureUpload) Append(ctx context.Context, body io.Reader) (int64, error) {
	if !u.blobs.enter() {
		return 0, errFixtureBlobsClosed
	}
	defer u.blobs.writes.Done()
	return u.Upload.Append(ctx, body)
}

func (u fixtureUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if !u.blobs.enter() {
		return blob.Info{}, errFixtureBlobsClosed
	}
	defer u.blobs.writes.Done()
	return u.Upload.Commit(ctx, want)
}

func (u fixtureUpload) Abort(ctx context.Context) error {
	if !u.blobs.enter() {
		return errFixtureBlobsClosed
	}
	defer u.blobs.writes.Done()
	return u.Upload.Abort(ctx)
}
