package fs

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

var osOpenPublished = os.Open

// A concurrent MoveFileEx publication can briefly deny a reader access to an
// existing immutable blob on Windows. Retry only that transient sharing error;
// a persistent lock and every other read failure still reach the caller.
func openBlobFile(ctx context.Context, path string) (*os.File, error) {
	deadline := time.Now().Add(time.Second)
	for {
		f, err := osOpenPublished(path)
		if err == nil || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
			time.Now().After(deadline) {
			return f, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
