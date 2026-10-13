package mirror

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// repairLease names the lease every repair pass holds, so two passes,
	// in this process or another sharing the database, never overlap.
	repairLease = "blob-mirror-repair"
	// repairLeaseTTL is how far ahead a pass takes the lease; it renews
	// between steps and pages, so a crashed pass frees the lease this soon.
	repairLeaseTTL = 5 * time.Minute
	// maxRepairBackoff caps the wait between attempts on one replica.
	maxRepairBackoff = time.Hour
	// noSource is the LastError of a pending replica that no member holds.
	noSource = "no source holds the blob"
)

// repairState is the Store's repair worker configuration.
type repairState struct {
	blobs       store.BlobIndex
	leases      store.LeaseStore
	batch       int
	maxAttempts int
	backoff     time.Duration
	onRepair    func(RepairReport, error)
	holder      string
	// running keeps two passes in this process apart; they share a lease
	// holder, so the lease alone would let both through.
	running sync.Mutex
	// backfillAfter continues the backfill scan where the last pass
	// stopped, so each pass looks at a bounded number of blobs.
	backfillAfter string
}

// RepairReport counts what one repair pass did.
type RepairReport struct {
	// Skipped is set when another pass held the lease; nothing was done.
	Skipped bool
	// Copied counts pending replicas filled by copying from another member.
	Copied int
	// Found counts pending replicas whose member already held the blob, for
	// example a commit that finished after its record was written.
	Found int
	// Failed counts attempts that failed this pass.
	Failed int
	// Deleted counts tombstones whose copy was removed.
	Deleted int
	// DeletesSkipped counts tombstones of blobs uploaded again since the
	// delete, which were kept rather than removed.
	DeletesSkipped int
	// Backfilled counts pending records added to bring a blob up to the
	// redundancy, for example on a newly added member.
	Backfilled int
	// Drained counts pending records added because a blob's only copies are
	// on members no longer accepting writes.
	Drained int
}

