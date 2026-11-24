package sync_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	gosync "sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var fed = []model.UseCase{model.UseFederatedRepo}

// pushed is two instances where A pushes to B.
func pushed(t *testing.T) (*world, *node, *node) {
	t.Helper()
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	return w, a, b
}

func TestUnitPushAppliesInOrderOnce(t *testing.T) {
	w, a, b := pushed(t)
	for i := range 5 {
		a.put("k", string(rune('a'+i)))
	}
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 5))
	if got := b.state(a).Items["k"].Value; got != "e" {
		t.Fatalf("k is %q, want the last put", got)
	}
	if a.cursor(b) != 5 {
		t.Fatalf("cursor %d, want 5", a.cursor(b))
	}

	// A duplicated batch, and one retried from its middle, are refused with
	// the next sequence and change nothing.
	entries := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 10))
	for _, from := range []int64{1, 3} {
		_, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
			Origin: a.id, Stream: stream, FromSeq: from, Entries: entries[from-1:],
		})
		var conflict *streamsync.ConflictError
		if !errors.As(err, &conflict) || conflict.Expected != 6 {
			t.Fatalf("retried batch from %d: %v, want a conflict expecting 6", from, err)
		}
	}
	wantApplied(t, b.applied(a), seqs(1, 5))

	// Nothing more to send sends nothing.
	before := w.pushes
	if err := a.engine.Dispatch(t.Context(), b.id, stream); err != nil {
		t.Fatal(err)
	}
	if w.pushes != before {
		t.Fatal("a caught-up peer was pushed to")
	}
}

func TestUnitPushGapRefusedAndSenderRecovers(t *testing.T) {
	_, a, b := pushed(t)
	for range 3 {
		a.put("k", "v")
	}
	a.run(streamsync.SendKind)

	// A batch past the next sequence is a gap.
	a.put("k", "4")
	a.put("k", "5")
	entries := must(a.store.Outbox().Read(t.Context(), a.id, stream, 4, 10))
	_, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
		Origin: a.id, Stream: stream, FromSeq: 5, Entries: entries,
	})
	var conflict *streamsync.ConflictError
	if !errors.As(err, &conflict) || conflict.Expected != 4 {
		t.Fatalf("gap: %v, want a conflict expecting 4", err)
	}
	if conflict.Error() == "" {
		t.Fatal("conflict has no message")
	}

	// A sender whose cursor is behind what the receiver applied, as after a
	// lost acknowledgement, resumes from the expected sequence.
	c := must(a.store.Outbox().Cursor(t.Context(), b.id, stream))
	c.AckedSeq = 1
	if err := a.store.Outbox().PutCursor(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := a.engine.Dispatch(t.Context(), b.id, stream); err != nil {
		t.Fatal(err)
	}
	wantApplied(t, b.applied(a), seqs(1, 5))
	if a.cursor(b) != 5 {
		t.Fatalf("cursor %d, want 5", a.cursor(b))
	}
}

func TestUnitPushDroppedAnswerResumes(t *testing.T) {
	w, a, b := pushed(t)
	a.put("k", "1")
	a.put("k", "2")
	w.faults.dropAnswer = 1
	if err := a.engine.Dispatch(t.Context(), b.id, stream); !errors.Is(err, errDropped) {
		t.Fatalf("dispatch: %v, want the dropped answer", err)
	}
	// B applied the batch; A does not know, and records why.
	wantApplied(t, b.applied(a), seqs(1, 2))
	c := must(a.store.Outbox().Cursor(t.Context(), b.id, stream))
	if c.AckedSeq != 0 || c.LastError == "" {
		t.Fatalf("cursor %+v, want 0 with the failure", c)
	}
	a.put("k", "3")
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 3))
	c = must(a.store.Outbox().Cursor(t.Context(), b.id, stream))
	if c.AckedSeq != 3 || c.LastError != "" {
		t.Fatalf("cursor %+v, want 3 and no failure", c)
	}
}

