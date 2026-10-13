package mirror

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestStatus(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	ms[0].Zone, ms[0].Kind = "home", "fs"
	ms[2].WriteEnabled = false
	s, _ := newMirror(t, ms, func(o *Options) {
		o.Redundancy, o.WriteQuorum, o.Zone, o.BreakerThreshold = 2, 1, "home", 1
	})
	// m1's commit fails: its copy is left pending and its breaker opens.
	f[1].failOn("commit", 0, errInjected)
	putSettled(t, s, blobtest.Content(3, 100))
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Redundancy != 2 || st.WriteQuorum != 1 || st.Zone != "home" ||
		st.UnderReplicated != 1 || len(st.Members) != 3 {
		t.Fatalf("status %+v", st)
	}
	m0, m1, m2 := st.Members[0], st.Members[1], st.Members[2]
	if m0.Name != "m0" || m0.Zone != "home" || m0.Kind != "fs" || !m0.Available ||
		!m0.WriteEnabled || m0.Latency < 0 || len(m0.Replicas) != 1 ||
		m0.Replicas[0].State != types.ReplicaPresent || m0.Replicas[0].Count != 1 {
		t.Fatalf("m0 %+v", m0)
	}
	if m1.Available || len(m1.Replicas) != 1 || m1.Replicas[0].State != types.ReplicaPending {
		t.Fatalf("m1 %+v", m1)
	}
	if m2.WriteEnabled || len(m2.Replicas) != 0 {
		t.Fatalf("a drained member holds no records: %+v", m2)
	}
}

// summaryFailures fails the summary queries.
type summaryFailures struct {
	store.ReplicaIndex
	counts, under bool
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

func TestStatusErrors(t *testing.T) {
	for name, fr := range map[string]summaryFailures{
		"counts": {counts: true}, "under-replicated": {under: true},
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
