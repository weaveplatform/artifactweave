package peers_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccPeerStreamInstance(t *testing.T) { peers.RunInstanceWith(t, runStreamFixture) }

func TestAccPeerStreamSQLite(t *testing.T) {
	t.Parallel()
	testPeerStream(t, peers.SQLite)
}

func TestAccPeerStreamPostgres(t *testing.T) {
	t.Parallel()
	testPeerStream(t, peers.Postgres)
}

func streamCall(t *testing.T, in *peers.Instance, cmd streamCommand) streamReceipt {
	t.Helper()
	path := filepath.Join(streamControlDir(in.DataDir), rand.Text()+".request")
	if err := streamWriteJSON(path, cmd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := os.ReadFile(path + ".receipt")
		if err == nil {
			var receipt streamReceipt
			if err := json.Unmarshal(data, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Error != "" {
				t.Fatalf("%s stream %s: %s", in.Name, cmd.Operation, receipt.Error)
			}
			return receipt
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s stream %s produced no receipt", in.Name, cmd.Operation)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func streamObserve(t *testing.T, in *peers.Instance, origin string) streamReceipt {
	t.Helper()
	return streamCall(t, in, streamCommand{Operation: "observe", Origin: origin})
}

func streamSequence(from, through int64) []string {
	out := make([]string, 0, max(through-from+1, 0))
	for seq := from; seq <= through; seq++ {
		out = append(out, strconv.FormatInt(seq, 10))
	}
	return out
}

func streamAwaitHistory(t *testing.T, in *peers.Instance, origin string, want []string, position int64) streamReceipt {
	t.Helper()
	var result streamReceipt
	eventually(t, in, "exact stream history", func() (bool, string) {
		result = streamObserve(t, in, origin)
		if len(result.State.Applied) > len(want) {
			t.Fatalf("%s applied extra entries: %v, want %v", in.Name, result.State.Applied, want)
		}
		if len(result.State.Applied) < len(want) || result.Applied.LastSeq != position {
			return false, fmt.Sprintf("history %v position %d; want %v position %d", result.State.Applied, result.Applied.LastSeq, want, position)
		}
		if !slices.Equal(result.State.Applied, want) {
			t.Fatalf("%s applied %v, want %v once each in order", in.Name, result.State.Applied, want)
		}
		return true, ""
	})
	return result
}

func streamCommit(t *testing.T, in *peers.Instance, seq int64, change streamtest.Change) outbox.OutboxEntry {
	t.Helper()
	entry := streamCall(t, in, streamCommand{Operation: "commit", Change: change}).Entry
	if entry.Seq != seq {
		t.Fatalf("%s committed sequence %d, want %d", in.Name, entry.Seq, seq)
	}
	return entry
}

func streamSweep(t *testing.T, ins ...*peers.Instance) {
	t.Helper()
	for _, in := range ins {
		streamCall(t, in, streamCommand{Operation: "sweep"})
	}
}

func streamPut(key, value string) streamtest.Change {
	return streamtest.Change{Type: streamtest.Put, Key: key, Value: value}
}

func testPeerStream(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{
		Instances: 3, Driver: driver, Retention: time.Minute, ChildTest: "TestAccPeerStreamInstance",
	})
	a, b, third := c.Instances[0], c.Instances[1], c.Instances[2]
	pair(t, a, b)
	pair(t, a, third)
	pair(t, b, third)
	ids := make([]string, len(c.Instances))
	for i, in := range c.Instances {
		ids[i] = in.Identity(t).InstanceID
	}
	for i, in := range c.Instances {
		var subscribers []string
		for j, id := range ids {
			if i != j {
				subscribers = append(subscribers, id)
			}
		}
		streamCall(t, in, streamCommand{Operation: "subscribe", Peers: subscribers})
	}
	for seq := int64(1); seq <= 2; seq++ {
		streamCommit(t, a, seq, streamPut(fmt.Sprint("initial", seq), fmt.Sprint(seq)))
	}
	for _, in := range []*peers.Instance{b, third} {
		streamAwaitHistory(t, in, ids[0], streamSequence(1, 2), 2)
	}
	t.Log("three paired processes applied the initial prefix exactly once and in order")

	// Stop the OS process, not its HTTP handler. Its prior history and setup
	// must survive reopening the same database without assertion-driven seeding.
	oldIdentity, oldURL, oldData := b.Identity(t), b.URL, b.DataDir
	c.Stop(1)
	streamCommit(t, a, 3, streamPut("stopped3", "3"))
	streamCommit(t, a, 4, streamPut("stopped4", "4"))
	streamCommit(t, third, 1, streamPut("third1", "continues while B is stopped"))
	streamAwaitHistory(t, third, ids[0], streamSequence(1, 4), 4)
	streamAwaitHistory(t, a, ids[2], streamSequence(1, 1), 1)
	c.Restart(1)
	if b.URL != oldURL || b.DataDir != oldData || !reflect.DeepEqual(b.Identity(t), oldIdentity) {
		t.Fatal("restart changed B's ingress, data directory or stored identity")
	}
	streamSweep(t, a, b, third)
	streamAwaitHistory(t, b, ids[0], streamSequence(1, 4), 4)
	streamAwaitHistory(t, b, ids[2], streamSequence(1, 1), 1)
	t.Log("actual process stop/restart preserved identity, durable history and subscriptions; delivery caught up exactly")

	c.Partition(0, 1)
	c.Partition(1, 2)
	streamCommit(t, a, 5, streamPut("partition5", "5"))
	streamCommit(t, a, 6, streamPut("partition6", "6"))
	streamCommit(t, third, 2, streamPut("third2", "continues across B's partition"))
	streamAwaitHistory(t, third, ids[0], streamSequence(1, 6), 6)
	streamAwaitHistory(t, a, ids[2], streamSequence(1, 2), 2)
	streamAwaitHistory(t, b, ids[0], streamSequence(1, 4), 4)
	c.Heal(0, 1)
	c.Heal(1, 2)
	streamSweep(t, a, b, third)
	streamAwaitHistory(t, b, ids[0], streamSequence(1, 6), 6)
	streamAwaitHistory(t, b, ids[2], streamSequence(1, 2), 2)
	t.Log("partition isolated B while A and the third process continued; healing resumed exact ordered delivery")

	a.SetPeerDelivery(t, ids[1], model.DeliveryPulled)
	b.SetPeerDelivery(t, ids[0], model.DeliveryPull)
	c.Cut(0, 1)
	if _, _, err := c.DoFrom(0, 1, http.MethodGet, "/artifactory/api/system/ping", ""); err == nil {
		t.Fatal("A can reach B across the directional cut")
	}
	streamCommit(t, a, 7, streamPut("pull7", "7"))
	last := streamCommit(t, a, 8, streamPut("pull8", "8"))
	streamSweep(t, b)
	streamAwaitHistory(t, b, ids[0], streamSequence(1, 8), 8)
	streamAwaitHistory(t, third, ids[0], streamSequence(1, 8), 8)
	// The pull carrying entries acknowledges its starting position. A later
	// signed read acknowledges 8 before the partition fixes that cursor.
	if streamCall(t, b, streamCommand{Operation: "pull", Origin: ids[0], After: 8}).Gone {
		t.Fatal("freshly applied pull position was already pruned")
	}
	// Exercise an actual signed duplicate batch. A replay at the stream
	// layer uses a fresh transport nonce and must not apply another effect.
	c.Restore(0, 1)
	duplicate := streamCall(t, a, streamCommand{Operation: "duplicate", Origin: ids[1], Entry: last})
	if duplicate.Expected != 9 {
		t.Fatalf("duplicate expected sequence %d, want 9", duplicate.Expected)
	}
	streamAwaitHistory(t, b, ids[0], streamSequence(1, 8), 8)
	t.Log("signed pull crossed a one-way cut; a signed duplicate was refused with expected sequence 9 and no additional application")

	// Keep B unable to read A until the real scheduler prunes beyond B's
	// acknowledged position. No clock substitution or direct prune is used.
	c.Partition(0, 1)
	c.Partition(1, 2)
	b.SetPeerDelivery(t, ids[0], model.DeliveryPush)
	streamCommit(t, a, 9, streamPut("snapshot9", "9"))
	streamCommit(t, a, 10, streamtest.Change{Type: streamtest.Put, Key: "snapshot-blob", Content: []byte("snapshot blob content")})
	streamCommit(t, a, 11, streamtest.Change{Type: streamtest.Delete, Key: "initial1"})
	streamAwaitHistory(t, third, ids[0], streamSequence(1, 11), 11)
	var pruned streamReceipt
	eventually(t, a, "real retention pruning beyond B's cursor", func() (bool, string) {
		pruned = streamObserve(t, a, ids[1])
		flagged := slices.ContainsFunc(pruned.Flags, func(f model.StreamResync) bool {
			return f.Origin == ids[0] && f.Receiver == ids[1] && f.Stream == qualificationStream && f.Position == 8
		})
		old := slices.ContainsFunc(pruned.Retained, func(e outbox.OutboxEntry) bool { return e.Seq <= 11 })
		return flagged && !old && pruned.Head == 11, fmt.Sprintf("head %d retained %d flags %+v", pruned.Head, len(pruned.Retained), pruned.Flags)
	})
	c.Restore(1, 0)
	probe := streamCall(t, b, streamCommand{Operation: "pull", Origin: ids[0], After: 8})
	if !probe.Gone {
		t.Fatal("signed pull below the actual pruned window did not return 410")
	}
	streamAwaitHistory(t, b, ids[0], streamSequence(1, 8), 8)
	b.SetPeerDelivery(t, ids[0], model.DeliveryPull)
	streamSweep(t, b)
	want := streamObserve(t, a, ids[0]).State
	var recovered streamReceipt
	eventually(t, b, "completed production snapshot recovery", func() (bool, string) {
		recovered = streamObserve(t, b, ids[0])
		complete := slices.ContainsFunc(recovered.Runs, func(r model.FullSyncRun) bool {
			return r.PeerInstanceID == ids[0] && r.Stream == qualificationStream &&
				!r.Forced && r.Status == retentionapi.RunCompleted && r.SnapshotSeq == 11
		})
		return complete && recovered.Applied.LastSeq == 11 && recovered.BlobPresent &&
				reflect.DeepEqual(recovered.State.Items, want.Items) && reflect.DeepEqual(recovered.State.Stamps, want.Stamps),
			fmt.Sprintf("position %d history %v runs %+v", recovered.Applied.LastSeq, recovered.State.Applied, recovered.Runs)
	})
	if !slices.Equal(recovered.State.Applied, streamSequence(1, 8)) {
		t.Fatalf("snapshot manufactured or repeated event history: %v", recovered.State.Applied)
	}
	if _, exists := recovered.State.Items["initial1"]; exists || !recovered.State.Stamps["initial1"].Deleted {
		t.Fatal("snapshot failed to recover the deletion tombstone")
	}
	t.Log("real one-minute retention pruned unapplied entries; signed pull returned 410; completed snapshot 11 restored items, stamps and blob without inventing event history")

	streamCommit(t, a, 12, streamPut("after12", "12"))
	streamCommit(t, a, 13, streamPut("after13", "13"))
	streamSweep(t, b)
	history := append(streamSequence(1, 8), streamSequence(12, 13)...)
	streamAwaitHistory(t, b, ids[0], history, 13)
	streamAwaitHistory(t, third, ids[0], streamSequence(1, 13), 13)
	c.Heal(0, 1)
	c.Heal(1, 2)
	streamSweep(t, a, b, third)
	streamAwaitHistory(t, b, ids[0], history, 13)
	eventually(t, a, "post-snapshot acknowledgment and cleared resync flags", func() (bool, string) {
		sender := streamObserve(t, a, ids[1])
		receiver := streamObserve(t, b, ids[0])
		return sender.Cursor.AckedSeq == 13 && len(sender.Flags) == 0 && len(receiver.Flags) == 0,
			fmt.Sprintf("cursor %d flags %d/%d", sender.Cursor.AckedSeq, len(sender.Flags), len(receiver.Flags))
	})
	t.Log("post-snapshot entries 12 and 13 applied exactly once in order, acknowledged at 13, with cleared resync flags")
}
