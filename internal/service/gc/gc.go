// Package gc reclaims storage (decision 0010). A pass purges trash entries
// past their retention, reaps abandoned upload sessions, deletes blobs no
// item or trash entry references, and deletes content in the blob store that
// the index never recorded (a deploy whose transaction failed after its
// content was written). Passes run on a schedule and on
// POST /api/system/storage/gc, under a lease so two instances sharing a
// database never collect at once.
//
// Every blob is removed in three steps: its index row is marked deleting,
// its content is deleted, and the row becomes a tombstone. A deleting row
// refuses references, and a deploy that revives a tombstone checks the
// content is still there, so a deploy of the same content racing the removal
// is refused and repeated rather than left referencing missing content.
package gc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// DefaultInterval is Artifactory's default garbage collection schedule,
	// every four hours.
	DefaultInterval = 4 * time.Hour
	// DefaultGrace is how long a blob must have been unreferenced, or its
	// content unindexed, before it is deleted. It must outlast any upload in
	// flight: content is written before its reference, so younger
	// unreferenced content may be a deploy about to commit.
	DefaultGrace = time.Hour
	// DefaultUploadExpiry is how long an upload session may sit untouched
	// before it is abandoned and reaped.
	DefaultUploadExpiry = 24 * time.Hour
	// leaseName and leaseTTL bound a pass: the lease is renewed between
	// steps, so a crashed pass frees it within the TTL.
	leaseName = "storage-gc"
	leaseTTL  = 30 * time.Minute
	batch     = 500
	// tombstoneRetention is how long the index remembers that a blob's
	// content was removed. A deploy reviving the row in that time checks the
	// content is there, so it must outlast the moment between a deploy
	// writing content and referencing it, which is milliseconds; a day is
	// generous and keeps the rows few.
	tombstoneRetention = 24 * time.Hour
)

// Report is what one pass did.
type Report struct {
	// Skipped is set when another instance held the lease; nothing was done.
	Skipped        bool
	TrashPurged    int
	UploadsReaped  int
	BlobsDeleted   int
	OrphansDeleted int
	BytesFreed     int64
	// Kept counts blobs that were referenced again while being collected,
	// and so were left alone.
	Kept int
	// Pruned counts tombstones dropped and revived rows forgotten after
	// tombstoneRetention.
	Pruned int64
}

// Options configures New. Zero values mean the defaults.
type Options struct {
	Grace        time.Duration
	UploadExpiry time.Duration
	// Reapers discard stale upload sessions of other kinds, keyed by the
	// session's Backend: a JFrog CLI multipart session holds staged parts
	// on local disk as well as a blob upload. Sessions of a backend with no
	// reaper are blob store uploads.
	Reapers map[string]func(context.Context, store.Upload) error
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
}

// Collector runs garbage collection passes over one store and its blob
// store. Passes in one process run one at a time.
type Collector struct {
	store        store.Store
	blobs        blob.Store
	trash        *trash.Service
	grace        time.Duration
	uploadExpiry time.Duration
	reapers      map[string]func(context.Context, store.Upload) error
	now          func() time.Time
	holder       string

	mu sync.Mutex
	// orphans remembers when each unindexed content blob was first seen.
	// Blob stores keep no times, so content is deleted only once it has
	// stayed unindexed for the grace period across passes; a restart only
	// delays that.
	orphans map[string]time.Time
}