func TestUnitStoppedReceiverResumes(t *testing.T) {
	w, a, b := pushed(t)
	a.put("k", "1")
	a.run(streamsync.SendKind)
	w.faults.down[b.id] = true
	a.put("k", "2")
	a.put("k", "3")
	if err := a.engine.Dispatch(t.Context(), b.id, stream); err == nil {
		t.Fatal("a stopped receiver was reached")
	}
	delete(w.faults.down, b.id)
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 3))
}

func TestUnitRolledBackAppendNeverDelivered(t *testing.T) {
	_, a, b := pushed(t)
	a.put("k", "1")
	boom := errors.New("the change failed")
	if _, err := a.kind.Commit(t.Context(), a.engine, stream, streamtest.Change{
		Type: streamtest.Put, Key: "lost", Value: "x",
	}, boom); !errors.Is(err, boom) {
		t.Fatalf("commit: %v", err)
	}
	a.put("k", "2")
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 2))
	if _, ok := b.state(a).Items["lost"]; ok {
		t.Fatal("a rolled-back change was delivered")
	}
}

func TestUnitConcurrentAppliersApplyOnce(t *testing.T) {
	_, a, b := pushed(t)
	for range 4 {
		a.put("k", "v")
	}
	entries := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 10))
	var wg gosync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, _ = b.engine.Receive(context.Background(), b.record(a.id), streamsync.Batch{
				Origin: a.id, Stream: stream, FromSeq: 1, Entries: entries,
			})
		})
	}
	wg.Wait()
	wantApplied(t, b.applied(a), seqs(1, 4))
}

func TestUnitPullModeReachesPushState(t *testing.T) {
	w := newWorld()
	a, b, c := w.add(t), w.add(t), w.add(t)
	// B cannot be reached: A records it as pulling, B records A as pulled
	// from. C is pushed to.
	pair(t, a, b, fed, model.DeliveryPulled, model.DeliveryPull)
	put(t, a, c, fed, model.DeliveryPush)
	put(t, c, a, fed, model.DeliveryPush)
	a.kind.Subscribe(stream, b.id, c.id)
	c.kind.Subscribe(stream, a.id)
	for i := range 7 {
		a.put("k"+string(rune('0'+i%3)), string(rune('a'+i)))
	}
	a.engine.Batch = 3
	b.engine.Batch = 3
	a.run(streamsync.SendKind)
	if a.cursor(b) != 0 {
		t.Fatal("a pulled peer was pushed to")
	}
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	wantApplied(t, b.applied(a), seqs(1, 7))
	wantApplied(t, c.applied(a), seqs(1, 7))
	if !equalItems(b.state(a).Items, c.state(a).Items) {
		t.Fatalf("pull state %v, push state %v", b.state(a).Items, c.state(a).Items)
	}
	// The pull acknowledged what it had applied before its last page;
	// the next pull acknowledges the rest.
	if err := b.engine.PullFrom(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
	if a.cursor(b) != 7 {
		t.Fatalf("A's cursor for B %d, want 7", a.cursor(b))
	}
	// An acknowledgement never moves back.
	if _, err := a.engine.Events(t.Context(), a.record(b.id), stream, 2, 10); err != nil {
		t.Fatal(err)
	}
	if a.cursor(b) != 7 {
		t.Fatalf("cursor moved back to %d", a.cursor(b))
	}
	// A instance A pushes to is never pulled.
	if err := c.engine.PullFrom(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
}

func equalItems(a, b map[string]streamtest.Item) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestUnitEventsPaging(t *testing.T) {
	_, a, b := pushed(t)
	for range 5 {
		a.put("k", "v")
	}
	page, err := a.engine.Events(t.Context(), a.record(b.id), stream, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Origin != a.id || len(page.Entries) != 2 || page.Entries[0].Seq != 2 || page.NextAfter != 3 {
		t.Fatalf("page %+v", page)
	}
	page = must(a.engine.Events(t.Context(), a.record(b.id), stream, 3, 2))
	if page.NextAfter != 0 || len(page.Entries) != 2 {
		t.Fatalf("last page %+v", page)
	}
	page = must(a.engine.Events(t.Context(), a.record(b.id), stream, 5, 2))
	if len(page.Entries) != 0 || page.NextAfter != 0 {
		t.Fatalf("caught-up page %+v", page)
	}
	for _, bad := range []struct {
		after int64
		limit int
	}{{0, 0}, {0, 1001}, {-1, 10}, {6, 10}} {
		if _, err := a.engine.Events(t.Context(), a.record(b.id), stream, bad.after, bad.limit); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("after %d limit %d: %v, want invalid", bad.after, bad.limit, err)
		}
	}
}

// content is a blob's content and SHA-256.
func content(s string) ([]byte, string) {
	sum := sha256.Sum256([]byte(s))
	return []byte(s), hex.EncodeToString(sum[:])
}

func TestUnitBlobFetchedAndVerified(t *testing.T) {
	w, a, b := pushed(t)
	body, sum := content("the content")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
	a.run(streamsync.SendKind)
	it := b.state(a).Items["f"]
	if it.Pending || it.SHA256 != sum {
		t.Fatalf("item %+v, want the visible content", it)
	}
	if ok := must(b.blobs.Exists(t.Context(), sum)); !ok {
		t.Fatal("the blob was not fetched")
	}
	// Content already held is not fetched again.
	fetches := w.fetches
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "g", Content: body})
	a.run(streamsync.SendKind)
	if w.fetches != fetches {
		t.Fatal("held content was fetched again")
	}
	// B serves what it holds to A, which receives the stream from it.
	rc, info, err := b.engine.Blob(t.Context(), b.record(a.id), sum)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != string(body) || info.Size != int64(len(body)) {
		t.Fatalf("served %q (%d)", got, info.Size)
	}
}

