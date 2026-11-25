package federated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

var (
	_ streamsync.SnapshotProvider = Stream{}
	_ streamsync.Reconciler       = Stream{}
)

// SnapshotItems lists the repository's items as this instance holds them
// for a full sync (decision 0021, Full sync): each file and folder with its
// whole state and the stamp of the change that left it so, and the
// tombstones of deleted items, in byte order of path. An item no change of
// the stream has stamped, such as one stored before the repository was
// federated, is stamped with its modification time and this instance, so
// the conflict rule orders it against the other members' changes by when it
// was written. Creation times travel only when propagateCreatedTime is on
// (SettingFederationPropagateCreatedTime); otherwise the receiver gives
// each item it creates the time it receives it, as 7.x's full sync does.
func (s Stream) SnapshotItems(ctx context.Context, self, stream, after string, limit int) ([]model.SnapshotEntry, error) {
	k, ok := key(stream)
	if !ok {
		return nil, nil
	}
	created, err := PropagatesCreatedTime(ctx, s.Store.Settings())
	if err != nil {
		return nil, err
	}
	return s.list(ctx, self, k, after, limit, true, created)
}

// LocalItems lists the repository's items here for the diff, as
// SnapshotItems does without their state: a repository's stream is one
// item set whichever member made each change, so origin does not narrow it.
func (s Stream) LocalItems(ctx context.Context, _, stream, after string, limit int) ([]model.SnapshotEntry, error) {
	k, ok := key(stream)
	if !ok {
		return nil, nil
	}
	self, err := s.Engine.Self(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // the engine names what failed
	}
	return s.list(ctx, self, k, after, limit, false, false)
}

// list lists up to limit entries after after, and exactly limit while
// more follow: the engine reads a shorter page as the last, so the paths
// that list nothing (a file without content, a stamp of an item not here)
// must not shorten one. It merges windows of the items and the stamps
// until it has limit entries or both run out.
func (s Stream) list(ctx context.Context, self, k, after string, limit int, state, created bool) ([]model.SnapshotEntry, error) {
	out := make([]model.SnapshotEntry, 0, limit)
	for {
		page, horizon, more, err := s.window(ctx, self, k, after, limit, state, created)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(out) >= limit {
			// The caller continues after the last entry returned, so what
			// is cut here is read again.
			return out[:limit], nil
		}
		if !more {
			return out, nil
		}
		after = horizon
	}
}

// window merges a page of the repository's items with a page of its
// stamps, both in byte order of path. Past the last path of a full page
// the other list may hold paths the full one has not reached, so the
// merge stops there, at horizon, and more reports that paths may follow.
func (s Stream) window(
	ctx context.Context, self, k, after string, limit int, state, created bool,
) (out []model.SnapshotEntry, horizon string, more bool, err error) {
	items, err := s.Items.List(ctx, k, after, limit, state)
	if errors.Is(err, kernel.ErrNotFound) {
		// Not federated here (any more): nothing to list or keep.
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("items of %s: %w", k, err)
	}
	stamps, err := s.Store.Stamps().List(ctx, k, after, limit)
	if err != nil {
		return nil, "", false, fmt.Errorf("stamps of %s: %w", k, err)
	}
	if len(items) == limit {
		horizon, more = items[limit-1].Path, true
	}
	if len(stamps) == limit && (!more || stamps[limit-1].Path < horizon) {
		horizon, more = stamps[limit-1].Path, true
	}
	for len(items) > 0 || len(stamps) > 0 {
		var (
			it    *artifactsapi.ItemChange
			stamp *model.ItemStamp
		)
		switch {
		case len(stamps) == 0 || len(items) > 0 && items[0].Path < stamps[0].Path:
			it, items = &items[0], items[1:]
		case len(items) == 0 || stamps[0].Path < items[0].Path:
			stamp, stamps = &stamps[0], stamps[1:]
		default:
			it, stamp, items, stamps = &items[0], &stamps[0], items[1:], stamps[1:]
		}
		p := path(it, stamp)
		if more && p > horizon {
			break
		}
		en, ok, err := entryOf(self, it, stamp, state, created)
		if err != nil {
			return nil, "", false, fmt.Errorf("%s:%s: %w", k, p, err)
		}
		if ok {
			out = append(out, en)
		}
	}
	return out, horizon, more, nil
}

func path(it *artifactsapi.ItemChange, stamp *model.ItemStamp) string {
	if it != nil {
		return it.Path
	}
	return stamp.Path
}