// Repair runs one repair pass under the repair lease:
//
//  1. pending replicas are copied to their member from a member that holds
//     the blob, or marked present when the member already has it;
//  2. deleting tombstones are carried out, unless the blob was uploaded
//     again since the delete;
//  3. blobs with fewer copies on write-enabled members than the redundancy
//     get pending records, which fills a newly added member and moves the
//     copies of a draining one.
//
// Each step works on at most the repair batch, so a pass is bounded. It never
// deletes a copy because there are too many: removing a member stays an
// explicit operator action (decision 0028). When another pass holds the lease
// it returns a report with Skipped set and no error.
func (s *Store) Repair(ctx context.Context) (RepairReport, error) {
	var rep RepairReport
	if s.repair.leases == nil || s.repair.blobs == nil {
		return rep, fmt.Errorf("%w: repair needs a lease store and a blob index", ErrConfig)
	}
	if !s.repair.running.TryLock() {
		rep.Skipped = true
		return rep, nil
	}
	defer s.repair.running.Unlock()
	held, err := s.renew(ctx)
	if err != nil || !held {
		rep.Skipped = !held
		return rep, err
	}
	defer func() {
		_ = s.repair.leases.Release(context.WithoutCancel(ctx), repairLease, s.repair.holder)
	}()
	for _, step := range []func(context.Context, *RepairReport) error{
		s.repairPending, s.repairDeletes, s.backfill,
	} {
		if held, err := s.renew(ctx); err != nil || !held {
			return rep, errors.Join(err, s.lostLease(held))
		}
		if err := step(ctx, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// lostLease reports a lease taken over mid-pass, which only happens when a
// step outran repairLeaseTTL.
func (*Store) lostLease(held bool) error {
	if held {
		return nil
	}
	return fmt.Errorf("blob/mirror: repair lease %s was taken over", repairLease)
}

// renew takes or extends the repair lease and reports whether it is held.
func (s *Store) renew(ctx context.Context) (bool, error) {
	now := s.now()
	held, err := s.repair.leases.Acquire(ctx, types.Lease{
		Name: repairLease, Holder: s.repair.holder, Task: "blob mirror repair",
		Acquired: now, Expires: now.Add(repairLeaseTTL),
	}, now)
	if err != nil {
		return false, fmt.Errorf("blob/mirror: repair lease: %w", err)
	}
	return held, nil
}

// due reports whether r's backoff has passed: a record that never failed is
// due at once, and the wait doubles with every failed attempt.
func (s *Store) due(r types.BlobReplica) bool {
	if r.Attempts == 0 {
		return true
	}
	wait := s.repair.backoff
	for i := 1; i < r.Attempts && wait < maxRepairBackoff; i++ {
		wait *= 2
	}
	return !s.now().Before(r.Updated.Add(min(wait, maxRepairBackoff)))
}

// queue calls fn for records in the given state, oldest first, that are
// due, until batch of them were handled or the index has no more. It renews
// the lease between pages.
func (s *Store) queue(
	ctx context.Context,
	state types.ReplicaState,
	fn func(types.BlobReplica) error,
) error {
	handled := 0
	p := store.Page{Limit: s.repair.batch}
	for {
		recs, next, err := s.replicas.List(ctx,
			store.ReplicaFilter{States: []types.ReplicaState{state}}, p)
		if err != nil {
			return fmt.Errorf("blob/mirror: list %s replicas: %w", state, err)
		}
		for _, r := range recs {
			if handled == s.repair.batch {
				return nil
			}
			if m := s.byName[r.Member]; m == nil || !s.due(r) || m.health.open(s.now()) {
				// A member no longer configured, a record backing off, or a
				// member whose breaker is open: leave it for a later pass.
				continue
			}
			if err := fn(r); err != nil {
				return err
			}
			handled++
		}
		if next == "" {
			return nil
		}
		if held, err := s.renew(ctx); err != nil || !held {
			return errors.Join(err, s.lostLease(held))
		}
		p.After = next
	}
}

// repairPending fills pending replicas.
func (s *Store) repairPending(ctx context.Context, rep *RepairReport) error {
	return s.queue(ctx, types.ReplicaPending, func(r types.BlobReplica) error {
		m := s.byName[r.Member]
		if info, err := s.stat(ctx, m, r.SHA256); err == nil {
			rep.Found++
			return s.markPresent(ctx, r, info.Size)
		}
		size, err := s.copyTo(ctx, m, r.SHA256)
		if err != nil {
			rep.Failed++
			return s.attemptFailed(ctx, r, err, types.ReplicaFailed)
		}
		rep.Copied++
		return s.markPresent(ctx, r, size)
	})
}

// stat asks one member for a blob, recording the outcome in its health.
func (s *Store) stat(ctx context.Context, m *member, sha string) (blob.Info, error) {
	start := s.now()
	info, err := m.Store.Stat(ctx, sha)
	s.observe(m, start, err)
	return info, err //nolint:wrapcheck // the caller names the member
}

// markPresent records that r's member holds the blob.
func (s *Store) markPresent(ctx context.Context, r types.BlobReplica, size int64) error {
	r.State, r.Size, r.LastError, r.Updated = types.ReplicaPresent, size, "", s.now()
	if err := s.replicas.Put(ctx, r); err != nil {
		return fmt.Errorf("blob/mirror: record %s on %s: %w", r.SHA256, r.Member, err)
	}
	return nil
}

// attemptFailed records a failed attempt on r. Once the attempts reach the
// maximum, a pending record becomes giveUp; a tombstone stays a tombstone
// (giveUp is its own state), because a failed tombstone would let reads
// probe the members and serve the deleted blob again.
func (s *Store) attemptFailed(
	ctx context.Context,
	r types.BlobReplica,
	cause error,
	giveUp types.ReplicaState,
) error {
	r.Attempts++
	r.LastError, r.Updated = cause.Error(), s.now()
	if r.Attempts >= s.repair.maxAttempts {
		r.State = giveUp
	}
	if err := s.replicas.Put(ctx, r); err != nil {
		return fmt.Errorf("blob/mirror: record %s on %s: %w", r.SHA256, r.Member, err)
	}
	return nil
}

// errNoSource reports a pending replica no member can be copied from.
var errNoSource = errors.New(noSource)

// copyTo copies sha to m from the members holding it, in read order, and
// returns the size copied.
func (s *Store) copyTo(ctx context.Context, m *member, sha string) (int64, error) {
	recs, err := s.replicas.ForBlob(ctx, sha)
	if err != nil {
		return 0, fmt.Errorf("blob/mirror: replicas of %s: %w", sha, err)
	}
	var sources []*member
	now := s.now()
	for _, r := range recs {
		src := s.byName[r.Member]
		if r.State == types.ReplicaPresent && src != nil && src != m && !src.health.open(now) {
			sources = append(sources, src)
		}
	}
	if len(sources) == 0 {
		return 0, errNoSource
	}
	want := s.knownDigests(ctx, sha)
	var failures []error
	for _, src := range s.readOrder(sources) {
		size, err := s.copyFrom(ctx, src, m, want)
		if err == nil {
			return size, nil
		}
		failures = append(failures, err)
	}
	return 0, errors.Join(failures...)
}

// knownDigests is every digest the blob index records for sha, so the
// target verifies the copy against all of them; the sha256 alone when the
// index has no row.
func (s *Store) knownDigests(ctx context.Context, sha string) blob.Digests {
	want := blob.Digests{SHA256: sha}
	if b, err := s.repair.blobs.Get(ctx, sha); err == nil {
		want.SHA1, want.MD5 = b.Checksums.SHA1, b.Checksums.MD5
	}
	return want
}

// copyFrom streams one blob from src into dst, verified on commit.
func (s *Store) copyFrom(ctx context.Context, src, dst *member, want blob.Digests) (int64, error) {
	start := s.now()
	rc, info, err := src.Store.Open(ctx, want.SHA256)
	s.observe(src, start, err)
	if err != nil {
		return 0, fmt.Errorf("open on %s: %w", src.Name, err)
	}
	defer func() { _ = rc.Close() }()
	if want.SHA1 == "" {
		want.SHA1 = info.Digests.SHA1
	}
	if want.MD5 == "" {
		want.MD5 = info.Digests.MD5
	}
	start = s.now()
	up, err := dst.Store.BeginUpload(ctx, blob.WithExpectedSHA256(want.SHA256))
	if err == nil {
		if _, err = up.Append(ctx, rc); err == nil {
			_, err = up.Commit(ctx, want)
		}
		if err != nil {
			_ = up.Abort(context.WithoutCancel(ctx))
		}
	}
	s.observe(dst, start, err)
	if err != nil {
		return 0, fmt.Errorf("copy from %s to %s: %w", src.Name, dst.Name, err)
	}
	return info.Size, nil
}

// repairDeletes carries out deleting tombstones. A blob referenced again
// since the delete (uploaded anew) is not deleted: its tombstone becomes a
// present record when the member still holds the blob, or a pending one when
// it does not, so the mirror converges on the re-upload instead of losing it.
func (s *Store) repairDeletes(ctx context.Context, rep *RepairReport) error {
	return s.queue(ctx, types.ReplicaDeleting, func(r types.BlobReplica) error {
		m := s.byName[r.Member]
		reuploaded, err := s.referenced(ctx, r.SHA256)
		if err != nil {
			return err
		}
		if reuploaded {
			rep.DeletesSkipped++
			if info, err := s.stat(ctx, m, r.SHA256); err == nil {
				return s.markPresent(ctx, r, info.Size)
			}
			r.State, r.Attempts, r.LastError, r.Updated =
				types.ReplicaPending, 0, "uploaded again after a delete", s.now()
			if err := s.replicas.Put(ctx, r); err != nil {
				return fmt.Errorf("blob/mirror: record %s on %s: %w", r.SHA256, r.Member, err)
			}
			return nil
		}
		start := s.now()
		err = m.Store.Delete(ctx, r.SHA256)
		s.observe(m, start, err)
		if err != nil && !errors.Is(err, blob.ErrNotFound) {
			rep.Failed++
			return s.attemptFailed(ctx, r, err, types.ReplicaDeleting)
		}
		rep.Deleted++
		if err := s.replicas.Delete(ctx, r.SHA256, r.Member); err != nil && !isNotFound(err) {
			return fmt.Errorf("blob/mirror: forget %s on %s: %w", r.SHA256, r.Member, err)
		}
		return nil
	})
}

// referenced reports whether the blob index counts references to sha.
func (s *Store) referenced(ctx context.Context, sha string) (bool, error) {
	b, err := s.repair.blobs.Get(ctx, sha)
	switch {
	case isNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("blob/mirror: blob index %s: %w", sha, err)
	}
	return b.Refs > 0, nil
}

// backfill brings blobs up to the redundancy on write-enabled members. It
// scans the replica index from where the last pass stopped, looks at up to
// one batch of distinct blobs, and adds pending records on members chosen
// by placement order for every blob short of copies. A blob whose only
// copies sit on members no longer accepting writes is drained this way too.
// Blobs the index has never seen (stored before the mirror existed) are not
// found by this scan.
func (s *Store) backfill(ctx context.Context, rep *RepairReport) error {
	writable := 0
	for _, m := range s.members {
		if m.WriteEnabled {
			writable++
		}
	}
	want := min(s.redundancy, writable)
	seen := map[string]bool{}
	p := store.Page{After: s.repair.backfillAfter, Limit: s.repair.batch}
	for len(seen) < s.repair.batch {
		recs, next, err := s.replicas.List(ctx, store.ReplicaFilter{}, p)
		if err != nil {
			return fmt.Errorf("blob/mirror: list replicas: %w", err)
		}
		for _, r := range recs {
			if seen[r.SHA256] || len(seen) == s.repair.batch {
				continue
			}
			seen[r.SHA256] = true
			if err := s.place1(ctx, r.SHA256, want, rep); err != nil {
				return err
			}
		}
		p.After = next
		if next == "" {
			break
		}
	}
	s.repair.backfillAfter = p.After
	return nil
}

// place1 adds pending records for one blob until want write-enabled members
// hold or await it.
func (s *Store) place1(ctx context.Context, sha string, want int, rep *RepairReport) error {
	recs, err := s.replicas.ForBlob(ctx, sha)
	if err != nil {
		return fmt.Errorf("blob/mirror: replicas of %s: %w", sha, err)
	}
	has := map[string]bool{}
	placed, onWritable, onDraining := 0, 0, 0
	var size int64
	for _, r := range recs {
		m := s.byName[r.Member]
		switch {
		case r.State == types.ReplicaDeleting:
			// Deleted; never re-create copies of it.
			return nil
		case m == nil:
			continue
		case r.State == types.ReplicaPresent && m.WriteEnabled:
			onWritable++
			size = r.Size
		case r.State == types.ReplicaPresent:
			onDraining++
			size = r.Size
		}
		has[r.Member] = true
		if m.WriteEnabled && (r.State == types.ReplicaPresent || r.State == types.ReplicaPending) {
			placed++
		}
	}
	drained := onWritable == 0 && onDraining > 0
	if onWritable+onDraining == 0 {
		// Nothing to copy from yet; the pending records will be repaired
		// first or fail.
		return nil
	}
	for _, m := range s.placementOrder() {
		if placed >= want {
			break
		}
		if has[m.Name] {
			continue
		}
		reason, count := "below redundancy", &rep.Backfilled
		if drained {
			reason, count = "copies on draining members", &rep.Drained
		}
		r := types.BlobReplica{
			SHA256: sha, Member: m.Name, State: types.ReplicaPending, Size: size,
			Updated: s.now(), LastError: reason,
		}
		if err := s.replicas.Put(ctx, r); err != nil {
			return fmt.Errorf("blob/mirror: record %s on %s: %w", sha, m.Name, err)
		}
		*count++
		placed++
	}
	return nil
}

// placementOrder is the order new copies go to members: write-enabled
// members in this instance's zone, then the rest in rotation, members with
// an open breaker last (their records simply wait for repair).
func (s *Store) placementOrder() []*member {
	var zone, rest []*member
	for _, m := range s.members {
		switch {
		case !m.WriteEnabled:
		case s.inZone(m):
			zone = append(zone, m)
		default:
			rest = append(rest, m)
		}
	}
	if len(rest) > 0 {
		k := int(
			s.next.Add(1)-1,
		) % len(
			rest,
		) // #nosec G115 -- a rotation counter; wrapping is harmless
		rest = slices.Concat(rest[k:], rest[:k])
	}
	now := s.now()
	out := slices.Concat(zone, rest)
	slices.SortStableFunc(out, func(a, b *member) int {
		return boolOrder(a.health.open(now), b.health.open(now))
	})
	return out
}

// DefaultRepairInterval is how often RunRepair runs a pass when no interval
// is given.
const DefaultRepairInterval = time.Minute

// RunRepair runs a repair pass every interval, and at once whenever trigger
// fires (the storage optimize endpoint sends on it), until ctx ends, then
// returns ctx's error. Each pass's report and error go to OnRepair when it
// is set; a failed pass does not stop the loop.
func (s *Store) RunRepair(ctx context.Context, every time.Duration, trigger <-chan struct{}) error {
	if every <= 0 {
		every = DefaultRepairInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("blob/mirror: repair loop: %w", ctx.Err())
		case <-ticker.C:
		case <-trigger:
		}
		rep, err := s.Repair(ctx)
		if s.repair.onRepair != nil {
			s.repair.onRepair(rep, err)
		}
	}
}
