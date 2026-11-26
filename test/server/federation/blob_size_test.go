package federation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// sizeEvent is a real repository item event with independently controlled
// declared size. Its content remains the legitimate bytes held by the sender.
func sizeEvent(t *testing.T, origin, path, sum string, size, seq int64) outbox.OutboxEntry {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return outbox.OutboxEntry{
		Origin: origin, Stream: repoStream, Seq: seq, EventType: "item.put", Payload: payload,
		Time: outbox.HLC{Wall: 1900000000000 + seq}, BlobSHA256: sum, BlobSize: size,
	}
}

func testBlobSizeReceive(t *testing.T, driver string, dsn func() string) {
	for _, kind := range []string{"generic", "maven"} {
		t.Run(kind, func(t *testing.T) { testBlobSizeReceiveKind(t, driver, dsn, kind) })
	}
}

func testBlobSizeReceiveKind(t *testing.T, driver string, dsn func() string, kind string) {
	path := func(name string) string {
		if kind == "maven" {
			return "org/example/" + name + "/1.0/" + name + "-1.0.jar"
		}
		return name
	}
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
			for _, in := range []*streamInstance{a, b} {
				in.call(http.MethodPut, "/artifactory/api/repositories/fed", fmt.Sprintf(`{"rclass":"federated","packageType":%q}`, kind), http.StatusOK)
			}
			// Seed legitimate content without generating stream events; the
			// next signed batch is exactly the one constructed below.
			a.members()
			b.members()
			const body = "known cached bytes"
			_, sum := blobContent(body)
			a.call(http.MethodPut, "/artifactory/fed/"+path("known"), body, http.StatusCreated)
			if cached {
				b.call(http.MethodPut, "/artifactory/fed/"+path("known"), body, http.StatusCreated)
			}
			a.members(model.FederationMember{PeerInstanceID: b.id, BaseURL: b.url, RemoteRepoKey: "fed", Enabled: true})
			b.members(model.FederationMember{PeerInstanceID: a.id, BaseURL: a.url, RemoteRepoKey: "fed", Enabled: true})
			bad := sizeEvent(t, a.id, path("wrong-size"), sum, int64(len(body))+1, 1)
			if cached {
				// Pending metadata, its diagnostic and cursor must roll back
				// together; cached bytes and existing references survive.
				boom := errors.New("rollback size mismatch")
				err := b.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
					if _, err := b.engine.Receive(ctx, b.peer(a.id), streamsync.Batch{
						Origin: a.id, Stream: repoStream, FromSeq: 1, Entries: []outbox.OutboxEntry{bad},
					}); err != nil {
						return err
					}
					return boom
				})
				if !errors.Is(err, boom) {
					t.Fatalf("rolled-back size mismatch: %v", err)
				}
				if _, ok := b.stamp(path("wrong-size")); ok {
					t.Fatal("pending stamp survived rollback")
				}
				if failed, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), repoStream); err != nil || len(failed) != 0 {
					t.Fatalf("failed records after rollback: %+v, %v", failed, err)
				}
			}
			good := sizeEvent(t, a.id, path("after"), sum, int64(len(body)), 2)
			seq, err := a.engine.Remote.Push(t.Context(), a.peer(b.id), streamsync.Batch{
				Origin: a.id, Stream: repoStream, FromSeq: 1, Entries: []outbox.OutboxEntry{bad, good},
			})
			if err != nil || seq != 2 {
				t.Fatalf("signed size-mismatch batch: seq=%d err=%v, want acknowledged 2", seq, err)
			}
			if b.hasItem(path("wrong-size")) {
				t.Fatal("inconsistent item published")
			}
			if got := b.call(http.MethodGet, "/artifactory/fed/"+path("after"), "", http.StatusOK); got != body {
				t.Fatalf("later valid item serves %q", got)
			}
			if cached {
				if got := b.call(http.MethodGet, "/artifactory/fed/"+path("known"), "", http.StatusOK); got != body {
					t.Fatalf("existing reference serves %q", got)
				}
			}
			stamp, ok := b.stamp(path("wrong-size"))
			if !ok || len(stamp.Pending) == 0 || stamp.Size != bad.BlobSize {
				t.Fatalf("pending size-mismatch stamp: %+v", stamp)
			}
			if pos, err := b.runtime.Store.Outbox().Applied(t.Context(), a.id, repoStream); err != nil || pos.LastSeq != 2 {
				t.Fatalf("applied position: %+v, %v", pos, err)
			}
			// The good event has now cached the content in both cases. A
			// retry must still validate the bad pending entry's own size.
			if err := b.engine.RetryFetch(t.Context(), repoStream, sum); !errors.Is(err, streamsync.ErrFetchUnavailable) {
				t.Fatalf("retry size mismatch: %v", err)
			}
			failed, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), repoStream)
			if err != nil || len(failed) != 1 || !strings.Contains(failed[0].LastError, "size") {
				t.Fatalf("size-mismatch task: %+v, %v", failed, err)
			}
			info, err := b.runtime.Blobs.Stat(t.Context(), sum)
			if err != nil || info.Size != int64(len(body)) || b.hasItem(path("wrong-size")) {
				t.Fatalf("retry changed valid bytes or published mismatch: %+v, %v", info, err)
			}
		})
	}
}

