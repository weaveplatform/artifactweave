package peers

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A parent process owns its children's collection. Children emit identical
// metadata, which Windows cannot replace while another writer holds it open.
var coverageMu sync.Mutex

// collectCoverage publishes only completed child files. The runtime's metadata
// temporary names use the clock, so concurrent children must never emit into
// the same directory. Counter names include the child PID and stay separate;
// covdata, rather than this collector, combines their counts.
func collectCoverage(parent, child string) error {
	coverageMu.Lock()
	defer coverageMu.Unlock()
	entries, err := os.ReadDir(child)
	if err != nil {
		return fmt.Errorf("read child coverage: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		metadata := strings.HasPrefix(name, "covmeta.")
		if entry.IsDir() || (!metadata && !strings.HasPrefix(name, "covcounters.")) {
			return fmt.Errorf("unexpected child coverage entry %q", name)
		}
		source, target := filepath.Join(child, name), filepath.Join(parent, name)
		existing, err := os.ReadFile(target)
		switch {
		case err == nil:
			if !metadata {
				return fmt.Errorf("child coverage counter already exists: %s", target)
			}
			data, err := os.ReadFile(source)
			if err != nil {
				return fmt.Errorf("read child metadata: %w", err)
			}
			if !bytes.Equal(existing, data) {
				return fmt.Errorf("child coverage metadata differs: %s", name)
			}
			if err := os.Remove(source); err != nil {
				return fmt.Errorf("remove collected child metadata: %w", err)
			}
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("read collected coverage: %w", err)
		default:
			if err := os.Rename(source, target); err != nil {
				return fmt.Errorf("collect child coverage: %w", err)
			}
		}
	}
	if err := os.Remove(child); err != nil {
		return fmt.Errorf("remove collected child coverage directory: %w", err)
	}
	return nil
}
