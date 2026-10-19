// Package trash restores, cleans and purges the trash can (decision 0010).
// Deleting an item moves its record here (internal/service/item), as does
// deleting a repository (Keep, internal/repos), and a trashed file keeps its
// blob reference, so it can be restored until its retention period passes. Purging an entry releases that reference, and the
// next garbage collection removes the content once nothing else holds it.
package trash

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// Repository is the key Artifactory shows the trash can under.
	Repository = "auto-trashcan"
	// DefaultRetentionDays is Artifactory's default trash can retention.
	DefaultRetentionDays = 14
	// DefaultBatch bounds the entries one transaction restores or purges,
	// so emptying a large trash can never holds one long transaction.
	DefaultBatch = 500
)

// ErrNothingTrashed is a restore from a path with no trashed items.
var ErrNothingTrashed = errors.New("trash: nothing is trashed at the path")

// ErrNotPermitted is a restore that selected an item its permit refused.
var ErrNotPermitted = errors.New("trash: restoring an item is not permitted")

// Settings returns the trash can settings. Unset means on with Artifactory's
// 14 days, and a stored retention below one day means the default too: the
// settings document may carry only "enabled".
func Settings(ctx context.Context, st store.Store) (types.TrashcanSettings, error) {
	t := types.TrashcanSettings{Enabled: true, RetentionDays: DefaultRetentionDays}
	err := st.Settings().Get(ctx, types.SettingTrashcan, &t)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return types.TrashcanSettings{Enabled: true, RetentionDays: DefaultRetentionDays}, nil
	case err != nil:
		return t, fmt.Errorf("trash: settings: %w", err)
	}
	if t.RetentionDays < 1 {
		t.RetentionDays = DefaultRetentionDays
	}
	return t, nil
}

// Service works on the trash can of one store.
type Service struct {
	Store store.Store
	// Items recreates restored items; only Restore needs it.
	Items *item.Service
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Batch bounds the entries per transaction; zero means DefaultBatch.
	Batch int
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) batch() int { return cmp.Or(s.Batch, DefaultBatch) }

