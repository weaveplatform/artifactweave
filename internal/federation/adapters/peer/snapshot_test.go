package peer_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func TestUnitStreamSnapshot(t *testing.T) {
	// An integer past 2^53 is carried as the origin wrote it.
	const payload = `{"n":12345678901234567891}`
	sum := strings.Repeat("cd", 32)
	var asked []any
	f := &fakeStreams{snapshot: func(stream, cursor string, limit int) (streamsync.SnapshotPage, error) {
		asked = []any{stream, cursor, limit}
		return streamsync.SnapshotPage{Origin: serverID, Stream: stream, Sequence: 42, Entries: []model.SnapshotEntry{
			{Key: "a", Time: hlc, Origin: serverID, Payload: []byte(payload), BlobSHA256: sum, BlobSize: 3},
			{Key: "b", Time: hlc, Origin: callerID, Deleted: true},
		}, NextCursor: "next"}, nil
	}}
	rm, sr := streamRemote(t, f)
	page, err := sr.Snapshot(t.Context(), rm.peer, "test:s", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(asked) != fmt.Sprint([]any{"test:s", "", 2}) || f.caller.InstanceID != callerID {
		t.Fatalf("asked %v by %s", asked, f.caller.InstanceID)
	}
	if page.Origin != serverID || page.Stream != "test:s" || page.Sequence != 42 || page.NextCursor != "next" || len(page.Entries) != 2 {
		t.Fatalf("page %+v", page)
	}
	a, b := page.Entries[0], page.Entries[1]
	if a.Key != "a" || a.Time != hlc || a.Origin != serverID || string(a.Payload) != payload || a.BlobSHA256 != sum || a.BlobSize != 3 || a.Deleted {
		t.Fatalf("entry %+v, payload %s", a, a.Payload)
	}
	if b.Key != "b" || !b.Deleted || b.Payload != nil || b.BlobSHA256 != "" || b.Origin != callerID {
		t.Fatalf("tombstone %+v", b)
	}
	// The cursor is passed on as it was given, and the last page has none.
	f.snapshot = func(stream, cursor string, limit int) (streamsync.SnapshotPage, error) {
		asked = []any{stream, cursor, limit}
		return streamsync.SnapshotPage{Origin: serverID, Stream: stream, Sequence: 42}, nil
	}
	if page, err := sr.Snapshot(t.Context(), rm.peer, "test:s", "next", 5); err != nil || page.NextCursor != "" || len(page.Entries) != 0 {
		t.Fatalf("last page %+v, %v", page, err)
	}
	if fmt.Sprint(asked) != fmt.Sprint([]any{"test:s", "next", 5}) {
		t.Fatalf("asked %v", asked)
	}

	// 410 is ErrGone; a kind without snapshots and other refusals are
	// errors naming the status.
	for _, c := range []struct {
		err    error
		status string
	}{
		{streamsync.ErrNoSnapshot, "404"},
		{streamsync.ErrNoStream, "404"},
		{streamsync.ErrRefused, "403"},
		{fmt.Errorf("cursor: %w", kernel.ErrInvalid), "400"},
	} {
		f.snapshot = func(string, string, int) (streamsync.SnapshotPage, error) { return streamsync.SnapshotPage{}, c.err }
		if _, err := sr.Snapshot(t.Context(), rm.peer, "test:s", "", 2); err == nil || !strings.Contains(err.Error(), c.status) {
			t.Errorf("%v: %v, want %s", c.err, err, c.status)
		}
	}
	f.snapshot = func(string, string, int) (streamsync.SnapshotPage, error) {
		return streamsync.SnapshotPage{}, streamsync.ErrGone
	}
	if _, err := sr.Snapshot(t.Context(), rm.peer, "test:s", "x", 2); !errors.Is(err, streamsync.ErrGone) {
		t.Fatalf("410: %v", err)
	}

	// A page of another origin is not this peer's snapshot.
	f.snapshot = func(string, string, int) (streamsync.SnapshotPage, error) {
		return streamsync.SnapshotPage{Origin: "jfrt@other", Stream: "test:s"}, nil
	}
	if _, err := sr.Snapshot(t.Context(), rm.peer, "test:s", "", 2); err == nil {
		t.Error("a page naming another origin was taken")
	}
	bad := peeradapter.StreamServer{Streams: &fakeStreams{snapshot: func(stream, _ string, _ int) (streamsync.SnapshotPage, error) {
		return streamsync.SnapshotPage{Origin: serverID, Stream: stream}, nil
	}}}
	signed := corepeer.WithCaller(t.Context(), model.Peer{InstanceID: callerID})
	if _, err := bad.GetSnapshot(signed, peergen.GetSnapshotRequestObject{Stream: "test:s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (peeradapter.StreamServer{Streams: f}).GetSnapshot(t.Context(), peergen.GetSnapshotRequestObject{}); statusOf(t, err) != 401 {
		t.Error("unsigned snapshot")
	}
	// Nor is a page of another stream, or one with an unreadable clock
	// time, which this instance's server never writes.
	for name, body := range map[string]string{
		"stream": `{"origin":"` + serverID + `","stream":"test:other","sequence":1,"entries":[]}`,
		"hlc":    `{"origin":"` + serverID + `","stream":"test:s","sequence":1,"entries":[{"key":"k","hlc":"now","origin":"x","deleted":false}]}`,
	} {
		rm.handler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
		if _, err := sr.Snapshot(t.Context(), rm.peer, "test:s", "", 2); err == nil {
			t.Errorf("a page with another %s was taken", name)
		}
	}
	nokey := rm.peer
	nokey.PublicKey = nil
	if _, err := sr.Snapshot(t.Context(), nokey, "test:s", "", 2); !errors.Is(err, peeradapter.ErrNoPeerKey) {
		t.Fatalf("no key: %v", err)
	}
}

func TestUnitStreamPushRefusedWhileResyncing(t *testing.T) {
	f := &fakeStreams{receive: func(streamsync.Batch) (int64, error) { return 0, streamsync.ErrBusy }}
	rm, sr := streamRemote(t, f)
	_, err := sr.Push(t.Context(), rm.peer, streamsync.Batch{
		Origin: callerID, Stream: "test:s", FromSeq: 1, Entries: []outbox.OutboxEntry{entry(1, `{}`)},
	})
	if !errors.Is(err, streamsync.ErrBusy) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("push while resyncing: %v, want busy", err)
	}
}