func TestAccBlobSizeReceiveSQLite(t *testing.T) {
	testBlobSizeReceive(t, "", func() string { return "" })
}

func TestAccBlobSizeReceivePostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	testBlobSizeReceive(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}

func testBlobSizeSnapshot(t *testing.T, driver string, dsn func() string) {
	for _, badFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("bad-first=%t", badFirst), func(t *testing.T) {
			a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
			a.federate(b)
			b.federate(a)
			a.members()
			b.members()
			goodPath, badPath := "0-good", "1-bad"
			if badFirst {
				goodPath, badPath = badPath, goodPath
			}
			const body = "snapshot content remains valid"
			_, sum := blobContent(body)
			a.call(http.MethodPut, "/artifactory/fed/"+goodPath, body, http.StatusCreated)
			b.call(http.MethodPut, "/artifactory/fed/keep", body, http.StatusCreated)
			// A pending item is part of the sender's snapshot even though
			// its content never passed that item's declared-size check.
			bad := sizeEvent(t, a.id, badPath, sum, int64(len(body))+1, 1)
			ok, err := a.runtime.Store.Stamps().Replace(t.Context(), nil, model.ItemStamp{
				RepoKey: "fed", Path: badPath, Stamp: model.Stamp{Time: bad.Time, Origin: a.id},
				SHA256: sum, Size: bad.BlobSize, Pending: bad.Payload,
			})
			if err != nil || !ok {
				t.Fatalf("seed sender's pending metadata: %t, %v", ok, err)
			}
			a.members(model.FederationMember{PeerInstanceID: b.id, BaseURL: b.url, RemoteRepoKey: "fed", Enabled: true})
			b.members(model.FederationMember{PeerInstanceID: a.id, BaseURL: a.url, RemoteRepoKey: "fed", Enabled: true})
			a.call(http.MethodPost, "/artifactory/api/federation/fullSync/fed", "", http.StatusOK)
			var run model.FullSyncRun
			eventually(t, func() string { return fmt.Sprintf("size snapshot runs: %+v", b.runs()) }, func() bool {
				for _, r := range b.runs() {
					if r.PeerInstanceID == a.id && r.Stream == repoStream && r.Status.Terminal() {
						run = r
						return true
					}
				}
				return false
			})
			if run.Status != retentionapi.RunCompletedWithErrors {
				t.Fatalf("size snapshot run: %+v, want completed with errors", run)
			}
			if b.hasItem(badPath) {
				t.Fatal("snapshot published inconsistent metadata")
			}
			for _, path := range []string{goodPath, "keep"} {
				if got := b.call(http.MethodGet, "/artifactory/fed/"+path, "", http.StatusOK); got != body {
					t.Fatalf("snapshot reference %s serves %q", path, got)
				}
			}
			if pending, ok := b.stamp(badPath); !ok || len(pending.Pending) == 0 {
				t.Fatalf("snapshot missing pending mismatch: %+v", pending)
			}
			failed, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), repoStream)
			if err != nil || len(failed) != 1 || !strings.Contains(failed[0].LastError, "size") {
				t.Fatalf("snapshot failures: %+v, %v", failed, err)
			}
		})
	}
}

func TestAccBlobSizeSnapshotSQLite(t *testing.T) {
	testBlobSizeSnapshot(t, "", func() string { return "" })
}

func TestAccBlobSizeSnapshotPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	testBlobSizeSnapshot(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}

