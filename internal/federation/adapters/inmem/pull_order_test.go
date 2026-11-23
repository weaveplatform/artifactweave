package inmem

import (
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Intents are taken in the order they were recorded, not by path or by their
// hashed operation keys (#1895), and a completed intent is never pending.
func TestUnitPullNextPendingFollowsRecordedOrder(t *testing.T) {
	s := New(memtx.New())
	ctx, p := t.Context(), s.PullProgress()
	binding := model.PullBinding{
		TargetKey: "target", TargetRevision: "target-revision", Root: "selected",
		Credential: kernel.CredentialReference{Name: "author", TokenID: "verified"},
		Source: artifactsapi.PullRepository{
			Repository: artifactsapi.TransferRepository{Key: "remote", Incarnation: "lifetime", Generation: "generation", PackageType: repo.Generic},
			Revision:   strings.Repeat("a", 64),
		},
	}
	run, err := p.Create(ctx, "run", binding, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := p.NextPending(ctx, run.Reference); err != nil || found {
		t.Fatal("an empty run had a pending intent", found, err)
	}
	if _, _, err := p.NextPending(ctx, model.PullReference{}); err == nil {
		t.Fatal("an invalid reference was read")
	}
	for _, path := range []string{"selected/z", "selected/b", "selected/m"} {
		if _, err := p.CreateItem(ctx, model.PullItem{
			Run: run.Reference, Action: model.PullFetch, Path: path, Phase: model.PullIntent, Version: 1, Updated: run.Started,
			Source: &artifactsapi.PullItem{
				Repository: binding.Source, Selection: binding.Selection(), Path: path, UpstreamPath: path,
				Size: 4, Actual: kernel.Checksums{SHA256: strings.Repeat("b", 64)}, Version: strings.Repeat("c", 64),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"selected/z", "selected/b", "selected/m"} {
		next, found, err := p.NextPending(ctx, run.Reference)
		if err != nil || !found || next.Path != want {
			t.Fatal("next pending intent", next.Path, want, found, err)
		}
		// Mark it finished directly; the order, not the transition rules,
		// is under test here.
		key, err := model.PullItemKey(next.Action, next.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Do(ctx, func() error {
			done := next
			done.Phase = model.PullItemComplete
			s.PullProgress().(pullProgress).items().Put(pullItemKey{run.Reference.ID, key}, done)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := p.NextPending(ctx, run.Reference); err != nil || found {
		t.Fatal("a completed intent stayed pending", found, err)
	}
}
