// Package gc reclaims storage (decision 0010). A pass purges trash entries
// past their retention, reaps abandoned upload sessions, deletes blobs no
// item or trash entry references, and deletes content in the blob store that
// the index never recorded (a deploy whose transaction failed after its
// content was written). Passes run on a schedule and on
// POST /api/system/storage/gc, under a lease so two instances sharing a
// database never collect at once.
package gc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// Lost lists blobs whose content was deleted while the same content was
	// uploaded again. Their items need the content deployed again; the pass
	// also logs each one.
	Lost []string
}

// Options configures New. Zero values mean the defaults.
type Options struct {
	Grace        time.Duration
	UploadExpiry time.Duration
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	Log *slog.Logger
}

// Collector runs garbage collection passes over one store and its blob
// store. Passes in one process run one at a time.
type Collector struct {
	store        store.Store
	blobs        blob.Store
	trash        *trash.Service
	grace        time.Duration
	uploadExpiry time.Duration
	now          func() time.Time
	log          *slog.Logger
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
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Collector{
		store: st, blobs: blobs, trash: &trash.Service{Store: st, Now: o.Now},
		grace:        cmp.Or(o.Grace, DefaultGrace),
		uploadExpiry: cmp.Or(o.UploadExpiry, DefaultUploadExpiry),
		now:          o.Now, log: o.Log, holder: "storage-gc-" + id,
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
		c.purgeTrash, c.reapUploads, c.collectIndexed, c.collectOrphans,
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
			if err := c.deleteBlob(ctx, b, rep); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

var (
	// errReferenced aborts a row delete for a blob referenced again.
	errReferenced = errors.New("gc: blob is referenced")
	// errLeaseLost ends a pass whose lease another instance took over, which
	// only happens when a step outran the lease TTL.
	errLeaseLost = errors.New("gc: lease was taken over")
)

// deleteBlob removes an unreferenced blob: its index row first, then its
// content. The row goes first because a checksum deploy finds content
// through the index. Once the row is gone, a deploy of the same content must
// upload it again, so the content cannot be deleted under a reference taken
// through the index. Content uploaded again in the moment between the row
// delete and the content delete is the one case that loses data; it is
// checked for on both sides of the content delete and reported.
func (c *Collector) deleteBlob(ctx context.Context, b types.Blob, rep *Report) error {
	sha := b.Checksums.SHA256
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		row, err := c.store.Blobs().Get(ctx, sha)
		if err != nil {
			return fmt.Errorf("gc: blob %s: %w", sha, err)
		}
		if row.Refs > 0 {
			return errReferenced
		}
		err = c.store.Blobs().Delete(ctx, sha)
		if errors.Is(err, store.ErrConflict) {
			return errReferenced
		}
		return err //nolint:wrapcheck // wrapped below with the blob
	})
	switch {
	case errors.Is(err, errReferenced):
		rep.Kept++
		return nil
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("gc: delete blob %s: %w", sha, err)
	}
	if c.reindexed(ctx, sha) {
		// Uploaded and referenced again since the row was deleted: the
		// content is in use.
		rep.Kept++
		return nil
	}
	if err := c.blobs.Delete(ctx, sha); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return fmt.Errorf("gc: delete content %s: %w", sha, err)
	}
	if c.reindexed(ctx, sha) {
		if ok, err := c.blobs.Exists(ctx, sha); err == nil && !ok {
			rep.Lost = append(rep.Lost, sha)
			c.log.ErrorContext(
				ctx,
				"garbage collection deleted content uploaded again at the same moment; deploy it again",
				"sha256",
				sha,
			)
		}
	}
	rep.BlobsDeleted++
	rep.BytesFreed += b.Size
	return nil
}

func (c *Collector) reindexed(ctx context.Context, sha string) bool {
	_, err := c.store.Blobs().Get(ctx, sha)
	return err == nil
}

// collectOrphans deletes content the index has no row for once it has stayed
// unindexed for the grace period. The walk only collects candidates; content
// is deleted after it, so no store is changed under its own iterator.
func (c *Collector) collectOrphans(ctx context.Context, rep *Report) error {
	now := c.now()
	seen := map[string]bool{}
	var due []blob.Info
	err := c.blobs.Walk(ctx, func(info blob.Info) error {
		sha := info.Digests.SHA256
		seen[sha] = true
		if c.reindexed(ctx, sha) {
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
		if c.reindexed(ctx, sha) {
			delete(c.orphans, sha)
			continue
		}
		if err := c.blobs.Delete(ctx, sha); err != nil && !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("gc: delete orphan %s: %w", sha, err)
		}
		delete(c.orphans, sha)
		rep.OrphansDeleted++
		rep.BytesFreed += info.Size
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
