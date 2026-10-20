// Package testfault holds the failing fakes that handler tests share
// (decision 0015), so the error paths of streaming handlers are covered
// without real failures: a client that goes away mid-stream, and a blob
// store whose writes fail.
package testfault

import (
	"context"
	"fmt"
	"io"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// ErrDisconnected is what a Disconnect body fails with. It wraps
// io.ErrUnexpectedEOF, which net/http's body reports when the client closes
// the connection before the length it declared.
var ErrDisconnected = fmt.Errorf("testfault: client disconnected: %w", io.ErrUnexpectedEOF)

// Disconnect returns body cut off after n bytes: it reads as body does
// until it has given n bytes, then fails every read with ErrDisconnected,
// as a request body does when its client goes away mid-stream. A body
// shorter than n ends as it would.
func Disconnect(body io.Reader, n int64) io.Reader {
	return &cut{r: body, left: n}
}

type cut struct {
	r    io.Reader
	left int64
}

func (c *cut) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrDisconnected
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err //nolint:wrapcheck // the body's own error, which io.Copy compares with io.EOF
}

// Blobs is a blob store whose uploads fail: Begin fails BeginUpload, Append
// fails every Append once the upload has stored what it read, a write that
// fails part way through, and Commit fails Commit. A nil error passes the
// call to Store.
type Blobs struct {
	blob.Store
	Begin, Append, Commit error
}

// BeginUpload starts an upload on Store whose Append and Commit fail as b
// says, or fails with Begin.
func (b Blobs) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if b.Begin != nil {
		return nil, b.Begin
	}
	u, err := b.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store's own error
	}
	return upload{Upload: u, b: b}, nil
}

type upload struct {
	blob.Upload
	b Blobs
}

func (u upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	n, err := u.Upload.Append(ctx, r)
	if err == nil && u.b.Append != nil {
		err = u.b.Append
	}
	return n, err //nolint:wrapcheck // the store's own error, or the one asked for
}

func (u upload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if u.b.Commit != nil {
		_ = u.Abort(ctx)
		return blob.Info{}, u.b.Commit
	}
	return u.Upload.Commit(ctx, want) //nolint:wrapcheck // the store's own error
}
