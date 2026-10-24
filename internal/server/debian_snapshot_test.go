package server

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestAccClaimDebianSnapshotBindsSourceAndDistribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := inmem.New()
	job := debianSnapshotRequest{
		Source: "source", Target: "target", Tag: "tag", Distribution: "stable",
	}
	p := auth.Principal{Name: "admin", Admin: true}
	if err := claimDebianSnapshot(ctx, st, job, p); err != nil {
		t.Fatal(err)
	}
	if err := claimDebianSnapshot(ctx, st, job, p); err != nil {
		t.Fatalf("same pending request should be retryable: %v", err)
	}
	otherDistribution := job
	otherDistribution.Distribution = "testing"
	if err := claimDebianSnapshot(ctx, st, otherDistribution, p); err == nil {
		t.Fatal("a different distribution claimed an existing tag")
	}
	otherSource := job
	otherSource.Source = "other"
	if err := claimDebianSnapshot(ctx, st, otherSource, p); err == nil {
		t.Fatal("a different source claimed an existing tag")
	}
	marker, err := st.Items().Get(ctx, job.Target, false, snapshotRoot(job))
	if err != nil {
		t.Fatal(err)
	}
	props, err := st.Items().Properties(ctx, marker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := props["deb.snapshot.pending.source"]; len(got) != 1 || got[0] != job.Source {
		t.Fatalf("source claim changed: %v", got)
	}
	if got := props["deb.snapshot.pending.distribution"]; len(got) != 1 ||
		got[0] != job.Distribution {
		t.Fatalf("distribution claim changed: %v", got)
	}
}
