package sync_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type retryHook struct {
	streamsync.Remote
	before  func()
	failure error
}

func (r retryHook) FetchBlob(ctx context.Context, p model.Peer, sha string, into func(io.Reader) error) error {
	r.before()
	if r.failure != nil {
		return r.failure
	}
	return r.Remote.FetchBlob(ctx, p, sha, into)
}

func TestUnitOperatorDeletionCancelsInflightRetry(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "download succeeds", true: "download fails"}[fails], func(t *testing.T) {
			w, a, b := pushed(t)
			body, sum := content("operator cancels download")
			w.faults.fetchErr = errors.New("source unavailable")
			a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
			a.run(streamsync.SendKind)
			w.faults.fetchErr = nil
			hook := retryHook{Remote: b.engine.Remote, before: func() {
				if err := b.store.Outbox().ClearFailedFetch(t.Context(), stream, sum); err != nil {
					t.Fatal(err)
				}
			}}
			if fails {
				hook.failure = errors.New("still unavailable")
			}
			b.engine.Remote = hook
			if err := b.engine.RetryFetch(t.Context(), stream, sum); err != nil {
				t.Error(err)
			}
			if !b.state(a).Items["f"].Pending {
				t.Fatal("cancelled retry published content")
			}
			if fs := b.failed(); len(fs) != 0 {
				t.Fatalf("cancelled retry recreated failed task: %+v", fs)
			}
			b.engine.Remote = hook.Remote
			if err := b.engine.RetryFetch(t.Context(), stream, sum); err != nil {
				t.Error(err)
			}
			if !b.state(a).Items["f"].Pending {
				t.Fatal("queued retry published a deleted task")
			}
		})
	}
}

// changingPending models a newer event arriving between the first Pending
// read and the transaction that claims an obsolete failed record.
type changingPending struct {
	streamsync.Kind
	calls int
}

func (k *changingPending) Pending(context.Context, string, string) ([]int64, error) {
	k.calls++
	if k.calls > 1 {
		return []int64{3}, nil
	}
	return nil, nil
}

func TestUnitObsoleteFailureCleanupKeepsNewlyPendingTask(t *testing.T) {
	w, a, b := pushed(t)
	body, sum := content("pending while clearing")
	w.faults.fetchErr = errors.New("source unavailable")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
	a.run(streamsync.SendKind)
	k := &changingPending{Kind: b.kind}
	b.engine.Kinds = &streamsync.Registry{}
	if err := b.engine.Kinds.Register(k); err != nil {
		t.Fatal(err)
	}
	if err := b.engine.RetryFetch(t.Context(), stream, sum); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal(err)
	}
	if fs := b.failed(); len(fs) != 1 {
		t.Fatalf("new pending task was cleared: %+v", fs)
	}
}

func TestUnitRetryRechecksCurrentSourceGrant(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		t.Run(map[bool]string{false: "sole source revoked", true: "another source remains"}[alternate], func(t *testing.T) {
			w, a, b := pushed(t)
			if alternate {
				c := w.add(t)
				pair(t, b, c, fed, model.DeliveryPush, model.DeliveryPush)
			}
			body, sum := content("grant revoked during content fetch")
			w.faults.fetchErr = errors.New("source unavailable")
			a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
			a.run(streamsync.SendKind)
			w.faults.fetchErr = nil
			b.engine.Remote = retryHook{Remote: b.engine.Remote, before: func() {
				if err := b.store.Peers().Delete(t.Context(), a.id); err != nil {
					t.Fatal(err)
				}
			}}
			err := b.engine.RetryFetch(t.Context(), stream, sum)
			if alternate {
				if err != nil {
					t.Fatal(err)
				}
				if b.state(a).Items["f"].Pending || len(b.failed()) != 0 {
					t.Fatal("authorized alternate member did not allow publication")
				}
			} else {
				if !errors.Is(err, streamsync.ErrRefused) {
					t.Fatalf("retry did not refuse revoked source: %v", err)
				}
				if !b.state(a).Items["f"].Pending || len(b.failed()) != 1 {
					t.Fatal("revoked source published content or lost retry task")
				}
			}
		})
	}
}
