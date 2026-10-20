package item

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Restore recreates a trashed item at t.Path with its recorded content,
// times and properties, creating missing parent folders, and runs the
// format's hooks as a deploy does. Call it inside a transaction that also
// removes the trash entry: a trashed file still holds its blob reference,
// which the restored item takes over, so no reference is taken here. A file
// it replaces releases its own reference, as an overwrite by deploy does; a
// folder already at the path keeps its content and gains the recorded
// properties. A folder in the way of a file, or a file in the way of a
// folder, is ErrConflict. Every restored item also carries
// PropTrashOriginalRepository and PropTrashRestoredTime.
func (s *Service) Restore(
	ctx context.Context,
	t Target,
	rec types.TrashRecord,
	by string,
) (types.Item, error) {
	full := strings.Trim(t.Path, "/")
	if full == "" {
		return types.Item{}, fmt.Errorf("%w: restore needs a path", types.ErrInvalidItemPath)
	}
	now := s.now()
	rec.Properties = restoredProperties(rec, now)
	if err := s.ensureFolders(ctx, t, path.Dir(full), by, now); err != nil {
		return types.Item{}, err
	}
	items := s.Store.Items()
	old, err := items.Get(ctx, t.Repo.Key, t.Cached, full)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return types.Item{}, fmt.Errorf("item: get %s: %w", full, err)
	}
	if exists && old.Folder != rec.Item.Folder {
		return types.Item{}, fmt.Errorf("%w: %s is in the way", ErrConflict, full)
	}
	if exists && old.Folder {
		// A folder recreated since the delete keeps its content and its own
		// properties; the recorded ones, and the restore's own, are added.
		if err := s.annotate(ctx, t.Repo, old, rec.Properties, nil, false); err != nil {
			return types.Item{}, err
		}
		return old, nil
	}
	it := rec.Item
	it.ID, it.RepoKey, it.Cached, it.Path, it.Name = 0, t.Repo.Key, t.Cached, "", full
	stored, err := items.Put(ctx, it)
	if err != nil {
		return types.Item{}, fmt.Errorf("item: put %s: %w", full, err)
	}
	if exists && old.Actual.SHA256 != "" {
		if _, err := s.Store.Blobs().Unref(ctx, old.Actual.SHA256, 1); err != nil {
			return types.Item{}, fmt.Errorf("item: release replaced blob: %w", err)
		}
	}
	if err := items.SetProperties(ctx, stored.ID, rec.Properties, now); err != nil {
		return types.Item{}, fmt.Errorf("item: properties of %s: %w", full, err)
	}
	hooks, env := s.format(t.Repo.PackageType), s.env()
	if !stored.Folder {
		if err := hooks.Deployed(ctx, env, t.Repo, stored); err != nil {
			return types.Item{}, err //nolint:wrapcheck // the format's own error
		}
	}
	if err := s.annotated(ctx, hooks, env, t.Repo, stored,
		format.Annotation{Set: rec.Properties, At: now}); err != nil {
		return types.Item{}, err
	}
	return stored, nil
}

// The properties a restore sets on every item it brings back. 7.x lists both
// on restored items (RTFACT-29810); their values are 6.20's: the key of the
// repository the item was deleted from, and the restore's time in Unix
// milliseconds (TrashService, TrashInterceptor.afterMove).
const (
	PropTrashOriginalRepository = "trash.originalRepository"
	PropTrashRestoredTime       = "trash.restoredTime"
)

// restoredProperties is rec's properties with the restore's own two. It is
// a copy: rec's map belongs to the trash entry the caller holds.
func restoredProperties(rec types.TrashRecord, at time.Time) types.Properties {
	props := make(types.Properties, len(rec.Properties)+2)
	maps.Copy(props, rec.Properties)
	// A restored item deleted and restored again keeps one restore time, the
	// latest, rather than gathering one value per restore.
	props[PropTrashOriginalRepository] = []string{rec.Item.RepoKey}
	props[PropTrashRestoredTime] = []string{strconv.FormatInt(at.UnixMilli(), 10)}
	return props
}