func TestUnitFailedFetchAdvancesAndRetryPublishes(t *testing.T) {
	for _, fault := range []string{"corrupt", "truncate", "unavailable", "size"} {
		t.Run(fault, func(t *testing.T) {
			w, a, b := pushed(t)
			body, sum := content("payload for " + fault)
			change := streamtest.Change{Type: streamtest.Put, Key: "f", Content: body}
			switch fault {
			case "corrupt":
				w.faults.corrupt = true
			case "truncate":
				w.faults.truncate = true
			case "unavailable":
				w.faults.fetchErr = errors.New("503")
			case "size":
				// The entry gives another size than the content has.
				must(a.kind.Commit(t.Context(), a.engine, "test:other", streamtest.Change{
					Type: streamtest.Put, Key: "seed", Content: body,
				}, nil))
				change = streamtest.Change{Type: streamtest.Put, Key: "f", SHA256: sum, Size: int64(len(body)) + 1}
			}
			a.commit(change)
			a.put("after", "applied")
			a.run(streamsync.SendKind)
			wantApplied(t, b.applied(a), seqs(1, 2))
			if it := b.state(a).Items["f"]; !it.Pending {
				t.Fatalf("item %+v, want pending", it)
			}
			if ok := must(b.blobs.Exists(t.Context(), sum)); ok {
				t.Fatal("bad content was stored")
			}
			failed := b.failed()
			if len(failed) != 1 || failed[0].SHA256 != sum || failed[0].LastError == "" {
				t.Fatalf("failed fetches %+v", failed)
			}
			w.faults = faults{down: map[string]bool{}}
			if fault == "size" {
				// The entry's size is wrong for good: the retry holds the
				// content to it too, so the item never publishes and its
				// failed binary stays for FED-06.
				if err := b.engine.RetryFetch(t.Context(), stream, sum); err == nil {
					t.Fatal("content of another size than its entry published")
				}
				if !b.state(a).Items["f"].Pending || len(b.failed()) != 1 {
					t.Fatalf("item %+v, failed %+v", b.state(a).Items["f"], b.failed())
				}
				return
			}
			b.run(streamsync.FetchKind)
			if it := b.state(a).Items["f"]; it.Pending {
				t.Fatalf("item %+v, want published", it)
			}
			if len(b.failed()) != 0 {
				t.Fatalf("failed fetches %+v remain", b.failed())
			}
		})
	}
}

