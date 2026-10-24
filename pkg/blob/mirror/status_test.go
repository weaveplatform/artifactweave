package mirror

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitStatus(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	ms[0].Zone, ms[0].Kind = "home", "fs"
	ms[2].WriteEnabled = false
	s, r := newMirror(t, ms, func(o *Options) {
		o.Redundancy, o.WriteQuorum, o.Zone, o.BreakerThreshold = 2, 1, "home", 1
	})
	// m1's commit fails: its copy is left pending and its breaker opens.
	f[1].failOn("commit", 0, errInjected)
	sha := putSettled(t, s, blobtest.Content(3, 100)).Digests.SHA256
	// Two members taken out of the configuration still have records: "gone"
	// holds the only copy of another blob, "old" a failed attempt at this one.
	other := strings.Repeat("ab", 32)
	for _, rec := range []types.BlobReplica{
		{SHA256: other, Member: "gone", State: types.ReplicaPresent, Size: 10},
		{SHA256: sha, Member: "old", State: types.ReplicaFailed, Size: 100},
	} {
		if err := r.Put(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Both blobs are under-replicated: the new one has one present copy, the
	// other only a copy on a member the mirror no longer reads from.
	if st.Redundancy != 2 || st.WriteQuorum != 1 || st.Zone != "home" ||
		st.UnderReplicated != 2 || len(st.Members) != 3 {
		t.Fatalf("status %+v", st)
	}
	m0, m1, m2 := st.Members[0], st.Members[1], st.Members[2]
	if m0.Name != "m0" || m0.Zone != "home" || m0.Kind != "fs" || !m0.Available ||
		!m0.WriteEnabled || m0.Latency < 0 || len(m0.Replicas) != 1 ||
		m0.Replicas[0].State != types.ReplicaPresent || m0.Replicas[0].Count != 1 ||
		m0.SoleCopies != 1 {
		t.Fatalf("m0 %+v", m0)
	}
	if m1.Available || len(m1.Replicas) != 1 || m1.Replicas[0].State != types.ReplicaPending {
		t.Fatalf("m1 %+v", m1)
	}
	if m2.WriteEnabled || len(m2.Replicas) != 0 || m2.SoleCopies != 0 {
		t.Fatalf("a drained member holds no records: %+v", m2)
	}
	if len(st.Removed) != 2 {
		t.Fatalf("removed members %+v", st.Removed)
	}
	gone, old := st.Removed[0], st.Removed[1]
	if gone.Name != "gone" || gone.SoleCopies != 1 || len(gone.Replicas) != 1 ||
		gone.Replicas[0].State != types.ReplicaPresent || gone.Replicas[0].Bytes != 10 {
		t.Fatalf("gone %+v", gone)
	}
	if old.Name != "old" || old.SoleCopies != 0 || len(old.Replicas) != 1 ||
		old.Replicas[0].State != types.ReplicaFailed {
		t.Fatalf("old %+v", old)
	}
}

// summaryFailures fails the summary queries.
type summaryFailures struct {
	store.ReplicaIndex
	counts, under, sole bool
}

func (f summaryFailures) SoleCopies(ctx context.Context) (map[string]int64, error) {
	if f.sole {
		return nil, errInjected
	}
	return f.ReplicaIndex.SoleCopies(ctx)
}

func (f summaryFailures) Counts(ctx context.Context) ([]store.ReplicaCount, error) {
	if f.counts {
		return nil, errInjected
	}
	return f.ReplicaIndex.Counts(ctx)
}

func (f summaryFailures) UnderReplicated(ctx context.Context, r int) (int64, error) {
	if f.under {
		return 0, errInjected
	}
	return f.ReplicaIndex.UnderReplicated(ctx, r)
}

func TestUnitStatusErrors(t *testing.T) {
	for name, fr := range map[string]summaryFailures{
		"counts": {counts: true}, "under-replicated": {under: true}, "sole copies": {sole: true},
	} {
		_, ms := fakes(2, false)
		s, r := newMirror(t, ms, nil)
		fr.ReplicaIndex = r
		s.replicas = fr
		if _, err := s.Status(context.Background()); !errors.Is(err, errInjected) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
