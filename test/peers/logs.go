package peers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// publishLog keeps raw logs private: only the sanitizer's completed answer
// reaches the artifact directory or the failure printer. Error details may
// themselves contain secrets, so callers receive only fixed messages.
func publishLog(path, dir string, sanitize func([]byte) ([]byte, error), print func([]byte)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("server log could not be read for publication")
	}
	clean, err := sanitize(data)
	if err != nil {
		return errors.New("server log could not be sanitized for publication")
	}
	if dir != "" {
		if err := exportLog(dir, filepath.Base(path), clean); err != nil {
			return errors.New("sanitized server log could not be exported")
		}
	}
	if print != nil {
		print(clean)
	}
	return nil
}

// exportLog publishes a complete sanitized file by rename. Temporary files
// contain sanitized bytes too; the raw input never enters this directory.
func exportLog(dir, name string, data []byte) (err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return err
	}
	pending := true
	defer func() {
		if pending {
			if removeErr := os.Remove(f.Name()); removeErr != nil {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	pending = false
	return nil
}

func logTailBytes(data []byte, limit int) string {
	if len(data) > limit {
		return fmt.Sprintf("[first %d bytes omitted]\n%s", len(data)-limit, data[len(data)-limit:])
	}
	return string(data)
}