func TestUnitRetryNeverShowsDeletedOrReplaced(t *testing.T) {
	w, a, b := pushed(t)
	gone, goneSum := content("deleted meanwhile")
	old, oldSum := content("replaced meanwhile")
	w.faults.fetchErr = errors.New("unavailable")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "gone", Content: gone})
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "replaced", Content: old})
	a.run(streamsync.SendKind)
	w.faults.fetchErr = nil
	a.commit(streamtest.Change{Type: streamtest.Delete, Key: "gone"})
	a.put("replaced", "new")
	a.run(streamsync.SendKind)
	b.run(streamsync.FetchKind)
	items := b.state(a).Items
	if _, ok := items["gone"]; ok {
		t.Fatal("a deleted item appeared")
	}
	if it := items["replaced"]; it.SHA256 != "" || it.Value != "new" || it.Pending {
		t.Fatalf("replaced item %+v", it)
	}
	if failed := b.failed(); len(failed) != 0 {
		t.Fatalf("failed fetches %+v remain for items nothing waits for", failed)
	}
	for _, sum := range []string{goneSum, oldSum} {
		if ok := must(b.blobs.Exists(t.Context(), sum)); ok {
			t.Fatal("content nothing waits for was fetched")
		}
	}
}

func TestUnitRetryFailsUntilFetched(t *testing.T) {
	w, a, b := pushed(t)
	body, sum := content("late")
	w.faults.fetchErr = errors.New("unavailable")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
	a.run(streamsync.SendKind)
	if err := b.engine.RetryFetch(t.Context(), stream, sum); err == nil {
		t.Fatal("a failing retry succeeded")
	}
	// Nothing failed, or no such kind, is nothing to do.
	if err := b.engine.RetryFetch(t.Context(), stream, "00"); err != nil {
		t.Fatal(err)
	}
	if err := b.engine.RetryFetch(t.Context(), "nokind:x", sum); err != nil {
		t.Fatal(err)
	}
}

func TestUnitRefusals(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	body, sum := content("x")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
	entries := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 10))
	batch := streamsync.Batch{Origin: a.id, Stream: stream, FromSeq: 1, Entries: entries}

	stranger := model.Peer{InstanceID: "jfrt@stranger", UseCases: fed}
	without := b.record(a.id)
	without.UseCases = []model.UseCase{model.UseDistribution}
	for _, c := range []struct {
		name   string
		caller model.Peer
		want   error
	}{
		{"unsubscribed", stranger, streamsync.ErrNoStream},
		{"no use case", without, streamsync.ErrRefused},
	} {
		if _, err := b.engine.Receive(t.Context(), c.caller, batch); !errors.Is(err, c.want) {
			t.Errorf("%s push: %v, want %v", c.name, err, c.want)
		}
		if _, err := a.engine.Events(t.Context(), c.caller, stream, 0, 10); !errors.Is(err, c.want) {
			t.Errorf("%s pull: %v, want %v", c.name, err, c.want)
		}
		if _, _, err := a.engine.Blob(t.Context(), c.caller, sum); !errors.Is(err, streamsync.ErrNoStream) {
			t.Errorf("%s blob: %v, want no stream", c.name, err)
		}
	}
	// A stream of no kind, an origin that is not the caller, content no
	// stream refers to and a malformed SHA-256.
	if _, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
		Origin: a.id, Stream: "nokind:x", FromSeq: 1, Entries: entries,
	}); !errors.Is(err, streamsync.ErrNoStream) {
		t.Errorf("no kind: %v", err)
	}
	if _, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
		Origin: "jfrt@other", Stream: stream, FromSeq: 1, Entries: entries,
	}); !errors.Is(err, streamsync.ErrRefused) {
		t.Errorf("other origin: %v", err)
	}
	_, other := content("not referenced")
	if _, _, err := a.engine.Blob(t.Context(), a.record(b.id), other); !errors.Is(err, streamsync.ErrNoStream) {
		t.Errorf("unreferenced blob: %v", err)
	}
	if _, _, err := a.engine.Blob(t.Context(), a.record(b.id), "ABC"); !errors.Is(err, kernel.ErrInvalid) {
		t.Errorf("malformed sha256: %v", err)
	}

	// The grant is read again when the entry applies: a caller whose
	// record lost the use case since its request verified applies nothing.
	put(t, b, a, []model.UseCase{model.UseDistribution}, model.DeliveryPush)
	if _, err := b.engine.Receive(t.Context(), model.Peer{InstanceID: a.id, UseCases: fed}, batch); !errors.Is(err, streamsync.ErrRefused) {
		t.Errorf("regrant: %v, want refused", err)
	}
	if n := len(b.applied(a)); n != 0 {
		t.Fatalf("applied %d entries for a peer without the grant", n)
	}
	// An unpaired caller is the same.
	if err := b.store.Peers().Delete(t.Context(), a.id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.engine.Receive(t.Context(), model.Peer{InstanceID: a.id, UseCases: fed}, batch); !errors.Is(err, streamsync.ErrRefused) {
		t.Errorf("unpaired at apply: %v, want refused", err)
	}
	// A dispatcher or pull to a peer that is gone, or may not have the
	// stream, does nothing.
	for _, id := range []string{"jfrt@gone", b.id} {
		if err := a.engine.Dispatch(t.Context(), id, "nokind:x"); err != nil {
			t.Error(err)
		}
		if err := a.engine.PullFrom(t.Context(), id, stream); err != nil {
			t.Error(err)
		}
	}
}

