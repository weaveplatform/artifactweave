package pkgroute

import (
	"context"
	"fmt"
	"io"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
)

// ReadServed reads up to limit+1 bytes of body, the copy of a remote file
// that the source service served a route (source.Result's Stream), so the
// caller can tell a copy over limit. That copy is the server's own: its
// cached file, or the spool file of a repository that keeps none, which
// the source service filled from the upstream before it served it. So a
// failed read of it is the server's storage fault (format.ErrStorageFault),
// which SourceError and the kit answer 500 "internal error" and log, as
// the remote cache answers its own failed reads of that copy (#1269), and
// not the upstream's 502. A read that fails because the request ended
// returns otherwise, the route's own answer, as before. 7.x reads a cached
// file from its own storage once RemoteRepoBase.getResourceStreamHandle
// has it, without the upstream (7.161.26); the status it answers a
// storage failure is not documented [I].
func ReadServed(ctx context.Context, body io.Reader, limit int64, otherwise error) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err == nil {
		return data, nil
	}
	if ctx.Err() != nil {
		return nil, otherwise
	}
	return nil, ServedReadFault(ctx, err)
}

// ServedReadFault is err, a failed read of the copy the source service
// served a route or of the cached blob a route opened itself, marked as the
// server's storage fault (format.ErrStorageFault), unless ctx is done and
// the read failed because the request ended. SourceError then answers it
// 500 even when the store's error wraps a timeout, which would otherwise
// read as the upstream's 504 (#1462).
func ServedReadFault(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("read the served copy: %w: %w", format.ErrStorageFault, err)
}