// Entries returns every entry trashed from repoKey at or below prefix ("" for
// the whole repository; a repoKey of "" for the whole trash can).
func (s *Service) Entries(ctx context.Context, repoKey, prefix string) ([]types.TrashEntry, error) {
	var out []types.TrashEntry
	page := store.Page{Limit: store.MaxLimit}
	for {
		batch, next, err := s.Store.Trash().List(ctx, repoKey, strings.Trim(prefix, "/"), page)
		if err != nil {
			return nil, fmt.Errorf("trash: list %s/%s: %w", repoKey, prefix, err)
		}
		out = append(out, batch...)
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

// Keep records every item of c's own content as trashed by by, as a delete
// of the repository's root through the item service records them, so a
// deleted repository can be restored. It changes nothing else: call it
// inside the transaction that then deletes the items, which hands each
// file's blob reference to its entry instead of releasing it. It returns
// how many references the entries hold per sha256, for that delete to
// release only the ones no entry took over.
//
// It pages through the items rather than listing them at once, so a large
// repository costs one page of memory, not all of it.
func (s *Service) Keep(ctx context.Context, c repo.Config, by string) (map[string]int64, error) {
	held := map[string]int64{}
	now := s.now()
	page := store.Page{Limit: store.MaxLimit}
	for {
		batch, next, err := s.Store.Items().Descendants(ctx, c.Key, false, "", page)
		if err != nil {
			return nil, fmt.Errorf("trash: list %s: %w", c.Key, err)
		}
		for _, it := range batch {
			props, err := s.Store.Items().Properties(ctx, it.ID)
			if err != nil {
				return nil, fmt.Errorf("trash: properties of %s/%s: %w", c.Key, it.FullPath(), err)
			}
			if _, err := s.Store.Trash().Add(ctx, types.TrashEntry{
				Record: types.TrashRecord{
					Item: it, Properties: props, PackageType: string(c.PackageType),
				},
				DeletedAt: now, DeletedBy: by,
			}); err != nil {
				return nil, fmt.Errorf("trash: keep %s/%s: %w", c.Key, it.FullPath(), err)
			}
			// purge releases one reference per file entry with content, so
			// this counts exactly what it will release.
			if !it.Folder && it.Actual.SHA256 != "" {
				held[it.Actual.SHA256]++
			}
		}
		if next == "" {
			return held, nil
		}
		page.After = next
	}
}

// Newest keeps, for each original path, the most recently trashed entry,
// ordered so folders come before what they contain.
func Newest(entries []types.TrashEntry) []types.TrashEntry {
	newest := map[string]types.TrashEntry{}
	for _, e := range entries {
		k := e.Record.Item.RepoKey + "/" + e.Record.Item.FullPath()
		cur, ok := newest[k]
		if !ok || e.DeletedAt.After(cur.DeletedAt) ||
			(e.DeletedAt.Equal(cur.DeletedAt) && e.ID > cur.ID) {
			newest[k] = e
		}
	}
	out := make([]types.TrashEntry, 0, len(newest))
	for _, e := range newest {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b types.TrashEntry) int {
		return cmp.Or(
			cmp.Compare(depth(a.Record.Item.FullPath()), depth(b.Record.Item.FullPath())),
			strings.Compare(a.Record.Item.FullPath(), b.Record.Item.FullPath()),
		)
	})
	return out
}

func depth(p string) int { return strings.Count(p, "/") }

// Destination is where a restore puts items: a repository and a path. With
// no path the items return to their original paths inside Repo; with one,
// the restored path lands there and what was below it below that.
type Destination struct {
	Repo repo.Config
	Path string
}

// Restore moves the newest trashed copy of everything at or below
// repoKey/from back into dst and returns how many items it restored. Each
// batch is one transaction that recreates the items and drops their entries;
// a conflict stops the restore with the earlier batches kept.
//
// permit is asked about every selected item, at its original path, before
// any batch moves, so one refusal restores nothing and answers
// ErrNotPermitted. Asking per batch instead would leave the batches before
// the refused item restored. A nil permit allows every item, for a caller
// such as an administrator that may restore anything.
func (s *Service) Restore(
	ctx context.Context,
	repoKey, from string,
	dst Destination,
	by string,
	permit func(types.Item) bool,
) (int, error) {
	from = strings.Trim(from, "/")
	entries, err := s.Entries(ctx, repoKey, from)
	if err != nil {
		return 0, err
	}
	selected := Newest(entries)
	if len(selected) == 0 {
		return 0, ErrNothingTrashed
	}
	if permit != nil {
		for _, e := range selected {
			if !permit(e.Record.Item) {
				return 0, fmt.Errorf("%w: %s/%s",
					ErrNotPermitted, e.Record.Item.RepoKey, e.Record.Item.FullPath())
			}
		}
	}
	restored := 0
	for chunk := range slices.Chunk(selected, s.batch()) {
		err := s.Store.Tx(ctx, func(ctx context.Context) error {
			for _, e := range chunk {
				t := item.Target{
					Repo: dst.Repo,
					Path: destination(e.Record.Item.FullPath(), from, dst.Path),
				}
				if _, err := s.Items.Restore(ctx, t, e.Record, by); err != nil {
					return err //nolint:wrapcheck // the item service's typed errors
				}
				if err := s.Store.Trash().Delete(ctx, e.ID); err != nil {
					return fmt.Errorf("trash: drop restored entry %d: %w", e.ID, err)
				}
			}
			return nil
		})
		if err != nil {
			return restored, err //nolint:wrapcheck // the transaction returns ours
		}
		restored += len(chunk)
	}
	return restored, nil
}

// destination maps an original path below from onto the destination path.
func destination(original, from, to string) string {
	to = strings.Trim(to, "/")
	if to == "" {
		return original
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(original, from), "/")
	if from == "" {
		rel = original
	}
	return strings.Trim(path.Join(to, rel), "/")
}

// Clean purges every entry trashed from repoKey at or below prefix and
// returns how many it purged.
func (s *Service) Clean(ctx context.Context, repoKey, prefix string) (int, error) {
	entries, err := s.Entries(ctx, repoKey, prefix)
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, entries)
}

// Empty purges the whole trash can and returns how many entries it purged.
func (s *Service) Empty(ctx context.Context) (int, error) {
	total := 0
	for {
		entries, _, err := s.Store.Trash().List(ctx, "", "", store.Page{Limit: s.batch()})
		if err != nil {
			return total, fmt.Errorf("trash: list: %w", err)
		}
		n, err := s.purge(ctx, entries)
		total += n
		// A batch that purged nothing was emptied concurrently; stop rather
		// than spin on entries another pass is removing.
		if err != nil || len(entries) < s.batch() || n == 0 {
			return total, err
		}
	}
}

// PurgeExpired purges the entries older than the retention period and
// returns how many it purged. It purges nothing while the trash can is off,
// as 7.x's small garbage collection, which expires the trash can, does not
// run then (KB "The complete Garbage Collection Guide"; 6.20's
// TrashServiceImpl.getGCCandidatesFromTrash). What was trashed before the
// trash can was turned off stays until it is turned on again and the entry
// expires, or until clean or empty removes it.
func (s *Service) PurgeExpired(ctx context.Context) (int, error) {
	cfg, err := Settings(ctx, s.Store)
	if err != nil || !cfg.Enabled {
		return 0, err
	}
	before := s.now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	total := 0
	for {
		entries, err := s.Store.Trash().Expired(ctx, before, s.batch())
		if err != nil {
			return total, fmt.Errorf("trash: expired: %w", err)
		}
		n, err := s.purge(ctx, entries)
		total += n
		if err != nil || len(entries) < s.batch() || n == 0 {
			return total, err
		}
	}
}