// New returns a Collector over st and blobs.
func New(st store.Store, blobs blob.Store, o Options) (*Collector, error) {
	id, err := blob.NewUploadID()
	if err != nil {
		return nil, fmt.Errorf("gc: lease holder: %w", err)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Collector{
		store: st, blobs: blobs, trash: &trash.Service{Store: st, Now: o.Now},
		grace:        cmp.Or(o.Grace, DefaultGrace),
		uploadExpiry: cmp.Or(o.UploadExpiry, DefaultUploadExpiry),
		reapers:      o.Reapers,
		now:          o.Now, holder: "storage-gc-" + id,
		orphans: map[string]time.Time{},
	}, nil
}

// Run makes one pass. A step that fails does not stop the others; their
// errors are joined.
func (c *Collector) Run(ctx context.Context) (Report, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rep Report
	held, err := c.renew(ctx)
	if err != nil || !held {
		rep.Skipped = err == nil
		return rep, err
	}
	defer func() {
		_ = c.store.Leases().Release(context.WithoutCancel(ctx), leaseName, c.holder)
	}()
	var errs []error
	for _, step := range []func(context.Context, *Report) error{
		c.purgeTrash, c.reapUploads, c.finishDeleting, c.collectIndexed, c.collectOrphans, c.prune,
	} {
		errs = append(errs, step(ctx, &rep))
		if held, err := c.renew(ctx); err != nil || !held {
			errs = append(errs, cmp.Or(err, errLeaseLost))
			break
		}
	}
	return rep, errors.Join(errs...)
}

// renew takes or extends the lease and reports whether it is held.
func (c *Collector) renew(ctx context.Context) (bool, error) {
	now := c.now()
	held, err := c.store.Leases().Acquire(ctx, types.Lease{
		Name: leaseName, Holder: c.holder, Task: "garbage collection",
		Acquired: now, Expires: now.Add(leaseTTL),
	}, now)
	if err != nil {
		return false, fmt.Errorf("gc: lease: %w", err)
	}
	return held, nil
}

func (c *Collector) purgeTrash(ctx context.Context, rep *Report) error {
	n, err := c.trash.PurgeExpired(ctx)
	rep.TrashPurged += n
	return err //nolint:wrapcheck // the trash service wraps its own
}

// reapUploads aborts upload sessions untouched for the upload expiry. A
// session of another blob store (the cold tier, or a mirror member whose
// mirror session is gone) is unknown here; only its row is dropped, and the
// object store's own lifecycle rule removes what it left.
func (c *Collector) reapUploads(ctx context.Context, rep *Report) error {
	before := c.now().Add(-c.uploadExpiry)
	for {
		stale, err := c.store.Uploads().Stale(ctx, before, batch)
		if err != nil {
			return fmt.Errorf("gc: stale uploads: %w", err)
		}
		reaped := 0
		for _, u := range stale {
			if reap, ok := c.reapers[u.Backend]; ok {
				if err := reap(ctx, u); err != nil {
					return fmt.Errorf("gc: reap %s upload %s: %w", u.Backend, u.ID, err)
				}
				reaped++
				continue
			}
			if up, err := c.blobs.ResumeUpload(ctx, u.ID); err == nil {
				_ = up.Abort(ctx)
			}
			err := c.store.Uploads().Delete(ctx, u.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("gc: drop upload %s: %w", u.ID, err)
			}
			reaped++
		}
		rep.UploadsReaped += reaped
		if len(stale) < batch || reaped == 0 {
			return nil
		}
	}
}

// finishDeleting removes the blobs a crashed pass marked deleting and never
// finished. Until then a deploy of the same content is refused.
func (c *Collector) finishDeleting(ctx context.Context, rep *Report) error {
	page := store.Page{Limit: batch}
	for {
		rows, next, err := c.store.Blobs().Deleting(ctx, page)
		if err != nil {
			return fmt.Errorf("gc: deleting blobs: %w", err)
		}
		for _, b := range rows {
			if err := c.remove(ctx, b); err != nil {
				return err
			}
			rep.BlobsDeleted++
			rep.BytesFreed += b.Size
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

// collectIndexed deletes blobs whose index row has had no references for the
// grace period.
func (c *Collector) collectIndexed(ctx context.Context, rep *Report) error {
	cutoff := c.now().Add(-c.grace)
	page := store.Page{Limit: batch}
	for {
		rows, next, err := c.store.Blobs().Unreferenced(ctx, page)
		if err != nil {
			return fmt.Errorf("gc: unreferenced blobs: %w", err)
		}
		for _, b := range rows {
			if b.Created.After(cutoff) {
				continue
			}
			removed, err := c.destroy(ctx, b, rep)
			if err != nil {
				return err
			}
			if removed {
				rep.BlobsDeleted++
				rep.BytesFreed += b.Size
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

// errLeaseLost ends a pass whose lease another instance took over, which only
// happens when a step outran the lease TTL.
var errLeaseLost = errors.New("gc: lease was taken over")

// destroy removes a blob: its row is marked deleting, which a blob
// referenced again since it was listed refuses (it is kept), then remove
// deletes the content and leaves the tombstone. It reports whether it
// removed the blob.
func (c *Collector) destroy(ctx context.Context, b types.Blob, rep *Report) (bool, error) {
	_, err := c.store.Blobs().MarkDeleting(ctx, b, c.now())
	switch {
	case errors.Is(err, store.ErrConflict):
		rep.Kept++
		return false, nil
	case err != nil:
		return false, fmt.Errorf("gc: mark blob %s: %w", b.Checksums.SHA256, err)
	}
	return true, c.remove(ctx, b)
}

// remove deletes a deleting blob's content and turns its row into a
// tombstone. The row is unreferenced and refuses new references, so nothing
// can come to need the content while it goes.
func (c *Collector) remove(ctx context.Context, b types.Blob) error {
	sha := b.Checksums.SHA256
	if err := c.blobs.Delete(ctx, sha); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return fmt.Errorf("gc: delete content %s: %w", sha, err)
	}
	err := c.store.Blobs().FinishDelete(ctx, sha, c.now())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("gc: finish deleting %s: %w", sha, err)
	}
	return nil
}

// indexed reports whether the index serves sha: a live row, or a deleting
// one that finishDeleting owns. A tombstone or no row at all leaves content
// unindexed. A failed read is an error, never "unindexed": content must not
// be taken for an orphan because the index could not be asked.
func (c *Collector) indexed(ctx context.Context, sha string) (bool, error) {
	b, err := c.store.Blobs().Get(ctx, sha)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("gc: blob %s: %w", sha, err)
	}
	return b.State != types.BlobDeleted, nil
}

// collectOrphans deletes content the index does not serve once it has stayed
// that way for the grace period. The walk only collects candidates; content
// is deleted after it, so no store is changed under its own iterator.
func (c *Collector) collectOrphans(ctx context.Context, rep *Report) error {
	now := c.now()
	seen := map[string]bool{}
	var due []blob.Info
	err := c.blobs.Walk(ctx, func(info blob.Info) error {
		sha := info.Digests.SHA256
		seen[sha] = true
		indexed, err := c.indexed(ctx, sha)
		if err != nil {
			return err
		}
		if indexed {
			delete(c.orphans, sha)
			return nil
		}
		first, ok := c.orphans[sha]
		if !ok {
			c.orphans[sha] = now
			return nil
		}
		if now.Sub(first) >= c.grace {
			due = append(due, info)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gc: walk blobs: %w", err)
	}
	for sha := range c.orphans {
		if !seen[sha] {
			delete(c.orphans, sha)
		}
	}
	for _, info := range due {
		sha := info.Digests.SHA256
		delete(c.orphans, sha)
		indexed, err := c.indexed(ctx, sha)
		if err != nil {
			return err
		}
		if indexed {
			continue
		}
		removed, err := c.destroy(ctx, types.Blob{
			Checksums: types.Checksums{SHA256: sha}, Size: info.Size, Created: now,
		}, rep)
		if err != nil {
			return err
		}
		if removed {
			rep.OrphansDeleted++
			rep.BytesFreed += info.Size
		}
	}
	return nil
}

// prune forgets removals older than tombstoneRetention.
func (c *Collector) prune(ctx context.Context, rep *Report) error {
	n, err := c.store.Blobs().Prune(ctx, c.now().Add(-tombstoneRetention))
	rep.Pruned += n
	if err != nil {
		return fmt.Errorf("gc: prune tombstones: %w", err)
	}
	return nil
}

// Loop runs a pass every interval until ctx ends, then returns ctx's error.
// Each pass's report and error go to onRun when it is set; a failed pass
// does not stop the loop.
func (c *Collector) Loop(
	ctx context.Context,
	every time.Duration,
	onRun func(Report, error),
) error {
	ticker := time.NewTicker(cmp.Or(every, DefaultInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("gc: loop: %w", ctx.Err())
		case <-ticker.C:
		}
		rep, err := c.Run(ctx)
		if onRun != nil {
			onRun(rep, err)
		}
	}
}
