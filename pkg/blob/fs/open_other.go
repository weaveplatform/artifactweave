//go:build !windows

package fs

import (
	"context"
	"os"
)

func openBlobFile(_ context.Context, path string) (*os.File, error) {
	return os.Open(path)
}
