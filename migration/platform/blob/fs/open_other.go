//go:build !windows

package fs

import (
	"context"
	"os"
)

func openBlobFile(_ context.Context, path string) (*os.File, error) {
	return os.Open(path) //nolint:wrapcheck // stays os.Open's *PathError; notFound wraps it
}
