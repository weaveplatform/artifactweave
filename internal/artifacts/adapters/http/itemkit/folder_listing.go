package itemkit

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// storedFolderChildren keeps presence separate from visible child count. The
// repository root and an empty stored folder are present; a file is not a folder.
func storedFolderChildren(ctx context.Context, st Store, key string, cached bool, folder string) ([]model.Item, bool, error) {
	if folder != "" {
		it, err := st.Items().Get(ctx, key, cached, folder)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && !it.Folder {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("folder in %s: %w", key, err)
		}
	}
	var children []model.Item
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		batch, next, err := st.Items().Children(ctx, key, cached, folder, page)
		if err != nil {
			return nil, false, fmt.Errorf("children in %s: %w", key, err)
		}
		children = append(children, batch...)
		if next == "" {
			return children, true, nil
		}
		page.After = next
	}
}

type indexOwnership int

const (
	firstIndexKind indexOwnership = iota
	folderIndexKind
)

// indexEntries shares name merging and ordering without making direct cache
// ownership inherit the virtual index's deliberate folder-dominance rule.
type indexEntries struct {
	kinds     map[string]bool
	ownership indexOwnership
}

func (e *indexEntries) add(fullPath string, folder bool) {
	if e.kinds == nil {
		e.kinds = map[string]bool{}
	}
	name := path.Base(fullPath)
	if _, exists := e.kinds[name]; !exists || e.ownership == folderIndexKind && folder {
		e.kinds[name] = folder
	}
}

func (e *indexEntries) entries() []indexEntry {
	entries := make([]indexEntry, 0, len(e.kinds))
	for name, folder := range e.kinds {
		entries = append(entries, indexEntry{name: name, folder: folder})
	}
	slices.SortFunc(entries, func(a, b indexEntry) int { return strings.Compare(a.name, b.name) })
	return entries
}