func TestUnitInvalidBatches(t *testing.T) {
	_, a, b := pushed(t)
	a.put("k", "v")
	good := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 10))[0]
	mutate := func(f func(*outbox.OutboxEntry)) []outbox.OutboxEntry {
		en := good
		f(&en)
		return []outbox.OutboxEntry{en}
	}
	for name, entries := range map[string][]outbox.OutboxEntry{
		"empty":       nil,
		"seq":         mutate(func(en *outbox.OutboxEntry) { en.Seq = 2 }),
		"no type":     mutate(func(en *outbox.OutboxEntry) { en.EventType = "" }),
		"array":       mutate(func(en *outbox.OutboxEntry) { en.Payload = []byte(`[1]`) }),
		"bad sha":     mutate(func(en *outbox.OutboxEntry) { en.BlobSHA256 = "xyz" }),
		"neg size":    mutate(func(en *outbox.OutboxEntry) { en.BlobSHA256, en.BlobSize = good.Origin, -1 }),
		"size no sha": mutate(func(en *outbox.OutboxEntry) { en.BlobSize = 4 }),
		"too many":    make([]outbox.OutboxEntry, streamsync.MaxBatch+1),
	} {
		if _, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
			Origin: a.id, Stream: stream, FromSeq: 1, Entries: entries,
		}); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
	// Append checks the same, and the stream's kind.
	if _, err := a.engine.Append(t.Context(), outbox.OutboxEntry{Stream: stream}); !errors.Is(err, kernel.ErrInvalid) {
		t.Errorf("append without a type: %v", err)
	}
	if _, err := a.engine.Append(t.Context(), outbox.OutboxEntry{Stream: "nokind:x", EventType: "put"}); err == nil {
		t.Error("append to a stream of no kind")
	}
}

func TestUnitPruneKeepsUnacknowledgedAndFlags(t *testing.T) {
	w := newWorld()
	a, b, c := w.add(t), w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	put(t, a, c, fed, model.DeliveryPush)
	put(t, c, a, fed, model.DeliveryPush)
	a.kind.Subscribe(stream, b.id, c.id)
	c.kind.Subscribe(stream, a.id)
	for range 4 {
		a.put("k", "v")
	}
	a.run(streamsync.SendKind)
	w.faults.down[b.id] = true
	for range 6 {
		a.put("k", "v")
	}
	a.run(streamsync.SendKind)
	if a.cursor(b) != 4 || a.cursor(c) != 10 {
		t.Fatalf("cursors B %d C %d", a.cursor(b), a.cursor(c))
	}

	// Inside the window only what every peer acknowledged goes.
	a.run(streamsync.PruneKind)
	left := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 100))
	if len(left) != 6 || left[0].Seq != 5 {
		t.Fatalf("kept %d entries from %d, want 5 to 10", len(left), left[0].Seq)
	}
	if len(a.resyncs()) != 0 {
		t.Fatalf("flags %+v inside the window", a.resyncs())
	}

	// Past the window everything goes, and B, still at 4, is flagged.
	*a.clock = t0.Add(streamsync.DefaultRetention + time.Hour)
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if left := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 100)); len(left) != 0 {
		t.Fatalf("kept %d expired entries", len(left))
	}
	flags := a.resyncs()
	if len(flags) != 1 || flags[0].Receiver != b.id || flags[0].Origin != a.id || flags[0].Position != 4 {
		t.Fatalf("flags %+v, want B at 4", flags)
	}
	// A prune that removes nothing changes no flag.
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}

	// B's pull from below the floor is gone, as is a push to it.
	if _, err := a.engine.Events(t.Context(), a.record(b.id), stream, 4, 10); !errors.Is(err, streamsync.ErrGone) {
		t.Fatalf("pull below the floor: %v, want gone", err)
	}
	delete(w.faults.down, b.id)
	a.put("k", "11")
	if err := a.engine.Dispatch(t.Context(), b.id, stream); err != nil {
		t.Fatal(err)
	}
	wantApplied(t, b.applied(a), seqs(1, 4))

	// A receiver pulling from below the floor flags itself.
	put(t, b, a, fed, model.DeliveryPull)
	if err := b.engine.PullFrom(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
	flags = b.resyncs()
	if len(flags) != 1 || flags[0].Origin != a.id || flags[0].Receiver != b.id {
		t.Fatalf("receiver flags %+v", flags)
	}
}

