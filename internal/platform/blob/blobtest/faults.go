package blobtest

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// ErrDropped is the error the reads of a blob that ReadFaults cut fail
// with when its Err is nil.
var ErrDropped = errors.New("blobtest: the store dropped the connection")

// ReadFaults is Store whose reads of a blob fail, as a store whose
// connection drops after it opened the blob does. Once Cut names a blob, or
// CutEvery every blob, the first spare opens of one read soundly and every
// later one opens, but each Read of it fails with Err, or ErrDropped. Counting
// opens, not reads, lets a test reach a later reader of a blob: the remote
// cache's check of a cached copy opens it, and a route's read of the copy
// the cache serves opens it again. Mend lifts the cut. It is safe for
// concurrent use.
type ReadFaults struct {
	blob.Store
	// Err is the error a cut blob's reads fail with; nil means ErrDropped.
	// A store whose read times out wraps context.DeadlineExceeded.
	Err   error
	mu    sync.Mutex
	cut   bool
	sha   string
	spare int
}

// Cut makes the opens of sha after the next spare ones fail their reads,
// in place of any earlier cut.
func (b *ReadFaults) Cut(sha string, spare int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cut, b.sha, b.spare = true, sha, spare
}

// CutEvery makes every open after the next spare ones, of any blob, fail
// its reads, in place of any earlier cut.
func (b *ReadFaults) CutEvery(spare int) { b.Cut("", spare) }

// Mend lifts the cut: every open reads soundly again.
func (b *ReadFaults) Mend() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cut = false
}

// Open opens sha in Store, whose reads fail when the cut covers sha and its
// spare opens are spent.
func (b *ReadFaults) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return r, info, err //nolint:wrapcheck // the store's own error
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cut || b.sha != "" && sha != b.sha {
		return r, info, nil
	}
	if b.spare > 0 {
		b.spare--
		return r, info, nil
	}
	if b.Err != nil {
		return droppedReads{r, b.Err}, info, nil
	}
	return droppedReads{r, ErrDropped}, info, nil
}

// droppedReads is an opened blob whose reads fail with err; it seeks and
// closes as the store's own reader does.
type droppedReads struct {
	io.ReadSeekCloser
	err error
}

func (d droppedReads) Read([]byte) (int, error) { return 0, d.err }
