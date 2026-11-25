package item

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

type federatedKey struct{}

// federated marks a context that applies another member's change: nothing
// the item service does under it is reported to the feed, the hooks' own
// calls into an item service included.
func federated(ctx context.Context) context.Context {
	return context.WithValue(ctx, federatedKey{}, true)
}

func isFederated(ctx context.Context) bool {
	v, _ := ctx.Value(federatedKey{}).(bool)
	return v
}

// feed reports it, as it is after a change, to the store's ItemFeed. A
// remote cache, a repository root and a change applied from another member
// are not reported (artapi.ItemFeed).
func (s *Service) feed(ctx context.Context, it model.Item) error {
	if s.Store == nil || it.Cached || it.IsRoot() || isFederated(ctx) {
		return nil
	}
	f := s.Store.ItemFeed()
	if f == nil {
		return nil
	}
	props, err := s.propertySnapshot(ctx, it)
	if err != nil {
		return err
	}
	c := change(it)
	c.Properties = props
	if err := f.ItemChanged(ctx, c); err != nil {
		return fmt.Errorf("item: feed %s:%s: %w", it.RepoKey, it.FullPath(), err)
	}
	return nil
}

// feedRemoved reports each removed item, the files first and then the
// folders deepest first, so a member that applies them in this order finds
// each folder empty when its turn comes.
func (s *Service) feedRemoved(ctx context.Context, removed []model.Item) error {
	if s.Store == nil || isFederated(ctx) || len(removed) == 0 {
		return nil
	}
	f := s.Store.ItemFeed()
	if f == nil {
		return nil
	}
	ordered := slices.Clone(removed)
	slices.SortStableFunc(ordered, func(a, b model.Item) int {
		if a.Folder != b.Folder {
			if a.Folder {
				return 1
			}
			return -1
		}
		return cmp.Compare(strings.Count(b.FullPath(), "/"), strings.Count(a.FullPath(), "/"))
	})
	for _, it := range ordered {
		if it.Cached || it.IsRoot() {
			continue
		}
		c := change(it)
		c.Deleted = true
		if err := f.ItemChanged(ctx, c); err != nil {
			return fmt.Errorf("item: feed removal of %s:%s: %w", it.RepoKey, it.FullPath(), err)
		}
	}
	return nil
}

func change(it model.Item) artapi.ItemChange {
	return artapi.ItemChange{
		Repository: it.RepoKey, Path: it.FullPath(), Folder: it.Folder, Size: it.Size,
		Actual: it.Actual, Original: it.Original, Created: it.Created, CreatedBy: it.CreatedBy,
		Modified: it.Modified, ModifiedBy: it.ModifiedBy,
	}
}

