package dialect

import (
	"context"
	"errors"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// storeFailure is a driver's error that Wrap marks as kernel.ErrStore, the
// store's own failure. It reads as the driver's error, so what the server
// logs does not change, and unwraps to both.
type storeFailure struct{ err error }

func (e storeFailure) Error() string { return e.err.Error() }

func (e storeFailure) Unwrap() []error { return []error{kernel.ErrStore, e.err} }

// markStore is err, a driver's error, marked as the store's own failure,
// unless the statement failed because its context ended: a request that
// ends is not a store that fails, and its caller answers as before. A
// remote cache that meets a marked error under a format's hook answers the
// server's 500, not the upstream's 502
// (https://github.com/weaveplatform/artifactweave/issues/1470).
func markStore(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return storeFailure{err}
}
