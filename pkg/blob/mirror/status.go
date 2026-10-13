package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Status is a mirror's configuration, member health and replica counts, for
// the storage summary (#18). Artifactory's summary has nothing per shard.
type Status struct {
	Redundancy  int
	WriteQuorum int
	// Zone is this instance's zone.
	Zone string
	// UnderReplicated counts blobs with fewer present copies than
	// Redundancy (store.ReplicaIndex.UnderReplicated).
	UnderReplicated int64
	Members         []MemberStatus
}

// MemberStatus is one member's view in Status.
type MemberStatus struct {
	Name, Zone, Kind string
	WriteEnabled     bool
	// Available is false while the member's breaker is open: reads and
	// writes go to the other members first.
	Available bool
	// Latency is the weighted average of recent successful calls; zero
	// before the first one.
	Latency time.Duration
	// Replicas are the member's replica records per state, ordered by state.
	Replicas []store.ReplicaCount
}

// Status reports the mirror's members and replica records. Records of
// members no longer configured are left out: they are drained or removed
// stores the repair worker no longer touches.
func (s *Store) Status(ctx context.Context) (Status, error) {
	counts, err := s.replicas.Counts(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("blob/mirror: status: %w", err)
	}
	under, err := s.replicas.UnderReplicated(ctx, s.redundancy)
	if err != nil {
		return Status{}, fmt.Errorf("blob/mirror: status: %w", err)
	}
	now := s.now()
	st := Status{
		Redundancy: s.redundancy, WriteQuorum: s.quorum, Zone: s.zone, UnderReplicated: under,
		Members: make([]MemberStatus, 0, len(s.members)),
	}
	for _, m := range s.members {
		ms := MemberStatus{
			Name: m.Name, Zone: m.Zone, Kind: m.Kind, WriteEnabled: m.WriteEnabled,
			Available: !m.health.open(now), Latency: m.health.avg(),
		}
		for _, c := range counts {
			if c.Member == m.Name {
				ms.Replicas = append(ms.Replicas, c)
			}
		}
		st.Members = append(st.Members, ms)
	}
	return st, nil
}