// ApplyFederated makes the file or folder at c.Path of repository cfg
// what another member's change left it (artapi.FederatedItems.Put), in
// ctx's transaction or one of its own. Unlike a deploy it asks the format
// nothing: the member that made the change admitted it, and asking again
// could rename or refuse the path, which would leave the members
// different. It runs the hooks a deploy runs, so the format recomputes its
// own metadata and index here.
func (s *Service) ApplyFederated(ctx context.Context, cfg repo.Config, c artapi.ItemChange) error {
	ctx = federated(ctx)
	full := strings.Trim(c.Path, "/")
	if full == "" {
		return fmt.Errorf("%w: a federated change needs a path", kernel.ErrInvalidItemPath)
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		ctx = artifactevents.WithActor(ctx, c.ModifiedBy)
		t := Target{Repo: cfg, Path: full}
		now := s.now()
		if c.Folder {
			return s.applyFolder(ctx, t, c)
		}
		if err := s.ensureFolders(ctx, t, path.Dir(full), c.ModifiedBy, now); err != nil {
			return conflict(err)
		}
		items := s.Store.Items()
		old, err := items.Get(ctx, cfg.Key, false, full)
		switch {
		case err == nil && old.Folder:
			return conflict(fmt.Errorf("%w: %s is a folder", ErrConflict, full))
		case err == nil && old.Actual.SHA256 == c.Actual.SHA256:
			return s.applySameContent(ctx, t, old, c)
		case err != nil && !errors.Is(err, kernel.ErrNotFound):
			return fmt.Errorf("item: get %s: %w", full, err)
		}
		actual, err := s.federatedChecksums(ctx, c)
		if err != nil {
			return err
		}
		stored, err := items.Put(ctx, model.Item{
			RepoKey: cfg.Key, Name: full, Size: c.Size, Actual: actual, Original: c.Original,
			MimeType: MimeType(full), Created: c.Created, CreatedBy: c.CreatedBy,
			Modified: c.Modified, ModifiedBy: c.ModifiedBy, Updated: now,
		})
		if err != nil {
			return fmt.Errorf("item: put %s: %w", full, err)
		}
		before, err := s.propertySnapshot(ctx, stored)
		if err != nil {
			return err
		}
		if err := s.reference(ctx, actual, c.Size, now); err != nil {
			return err
		}
		if old.ID != 0 && old.Actual.SHA256 != "" {
			if _, err := s.Store.Blobs().Unref(ctx, old.Actual.SHA256, 1); err != nil {
				return fmt.Errorf("item: release replaced blob: %w", err)
			}
		}
		// New content is a new file, as a deploy over other content makes
		// it (settle): the properties are the change's, and the download
		// statistics start again.
		if err := s.settle(ctx, stored, c.Properties, old.ID != 0, true, now); err != nil {
			return err
		}
		ctx = ownPropertyEvents(ctx, stored.ID, c.ModifiedBy)
		hooks, env := s.format(cfg.PackageType), s.env()
		if err := hooks.Deployed(ctx, env, cfg, stored); err != nil {
			return err //nolint:wrapcheck // the format's own error
		}
		if err := s.annotated(ctx, hooks, env, cfg, stored, format.Annotation{Set: c.Properties, At: now}); err != nil {
			return err
		}
		if err := s.emitWritten(ctx, stored, c.ModifiedBy, now); err != nil {
			return err
		}
		return s.emitPropertyChanges(ctx, stored, before, c.ModifiedBy, now)
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// conflict marks an item in the way with kernel.ErrConflict too, which is
// what another module can test for (artapi.FederatedItems).
func conflict(err error) error {
	if errors.Is(err, ErrConflict) {
		return fmt.Errorf("%w: %w", kernel.ErrConflict, err)
	}
	return err
}

// federatedChecksums is the content's checksums as this instance's blob
// index records them, or the change's when the index has no row yet: the
// blob store has verified the SHA-256 the change names, and the member
// that made the change computed the others from the same bytes.
func (s *Service) federatedChecksums(ctx context.Context, c artapi.ItemChange) (kernel.Checksums, error) {
	if c.Actual.SHA256 == "" {
		return kernel.Checksums{}, fmt.Errorf("%w: a federated file needs its SHA-256", kernel.ErrInvalid)
	}
	b, err := s.Store.Blobs().Find(ctx, kernel.Checksums{SHA256: c.Actual.SHA256})
	switch {
	case err == nil:
		return b.Checksums, nil
	case errors.Is(err, kernel.ErrNotFound):
		return c.Actual, nil
	default:
		return kernel.Checksums{}, fmt.Errorf("item: blob %s: %w", c.Actual.SHA256, err)
	}
}

// reference takes the item's reference on its content, as a deploy does
// (writeFile): content collected since the blob store took it rolls the
// apply back, and the stream retries it.
func (s *Service) reference(ctx context.Context, actual kernel.Checksums, size int64, now time.Time) error {
	ref, err := s.Store.Blobs().Ref(ctx, blob.Blob{Checksums: actual, Size: size, Created: now}, 1)
	if errors.Is(err, port.ErrDeleting) {
		return fmt.Errorf("%w: %w", ErrCollected, err)
	}
	if err != nil {
		return fmt.Errorf("item: reference blob: %w", err)
	}
	if !ref.Deleted.IsZero() {
		ok, err := s.Blobs.Exists(ctx, actual.SHA256)
		if err != nil {
			return fmt.Errorf("item: blob exists: %w", err)
		}
		if !ok {
			return ErrCollected
		}
	}
	return nil
}

// applySameContent is a change that left a file's content as this
// instance holds it: a property change or a recorded checksum. The record
// stays, as Annotate leaves it, and takes the change's properties and
// original checksums; the format follows the property change as it does
// for Annotate.
func (s *Service) applySameContent(ctx context.Context, t Target, old model.Item, c artapi.ItemChange) error {
	if old.Original != c.Original {
		old.Original = c.Original
		if _, err := s.Store.Items().Put(ctx, old); err != nil {
			return fmt.Errorf("item: put %s: %w", old.FullPath(), err)
		}
	}
	return s.applyProperties(ctx, t, old, c.Properties)
}

// applyFolder creates the folder at t.Path when missing and gives it the
// change's properties. A file in the way is a conflict.
func (s *Service) applyFolder(ctx context.Context, t Target, c artapi.ItemChange) error {
	now := s.now()
	if err := s.ensureFolders(ctx, t, t.Path, c.ModifiedBy, now); err != nil {
		return conflict(err)
	}
	folder, err := s.Store.Items().Get(ctx, t.Repo.Key, false, t.Path)
	if err != nil {
		return fmt.Errorf("item: get %s: %w", t.Path, err)
	}
	return s.applyProperties(ctx, t, folder, c.Properties)
}

// applyProperties makes it's properties want, through annotate, so the
// format's Annotated hook and the property events follow as they do for
// a client's change. Nothing to change changes nothing.
func (s *Service) applyProperties(ctx context.Context, t Target, it model.Item, want map[string][]string) error {
	have, err := s.propertySnapshot(ctx, it)
	if err != nil {
		return err
	}
	set := model.Properties{}
	for k, v := range want {
		if !slices.Equal(have[k], v) {
			set[k] = slices.Clone(v)
		}
	}
	var remove []string
	for _, k := range slices.Sorted(maps.Keys(have)) {
		if _, ok := want[k]; !ok {
			remove = append(remove, k)
		}
	}
	if len(set) == 0 && len(remove) == 0 {
		return nil
	}
	return s.annotate(ctx, t.Repo, it, set, remove, false)
}

// DeleteFederated removes the file at p of cfg, into the trash can when it
// is on, as a client's delete does, or the folder at p once nothing is
// left in it (artapi.FederatedItems.Delete). Nothing at p is not an error.
func (s *Service) DeleteFederated(ctx context.Context, cfg repo.Config, p string, folder bool) error {
	ctx = federated(ctx)
	full := strings.Trim(p, "/")
	if full == "" {
		return fmt.Errorf("%w: a federated delete needs a path", kernel.ErrInvalidItemPath)
	}
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the callback names its failures
		it, err := s.Store.Items().Get(ctx, cfg.Key, false, full)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("item: get %s: %w", full, err)
		case it.Folder != folder:
			// The other kind of item at the path is not the one deleted.
			return nil
		case folder:
			children, _, err := s.Store.Items().Children(ctx, cfg.Key, false, full, kernel.Page{Limit: 1})
			if err != nil {
				return fmt.Errorf("item: list %s: %w", full, err)
			}
			if len(children) > 0 {
				// Something written here since, by a change that won, keeps
				// the folder.
				return nil
			}
		}
		_, err = s.Delete(ctx, Target{Repo: cfg, Path: full}, "")
		return err
	})
}

