package sync_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

func TestUnitFullSyncChecksEveryDeclaredSize(t *testing.T) {
	for _, badFirst := range []bool{false, true} {
		for _, delta := range []int64{-1, 1} {
			t.Run(fmt.Sprintf("bad-first=%t/delta=%d", badFirst, delta), func(t *testing.T) {
				w, a, b := pushed(t)
				body, sum := content("shared content in a snapshot")
				good, bad := "0-good", "1-bad"
				if badFirst {
					good, bad = bad, good
				}
				a.commit(streamtest.Change{Type: streamtest.Put, Key: good, Content: body})
				a.commit(streamtest.Change{Type: streamtest.Put, Key: bad, SHA256: sum, Size: int64(len(body)) + delta})
				a.commit(streamtest.Change{Type: streamtest.Put, Key: "2-same", SHA256: sum, Size: int64(len(body))})
				if _, err := b.engine.FullSync(t.Context(), a.id, stream, false); err != nil {
					t.Fatal(err)
				}
				b.run(streamsync.FullSyncKind)
				state := b.state(a)
				if !state.Items[bad].Pending || state.Items[good].Pending || state.Items["2-same"].Pending {
					t.Fatalf("snapshot sizes borrowed validation: %+v", state.Items)
				}
				if runs := b.runs(); len(runs) != 1 || runs[0].Status != retentionapi.RunCompletedWithErrors {
					t.Fatalf("size snapshot runs: %+v", runs)
				}
				wantFetches := 1
				if badFirst {
					wantFetches++ // the rejected size was attempted before the valid one
				}
				if w.fetches != wantFetches {
					t.Fatalf("%d fetches, want %d with equal valid references coalesced", w.fetches, wantFetches)
				}
				if err := b.engine.RetryFetch(t.Context(), stream, sum); !errors.Is(err, streamsync.ErrFetchUnavailable) {
					t.Fatalf("retry conflicting size: %v", err)
				}
				if !b.state(a).Items[bad].Pending || len(b.failed()) != 1 {
					t.Fatalf("retry published mismatch: %+v; failed %+v", b.state(a).Items, b.failed())
				}
				r, info, err := b.blobs.Open(t.Context(), sum)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				if closeErr := r.Close(); err == nil {
					err = closeErr
				}
				if err != nil || string(got) != string(body) || info.Size != int64(len(body)) {
					t.Fatalf("valid content changed: %q %+v %v", got, info, err)
				}
				if failed := b.failed(); !strings.Contains(failed[0].LastError, "size mismatch") {
					t.Fatalf("failure is not a size mismatch: %+v", failed)
				}
				// An unrelated entry still follows the snapshot's sequence.
				a.put("later", "after the mismatch")
				a.run(streamsync.SendKind)
				if b.state(a).Items["later"].Value != "after the mismatch" {
					t.Fatal("size mismatch blocked the later event")
				}
			})
		}
	}
}

func pendingSizes(t *testing.T) (*world, *node, *node, string) {
	t.Helper()
	w, a, b := pushed(t)
	body, sum := content("one digest with conflicting pending sizes")
	w.faults.fetchErr = errors.New("source unavailable")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "good", Content: body})
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "bad", SHA256: sum, Size: int64(len(body)) + 1})
	a.run(streamsync.SendKind)
	w.faults.fetchErr = nil
	return w, a, b, sum
}

func TestUnitPartialSizePublicationRollsBackOnRemainingReadFailure(t *testing.T) {
	w, a, b, sum := pendingSizes(t)
	before := b.failed()[0]
	// The first read supplies validation requirements; the second follows
	// publication inside the claim transaction and must roll everything back.
	w.inject.arm("Kind.Pending", 1)
	if err := b.engine.RetryFetch(t.Context(), stream, sum); !errors.Is(err, errBoom) {
		t.Fatalf("remaining-size read: %v", err)
	}
	for _, key := range []string{"good", "bad"} {
		if !b.state(a).Items[key].Pending {
			t.Fatalf("%s publication survived rollback", key)
		}
	}
	if after := b.failed()[0]; after.Failures != before.Failures || !after.FirstFailed.Equal(before.FirstFailed) {
		t.Fatalf("failed task changed across rollback: before %+v after %+v", before, after)
	}
	w.inject.arm("", 0)
	if err := b.engine.RetryFetch(t.Context(), stream, sum); !errors.Is(err, streamsync.ErrFetchUnavailable) {
		t.Fatalf("partial retry: %v", err)
	}
	if b.state(a).Items["good"].Pending || !b.state(a).Items["bad"].Pending {
		t.Fatalf("partial publication: %+v", b.state(a).Items)
	}
	if after := b.failed()[0]; after.Failures != before.Failures+1 || !after.FirstFailed.Equal(before.FirstFailed) {
		t.Fatalf("partial retry did not preserve task history: before %+v after %+v", before, after)
	}
}

func TestUnitOperatorDeletionCancelsPartialSizePublication(t *testing.T) {
	_, a, b, sum := pendingSizes(t)
	b.engine.Remote = retryHook{Remote: b.engine.Remote, before: func() {
		if err := b.store.Outbox().ClearFailedFetch(t.Context(), stream, sum); err != nil {
			t.Fatal(err)
		}
	}}
	if err := b.engine.RetryFetch(t.Context(), stream, sum); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"good", "bad"} {
		if !b.state(a).Items[key].Pending {
			t.Fatalf("deleted task published %s", key)
		}
	}
	if len(b.failed()) != 0 {
		t.Fatalf("partial retry recreated deleted task: %+v", b.failed())
	}
}

func TestUnitRevocationCancelsPartialSizePublication(t *testing.T) {
	_, a, b, sum := pendingSizes(t)
	b.engine.Remote = retryHook{Remote: b.engine.Remote, before: func() {
		if err := b.store.Peers().Delete(t.Context(), a.id); err != nil {
			t.Fatal(err)
		}
	}}
	if err := b.engine.RetryFetch(t.Context(), stream, sum); !errors.Is(err, streamsync.ErrRefused) {
		t.Fatalf("partial publication with revoked source: %v", err)
	}
	for _, key := range []string{"good", "bad"} {
		if !b.state(a).Items[key].Pending {
			t.Fatalf("revoked source published %s", key)
		}
	}
	if len(b.failed()) != 1 {
		t.Fatalf("revocation lost the mixed task: %+v", b.failed())
	}
}
