package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// publish moves the complete upload without replacing a concurrently published
// blob. MoveFileEx with REPLACE_EXISTING can deny a concurrent Open access to the
// destination; adopting the winner after a failed replacement is then too late.
// Omitting that flag preserves the existing file and works without hard links.
// Upload state files still use os.Rename because their contents must be replaced.
func publish(from, to string) error {
	oldpath, err := publicationPath(from)
	if err == nil {
		var newpath *uint16
		newpath, err = publicationPath(to)
		if err == nil {
			err = windows.MoveFileEx(oldpath, newpath, 0)
		}
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// The native call needs the long-path handling os.Rename normally supplies.
// Resolve relative roots before adding the extended prefix; retain device and
// already-extended paths, and use the UNC spelling for network shares.
func publicationPath(path string) (*uint16, error) {
	abs := path
	if !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\\.\`) &&
		!strings.HasPrefix(path, `\??\`) {
		var err error
		abs, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("blob/fs: absolute publication path: %w", err)
		}
		if len(abs) >= 248 {
			if strings.HasPrefix(abs, `\\`) {
				abs = `\\?\UNC\` + abs[2:]
			} else {
				abs = `\\?\` + abs
			}
		}
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return nil, fmt.Errorf("blob/fs: publication path: %w", err)
	}
	return p, nil
}