// DiscardFederated removes whatever is at p of cfg, a folder with
// everything below it, as a client's delete does
// (artapi.FederatedItems.Discard). Nothing at p is not an error.
func (s *Service) DiscardFederated(ctx context.Context, cfg repo.Config, p string) error {
	ctx = federated(ctx)
	full := strings.Trim(p, "/")
	if full == "" {
		return fmt.Errorf("%w: a federated discard needs a path", kernel.ErrInvalidItemPath)
	}
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // the callback names its failures
		_, err := s.Store.Items().Get(ctx, cfg.Key, false, full)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("item: get %s: %w", full, err)
		}
		_, err = s.Delete(ctx, Target{Repo: cfg, Path: full}, "")
		return err
	})
}

// ListFederated lists up to limit of cfg's files and folders after the
// full path after, in byte order of full path, each as the change that
// would leave another member's copy so (artapi.FederatedItems.List).
func (s *Service) ListFederated(
	ctx context.Context, cfg repo.Config, after string, limit int, properties bool,
) ([]artapi.ItemChange, error) {
	page := kernel.Page{Limit: limit}
	if after != "" {
		page.After = kernel.Cursor(after)
	}
	items, _, err := s.Store.Items().DescendantsByFullPath(ctx, cfg.Key, false, kernel.RootPath, page)
	if err != nil {
		return nil, fmt.Errorf("item: list %s: %w", cfg.Key, err)
	}
	out := make([]artapi.ItemChange, 0, len(items))
	for _, it := range items {
		c := change(it)
		if properties {
			if c.Properties, err = s.propertySnapshot(ctx, it); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// HoldsFederated reports whether a file of cfg has the content sha256
// (artapi.FederatedItems.Holds). The content index is per blob, not per
// repository, so it pages through every file of that content until one
// is cfg's.
func (s *Service) HoldsFederated(ctx context.Context, cfg repo.Config, sha256 string) (bool, error) {
	if sha256 == "" {
		return false, nil
	}
	page := kernel.Page{Limit: 100}
	for {
		items, next, err := s.Store.Items().FindByChecksum(ctx, kernel.Checksums{SHA256: sha256}, page)
		if err != nil {
			return false, fmt.Errorf("item: find %s: %w", sha256, err)
		}
		for _, it := range items {
			if it.RepoKey == cfg.Key && !it.Cached && !it.Folder {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		page.After = next
	}
}