func testBlobSizeRetryMixed(t *testing.T, driver string, dsn func() string) {
	for _, badFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("bad-first=%t", badFirst), func(t *testing.T) {
			a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
			goodPath, badPath := "0-good", "1-bad"
			if badFirst {
				goodPath, badPath = badPath, goodPath
			}
			a.federate(b)
			b.federate(a)
			a.members()
			const body = "one digest shared by pending paths"
			_, sum := blobContent(body)
			a.call(http.MethodPut, "/artifactory/fed/known", body, http.StatusCreated)
			a.members(model.FederationMember{PeerInstanceID: b.id, BaseURL: b.url, RemoteRepoKey: "fed", Enabled: true})
			a.setHook(func(w http.ResponseWriter, r *http.Request, _ http.Handler) bool {
				if strings.Contains(r.URL.Path, "/blobs/") {
					w.WriteHeader(http.StatusServiceUnavailable)
					return true
				}
				return false
			})
			good := sizeEvent(t, a.id, goodPath, sum, int64(len(body)), 1)
			badSize := int64(len(body)) + 1
			if badFirst {
				// Also put the invalid requirement first in ascending size
				// order: retry must try the later, valid pending size.
				badSize = int64(len(body)) - 1
			}
			bad := sizeEvent(t, a.id, badPath, sum, badSize, 2)
			if _, err := a.engine.Remote.Push(t.Context(), a.peer(b.id), streamsync.Batch{
				Origin: a.id, Stream: repoStream, FromSeq: 1, Entries: []outbox.OutboxEntry{good, bad},
			}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{goodPath, badPath} {
				if pending, ok := b.stamp(path); !ok || len(pending.Pending) == 0 {
					t.Fatalf("missing pending item %s: %+v", path, pending)
				}
			}
			// A separate valid deploy makes the digest available, just as another
			// repository or stream can, without changing either pending event.
			b.members()
			b.call(http.MethodPut, "/artifactory/fed/known", body, http.StatusCreated)
			b.members(model.FederationMember{PeerInstanceID: a.id, BaseURL: a.url, RemoteRepoKey: "fed", Enabled: true})
			boom := errors.New("rollback partial publication")
			err := b.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
				if err := b.engine.RetryFetch(ctx, repoStream, sum); err == nil {
					return errors.New("partial publication reported no remaining mismatch")
				} else if !errors.Is(err, streamsync.ErrFetchUnavailable) {
					return fmt.Errorf("partial publication: %w", err)
				}
				return boom
			})
			if !errors.Is(err, boom) || b.hasItem(goodPath) || b.hasItem(badPath) {
				t.Fatalf("partial publication did not roll back: %v", err)
			}
			if err := b.engine.RetryFetch(t.Context(), repoStream, sum); !errors.Is(err, streamsync.ErrFetchUnavailable) {
				t.Fatalf("mixed-size retry: %v, want remaining size mismatch", err)
			}
			if !b.hasItem(goodPath) || b.hasItem(badPath) {
				t.Fatal("retry did not publish exactly the consistent pending item")
			}
			if got := b.call(http.MethodGet, "/artifactory/fed/known", "", http.StatusOK); got != body {
				t.Fatalf("existing content changed: %q", got)
			}
			failed, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), repoStream)
			if err != nil || len(failed) != 1 || !strings.Contains(failed[0].LastError, "size") {
				t.Fatalf("mixed-size task was lost: %+v, %v", failed, err)
			}
			for _, step := range []struct {
				path string
				seq  int64
			}{{"after", 3}, {badPath, 4}} {
				en := sizeEvent(t, a.id, step.path, sum, int64(len(body)), step.seq)
				if seq, err := a.engine.Remote.Push(t.Context(), a.peer(b.id), streamsync.Batch{
					Origin: a.id, Stream: repoStream, FromSeq: en.Seq, Entries: []outbox.OutboxEntry{en},
				}); err != nil || seq != en.Seq {
					t.Fatalf("later valid entry %s: seq=%d err=%v", step.path, seq, err)
				}
				if got := b.call(http.MethodGet, "/artifactory/fed/"+step.path, "", http.StatusOK); got != body {
					t.Fatalf("later entry %s serves %q", step.path, got)
				}
			}
			if err := b.engine.RetryFetch(t.Context(), repoStream, sum); err != nil {
				t.Fatalf("obsolete mismatch task: %v", err)
			}
			if failed, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), repoStream); err != nil || len(failed) != 0 {
				t.Fatalf("corrected size retained failed task: %+v, %v", failed, err)
			}
		})
	}
}

func TestAccBlobSizeRetryMixedPendingSQLite(t *testing.T) {
	testBlobSizeRetryMixed(t, "", func() string { return "" })
}

func TestAccBlobSizeRetryMixedPendingPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	testBlobSizeRetryMixed(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
