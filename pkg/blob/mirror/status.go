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
	// Removed are members with replica records that are no longer
	// configured. Repair neither copies to nor reads from them, so a blob
	// whose only present copy is on one (SoleCopies) is out of the mirror's
	// reach until the member is configured again.
	Removed []RemovedMember
}

// RemovedMember is a member in Status.Removed.
type RemovedMember struct {
	Name string
	// Replicas are its replica records per state, ordered by state.
	Replicas []store.ReplicaCount
	// SoleCopies counts the blobs whose only present copy is on it.
	SoleCopies int64
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
	// SoleCopies counts the blobs whose only present copy is on this
	// member. A drained member (WriteEnabled false) is safe to remove from
	// the configuration once it is zero.
	SoleCopies int64
}

// Status reports the mirror's members, their replica records, and the
// records of members no longer configured.
func (s *Store) Status(ctx context.Context) (Status, error) {
	counts, err := s.replicas.Counts(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("blob/mirror: status: %w", err)
	}
	under, err := s.replicas.UnderReplicated(ctx, s.redundancy)
	if err != nil {
		return Status{}, fmt.Errorf("blob/mirror: status: %w", err)
	}
	sole, err := s.replicas.SoleCopies(ctx)
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
			SoleCopies: sole[m.Name],
		}
		for _, c := range counts {
			if c.Member == m.Name {
				ms.Replicas = append(ms.Replicas, c)
			}
		}
		st.Members = append(st.Members, ms)
	}
	// Counts are ordered by member, so each removed member's records are
	// adjacent.
	for _, c := range counts {
		if s.byName[c.Member] != nil {
			continue
		}
		if n := len(st.Removed); n == 0 || st.Removed[n-1].Name != c.Member {
			st.Removed = append(
				st.Removed,
				RemovedMember{Name: c.Member, SoleCopies: sole[c.Member]},
			)
		}
		last := &st.Removed[len(st.Removed)-1]
		last.Replicas = append(last.Replicas, c)
	}
	return st, nil
}