// purge drops entries and releases the blob references their files held,
// one transaction per batch. An entry already gone was purged concurrently;
// its reference was released by whoever removed it, so it is skipped here.
func (s *Service) purge(ctx context.Context, entries []types.TrashEntry) (int, error) {
	purged := 0
	for chunk := range slices.Chunk(entries, s.batch()) {
		n := 0
		err := s.Store.Tx(ctx, func(ctx context.Context) error {
			n = 0
			for _, e := range chunk {
				err := s.Store.Trash().Delete(ctx, e.ID)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return fmt.Errorf("trash: purge entry %d: %w", e.ID, err)
				}
				n++
				it := e.Record.Item
				if it.Folder || it.Actual.SHA256 == "" {
					continue
				}
				_, err = s.Store.Blobs().Unref(ctx, it.Actual.SHA256, 1)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("trash: release blob %s: %w", it.Actual.SHA256, err)
				}
			}
			return nil
		})
		if err != nil {
			return purged, err //nolint:wrapcheck // the transaction returns ours
		}
		purged += n
	}
	return purged, nil
}

// Child is one entry of a trash can folder listing.
type Child struct {
	Name   string
	Folder bool
}

// Children lists what the trash can shows directly inside dir: the
// repositories with trashed items at its root ("" for dir), and below a
// repository the next path segment of every trashed path, as Artifactory's
// auto-trashcan repository does. sees filters as for Below.
func (s *Service) Children(
	ctx context.Context,
	dir string,
	sees func(types.Item) bool,
) ([]Child, error) {
	nodes, err := s.Below(ctx, dir, false, sees)
	if err != nil {
		return nil, err
	}
	out := make([]Child, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, Child{Name: path.Base(n.Path), Folder: n.Folder})
	}
	return out, nil
}

// Node is one item of the trash can's tree, as Artifactory's auto-trashcan
// repository holds it: Path is the original repository key and the item's
// original path, joined.
type Node struct {
	Path   string
	Folder bool
	// Entry is the newest entry trashed at Path. It is nil for a folder that
	// only holds what was trashed below it, such as a repository's folder at
	// the root, or the parent of a file deleted on its own.
	Entry *types.TrashEntry
}

// Below lists the trash can's tree under dir ("" for its root): the direct
// children, or with deep every descendant, sorted by path.
//
// sees filters the entries before the tree is derived from them, so a
// repository or folder shows only when an entry the caller sees lies at or
// below it. A nil sees shows every entry, for an administrator.
func (s *Service) Below(
	ctx context.Context,
	dir string,
	deep bool,
	sees func(types.Item) bool,
) ([]Node, error) {
	dir = strings.Trim(dir, "/")
	repoKey, prefix, _ := strings.Cut(dir, "/")
	entries, err := s.Entries(ctx, repoKey, prefix)
	if err != nil {
		return nil, err
	}
	visible := make([]types.TrashEntry, 0, len(entries))
	for _, e := range entries {
		if sees == nil || sees(e.Record.Item) {
			visible = append(visible, e)
		}
	}
	nodes := map[string]*Node{}
	node := func(p string) *Node {
		n, ok := nodes[p]
		if !ok {
			n = &Node{Path: p}
			nodes[p] = n
		}
		return n
	}
	for _, e := range Newest(visible) {
		rel := strings.Trim(e.Record.Item.RepoKey+"/"+e.Record.Item.FullPath(), "/")
		if dir != "" {
			cut, ok := strings.CutPrefix(rel, dir+"/")
			if !ok {
				continue
			}
			rel = cut
		}
		segs := strings.Split(rel, "/")
		// Every segment above the entry is a folder of the tree, the
		// repository's among them.
		for i := 1; i < len(segs) && (deep || i == 1); i++ {
			node(path.Join(dir, path.Join(segs[:i]...))).Folder = true
		}
		if !deep && len(segs) > 1 {
			continue
		}
		n := node(path.Join(dir, rel))
		n.Entry, n.Folder = &e, n.Folder || e.Record.Item.Folder
	}
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		// A file trashed where a folder later held trashed items is gone from
		// Artifactory's trash can, which replaces it with the folder
		// (TrashServiceImpl.undeployFileWithSameNameInTrash in 6.20).
		if n.Folder && n.Entry != nil && !n.Entry.Record.Item.Folder {
			n.Entry = nil
		}
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b Node) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// Find returns the newest entry trashed from repoKey at exactly fullPath.
func (s *Service) Find(ctx context.Context, repoKey, fullPath string) (types.TrashEntry, error) {
	entries, err := s.Store.Trash().Find(ctx, repoKey, strings.Trim(fullPath, "/"))
	if err != nil {
		return types.TrashEntry{}, fmt.Errorf("trash: find %s/%s: %w", repoKey, fullPath, err)
	}
	if len(entries) == 0 {
		return types.TrashEntry{}, store.ErrNotFound
	}
	return entries[0], nil
}