// entryOf is one path's snapshot entry: a change still waiting for its
// blob is the item's state, as it is what the conflict rule chose; then
// the item as it stands, under its stamp unless that is a tombstone,
// which a folder kept for what was written in it since outlives; then a
// tombstone. A stamp of a live item that is not here, which a skipped
// apply leaves, lists nothing; nor does a file without content.
func entryOf(self string, it *artifactsapi.ItemChange, stamp *model.ItemStamp, state, created bool) (model.SnapshotEntry, bool, error) {
	switch {
	case stamp != nil && len(stamp.Pending) > 0:
		en := model.SnapshotEntry{
			Key: stamp.Path, Time: stamp.Time, Origin: stamp.Origin, BlobSHA256: stamp.SHA256, BlobSize: stamp.Size,
		}
		if state {
			var p payload
			if err := json.Unmarshal(stamp.Pending, &p); err != nil {
				return model.SnapshotEntry{}, false, fmt.Errorf("pending change: %w", err)
			}
			b, err := encode(p, created)
			if err != nil {
				return model.SnapshotEntry{}, false, err
			}
			en.Payload = b
		}
		return en, true, nil
	case it != nil:
		if !it.Folder && it.Actual.SHA256 == "" {
			return model.SnapshotEntry{}, false, nil
		}
		en := model.SnapshotEntry{Key: it.Path, Time: outbox.HLC{Wall: it.Modified.UnixMilli()}, Origin: self}
		if stamp != nil && !stamp.Deleted {
			en.Time, en.Origin = stamp.Time, stamp.Origin
		}
		if !it.Folder {
			en.BlobSHA256, en.BlobSize = it.Actual.SHA256, it.Size
		}
		if state {
			b, err := encode(payload{
				Path: it.Path, Folder: it.Folder, Actual: it.Actual, Original: it.Original,
				Created: it.Created, CreatedBy: it.CreatedBy, Modified: it.Modified, ModifiedBy: it.ModifiedBy,
				Properties: it.Properties,
			}, created)
			if err != nil {
				return model.SnapshotEntry{}, false, err
			}
			en.Payload = b
		}
		return en, true, nil
	case stamp.Deleted:
		return model.SnapshotEntry{Key: stamp.Path, Time: stamp.Time, Origin: stamp.Origin, Deleted: true}, true, nil
	}
	return model.SnapshotEntry{}, false, nil
}

// encode writes a snapshot payload, without its creation time unless
// created.
func encode(p payload, created bool) ([]byte, error) {
	if !created {
		p.Created = time.Time{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", p.Path, err)
	}
	return b, nil
}

// Reconcile makes the item at en.Key what the snapshot entry says and
// records its stamp, in the full sync's transaction. The engine has
// decided that the entry wins, by the conflict rule or because the run is
// forced; the stamp is read again here, so a local write committed since
// rolls the page back to be decided again. A tombstone says nothing of
// the item's kind, so a file goes, or a folder once nothing is left in
// it. A file whose content did not arrive waits for it, as Apply's does.
// An item whose payload has no creation time, as a member without
// propagateCreatedTime sends it, is created now.
func (s Stream) Reconcile(ctx context.Context, _, stream string, en model.SnapshotEntry, held bool) error {
	k, ok := key(stream)
	if !ok {
		return nil
	}
	var prev *model.Stamp
	cur, err := s.Store.Stamps().Stamp(ctx, k, en.Key)
	switch {
	case err == nil:
		prev = &cur.Stamp
	case !errors.Is(err, kernel.ErrNotFound):
		return fmt.Errorf("stamp of %s:%s: %w", k, en.Key, err)
	}
	next := model.ItemStamp{
		RepoKey: k, Path: en.Key, Stamp: en.Stamp(), Deleted: en.Deleted, SHA256: en.BlobSHA256, Size: en.BlobSize,
	}
	if en.Deleted {
		next.Folder = cur.Folder
		err = s.Items.Delete(ctx, k, en.Key, false)
		if err == nil {
			err = s.Items.Delete(ctx, k, en.Key, true)
		}
	} else {
		var p payload
		if err := json.Unmarshal(en.Payload, &p); err != nil {
			s.log().WarnContext(ctx, "federated repository snapshot entry skipped: unreadable payload",
				"repository", k, "path", en.Key, "origin", en.Origin)
			return nil
		}
		p.Path, next.Folder = en.Key, p.Folder
		if p.Created.IsZero() {
			p.Created = s.now()
		}
		if !p.Folder && !held {
			if next.Pending, err = json.Marshal(p); err != nil {
				return fmt.Errorf("encode %s:%s: %w", k, en.Key, err)
			}
		} else {
			err = s.Items.Put(ctx, change(k, p, en.BlobSHA256, en.BlobSize))
		}
	}
	if skipped(err) {
		s.log().WarnContext(ctx, "federated repository snapshot entry skipped",
			"repository", k, "path", en.Key, "origin", en.Origin, "error", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("reconcile %s:%s: %w", k, en.Key, err)
	}
	return s.replace(ctx, prev, next)
}

// Discard removes the item at key, a folder with everything below it, and
// their stamps, tombstones included: a forced full sync found no entry for
// it in the snapshot, so nothing below it is in the snapshot either.
func (s Stream) Discard(ctx context.Context, _, stream, path string) error {
	k, ok := key(stream)
	if !ok {
		return nil
	}
	err := s.Items.Discard(ctx, k, path)
	if errors.Is(err, kernel.ErrNotFound) {
		// Not federated here any more: the stamps are cleared with it.
		return nil
	}
	if err != nil {
		return fmt.Errorf("discard %s:%s: %w", k, path, err)
	}
	if err := s.Store.Stamps().Remove(ctx, k, path, true); err != nil {
		return fmt.Errorf("discard the stamps of %s:%s: %w", k, path, err)
	}
	return nil
}

func (s Stream) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// PropagatesCreatedTime reports whether full sync carries original
// creation times (PUT federation/propagateCreatedTime); off until it is
// set, as 7.x's is.
func PropagatesCreatedTime(ctx context.Context, st settings.SettingsStore) (bool, error) {
	var on bool
	err := st.Get(ctx, settings.SettingFederationPropagateCreatedTime, &on)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return false, fmt.Errorf("read %s: %w", settings.SettingFederationPropagateCreatedTime, err)
	}
	return on, nil
}
