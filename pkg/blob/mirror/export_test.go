package mirror

import (
	"context"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// These names open the mirror's internals to its tests, which are the
// external test package mirror_test: they build mirrors over the in-memory
// metadata store and its row types, whose packages alias this package's
// declarations, so an in-package test importing them would be an import
// cycle. Each one reads or replaces exactly what the tests reached before.

// ChunkSize is chunkSize, the fan-out chunk New derives from the members'
// part sizes.
var ChunkSize = chunkSize

// The repair worker's default lease name, its backoff cap and the LastError
// of a pending replica that no member holds.
const (
	DefaultRepairLease = defaultRepairLease
	MaxRepairBackoff   = maxRepairBackoff
	NoSource           = noSource
)

// Settle waits until no background commit of sha is running, or ctx ends.
func (s *Store) Settle(ctx context.Context, sha string) error { return s.settle(ctx, sha) }

// Due reports whether r's repair backoff has passed.
func (s *Store) Due(r types.BlobReplica) bool { return s.due(r) }

// Now is the mirror's clock.
func (s *Store) Now() time.Time { return s.now() }

// Redundancy is the redundancy New derived.
func (s *Store) Redundancy() int { return s.redundancy }

// Quorum is the write quorum New derived.
func (s *Store) Quorum() int { return s.quorum }

// Chunk is the fan-out chunk New derived.
func (s *Store) Chunk() int64 { return s.chunk }

// Breaker is the named member's breaker and observed latency.
func (s *Store) Breaker(member string) *health { return &s.byName[member].health }

// Open reports whether the breaker is open at now.
func (h *health) Open(now time.Time) bool { return h.open(now) }

// Failure records a failed call at now, opening the breaker once threshold
// failures follow one another.
func (h *health) Failure(now time.Time, threshold int, cooldown time.Duration) {
	h.failure(now, threshold, cooldown)
}

// SetReplicas replaces the replica index, so a test can make it fail.
func (s *Store) SetReplicas(r store.ReplicaIndex) { s.replicas = r }

// RepairRunning is the lock that keeps two repair passes in this process
// apart.
func (s *Store) RepairRunning() *sync.Mutex { return &s.repair.running }

// SetRepairLeases replaces the repair worker's lease store.
func (s *Store) SetRepairLeases(l store.LeaseStore) { s.repair.leases = l }

// SetRepairBlobs replaces the repair worker's blob index.
func (s *Store) SetRepairBlobs(b store.BlobIndex) { s.repair.blobs = b }

// Children is the member upload of every active target of u, a mirror
// upload.
func Children(u blob.Upload) map[string]blob.Upload { return u.(*upload).children }