func TestUnitPruneExpiresAcrossPages(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	w.faults.down[b.id] = true
	for range streamsync.MaxBatch + 5 {
		a.put("k", "v")
	}
	*a.clock = t0.Add(time.Minute)
	a.put("k", "fresh")
	*a.clock = t0.Add(streamsync.DefaultRetention + 30*time.Second)
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	left := must(a.store.Outbox().Read(t.Context(), a.id, stream, 0, 10))
	if len(left) != 1 || left[0].Seq != streamsync.MaxBatch+6 {
		t.Fatalf("kept %+v, want only the fresh entry", left)
	}
}

func TestUnitSweepStartsMissedWork(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	a.put("k", "before B was subscribed")
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	a.engine.Priority = func(_ context.Context, s string) int { return len(s) }
	a.kind.Subscribe("test:z", "jfrt@unknown")
	a.run(streamsync.SweepKind)
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 1))
}

func TestUnitRegistry(t *testing.T) {
	reg := &streamsync.Registry{}
	k := &streamtest.Kind{}
	if err := reg.Register(k); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(k); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, name := range []string{"", "a:b"} {
		if err := reg.Register(named{k, name}); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
	if _, ok := reg.For("test:x"); !ok {
		t.Fatal("test:x has no kind")
	}
	if _, ok := reg.For("nocolon"); ok {
		t.Fatal("a stream without a kind prefix has one")
	}
	if streamsync.KindOf("repo:libs") != "repo" {
		t.Fatal("KindOf")
	}
}

type named struct {
	*streamtest.Kind
	name string
}

func (n named) Name() string { return n.name }

func TestUnitWorkRegistration(t *testing.T) {
	w := newWorld()
	a := w.add(t)
	// The periodic items are queued once, and a second registration on the
	// same queue is refused.
	for _, kind := range []string{streamsync.SweepKind, streamsync.PruneKind} {
		if _, err := a.store.WorkQueue().Get(t.Context(), kind, "streams"); err != nil {
			t.Fatalf("%s not queued: %v", kind, err)
		}
	}
	if err := a.engine.Register(t.Context(), a.queue, a.store.WorkQueue()); err == nil {
		t.Fatal("a second registration was accepted")
	}
	// An item without a stream is parked at once, not retried.
	for _, kind := range []string{streamsync.SendKind, streamsync.PullKind, streamsync.FetchKind} {
		if err := a.store.WorkQueue().Enqueue(t.Context(), kind, "bad", []byte(`{}`), t0); err != nil {
			t.Fatal(err)
		}
		if _, err := a.queue.RunDue(t.Context(), kind); err != nil {
			t.Fatal(err)
		}
		it := must(a.store.WorkQueue().Get(t.Context(), kind, "bad"))
		if it.State != work.WorkParked {
			t.Fatalf("%s item %s, want parked", kind, it.State)
		}
	}
}
