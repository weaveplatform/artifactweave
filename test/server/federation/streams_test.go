package federation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const testStream = "test:s"

// streamInstance is one running instance with the test stream kind
// registered, and a hook in front of its router that a scenario uses to
// break an answer.
type streamInstance struct {
	t       *testing.T
	runtime *app.Runtime
	url     string
	id      string
	kind    *streamtest.Kind
	engine  *streamsync.Engine
	hook    atomic.Pointer[func(http.ResponseWriter, *http.Request, http.Handler) bool]
}

// newStreamWorld opens two runtimes over dsnA and dsnB behind their real
// routers, each allowed to call the other's loopback listener, and records
// each as the other's peer for federated-repo/*, as pairing would, with the
// deliveries given. Both subscribe to the test stream.
//
// These component scenarios retain direct fault hooks. The process-based
// TestAccPeerStreamSQLite/Postgres scenarios separately qualify pairing,
// restart, pull and real pruning through snapshot recovery in test/peers.
func newStreamWorld(t *testing.T, driver, dsnA, dsnB string, aRecordsB, bRecordsA model.Delivery) (a, b *streamInstance) {
	t.Helper()
	listeners := make([]net.Listener, 2)
	origins := make([]string, 2)
	for i := range listeners {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i], origins[i] = l, "http://"+l.Addr().String()
	}
	open := func(l net.Listener, dsn string) *streamInstance {
		cfg := pushConfig(t, driver, dsn)
		cfg.Remote.AllowedPrivateOrigins = origins
		runtime, err := app.OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), harness.AppOptions())
		if err != nil {
			t.Fatal("open runtime", err)
		}
		in := &streamInstance{t: t, runtime: runtime}
		router := harness.Router(harness.Config{
			Store: runtime.Store, Blobs: runtime.Blobs, Auth: runtime.Auth, Tokens: runtime.Tokens, Keys: runtime.Keys,
			Federation: runtime.Federation, PeerRemote: runtime.PeerRemote, Work: runtime.Work,
			PasswordHash: support.TestHash, Logger: slog.New(slog.DiscardHandler),
		})
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hook := in.hook.Load(); hook != nil && (*hook)(w, r, router) {
				return
			}
			router.ServeHTTP(w, r)
		}))
		_ = server.Listener.Close()
		server.Listener = l
		server.Start()
		t.Cleanup(func() {
			server.Close()
			if err := runtime.Close(context.Background()); err != nil {
				t.Error("close runtime", err)
			}
		})
		in.url = server.URL
		id, err := runtime.Store.Instance().Get(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		in.id = id.InstanceID
		in.engine = runtime.Federation.Streams()
		in.kind = &streamtest.Kind{Store: runtime.Store}
		if err := in.engine.Kinds.Register(in.kind); err != nil {
			t.Fatal(err)
		}
		return in
	}
	a, b = open(listeners[0], dsnA), open(listeners[1], dsnB)
	a.record(b, []model.UseCase{model.UseFederatedRepo}, aRecordsB)
	b.record(a, []model.UseCase{model.UseFederatedRepo}, bRecordsA)
	a.kind.Subscribe(testStream, b.id)
	b.kind.Subscribe(testStream, a.id)
	return a, b
}

// record makes in's record of peer, seeded through PeerStore as pairing
// leaves it.
func (in *streamInstance) record(peer *streamInstance, use []model.UseCase, d model.Delivery) {
	in.t.Helper()
	id, err := peer.runtime.Store.Instance().Get(in.t.Context())
	if err != nil {
		in.t.Fatal(err)
	}
	if err := in.runtime.Store.Peers().Put(in.t.Context(), model.Peer{
		InstanceID: peer.id, BaseURL: peer.url, PublicKey: id.PublicKey, ProtocolVersion: 1,
		UseCases: use, Delivery: d,
	}); err != nil {
		in.t.Fatal(err)
	}
}

func (in *streamInstance) peer(id string) model.Peer {
	in.t.Helper()
	p, err := in.runtime.Store.Peers().Get(in.t.Context(), id)
	if err != nil {
		in.t.Fatal(err)
	}
	return p
}

func (in *streamInstance) setHook(h func(http.ResponseWriter, *http.Request, http.Handler) bool) {
	if h == nil {
		in.hook.Store(nil)
		return
	}
	in.hook.Store(&h)
}

func (in *streamInstance) commit(c streamtest.Change) {
	in.t.Helper()
	if _, err := in.kind.Commit(in.t.Context(), in.engine, testStream, c, nil); err != nil {
		in.t.Fatal(err)
	}
}

func (in *streamInstance) put(key, value string) {
	in.commit(streamtest.Change{Type: streamtest.Put, Key: key, Value: value})
}

func (in *streamInstance) state(origin *streamInstance) streamtest.State {
	in.t.Helper()
	s, err := in.kind.State(in.t.Context(), testStream, origin.id)
	if err != nil {
		in.t.Fatal(err)
	}
	return s
}

func (in *streamInstance) cursor(peer *streamInstance) int64 {
	in.t.Helper()
	c, err := in.runtime.Store.Outbox().Cursor(in.t.Context(), peer.id, testStream)
	if errors.Is(err, kernel.ErrNotFound) {
		return 0
	}
	if err != nil {
		in.t.Fatal(err)
	}
	return c.AckedSeq
}

func (in *streamInstance) sweep() {
	in.t.Helper()
	if _, err := in.runtime.Work.RunDue(in.t.Context(), streamsync.SweepKind); err != nil {
		in.t.Fatal(err)
	}
}

// eventually waits for cond, failing with what describes the state.
func eventually(t *testing.T, what func() string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func seqsTo(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

// waitApplied waits until in has applied origin's entries 1 to n, and
// checks it applied each once and in order.
func (in *streamInstance) waitApplied(origin *streamInstance, n int) {
	in.t.Helper()
	eventually(in.t, func() string { return fmt.Sprint("applied ", in.state(origin).Applied) }, func() bool {
		return len(in.state(origin).Applied) >= n
	})
	if got := in.state(origin).Applied; !slices.Equal(got, seqsTo(n)) {
		in.t.Fatalf("applied %v, want 1 to %d once each in order", got, n)
	}
}

// dropAnswer is a hook that serves a request matching method and path
// prefix once, then closes the connection instead of answering.
func dropAnswer(method, prefix string, n *atomic.Int32) func(http.ResponseWriter, *http.Request, http.Handler) bool {
	return func(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
		if r.Method != method || !strings.HasPrefix(r.URL.Path, prefix) || n.Add(-1) < 0 {
			return false
		}
		next.ServeHTTP(httptest.NewRecorder(), r)
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return true
	}
}

const (
	eventsPath = "/artifactweave/peer/v1/streams/"
	blobsPath  = "/artifactweave/peer/v1/blobs/"
)

func testStreams(t *testing.T, driver string, dsn func() string) {
	t.Run("push", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		for i := range 5 {
			a.put("k", fmt.Sprint(i))
		}
		b.waitApplied(a, 5)
		eventually(t, func() string { return fmt.Sprint("cursor ", a.cursor(b)) }, func() bool { return a.cursor(b) == 5 })

		// An answer lost after the batch applied is retried, and the
		// retry changes nothing.
		var drops atomic.Int32
		drops.Store(1)
		b.setHook(dropAnswer(http.MethodPost, eventsPath, &drops))
		a.put("k", "6")
		b.waitApplied(a, 6)
		eventually(t, func() string { return fmt.Sprint("cursor ", a.cursor(b)) }, func() bool { return a.cursor(b) == 6 })
		b.setHook(nil)
		if drops.Load() >= 0 {
			t.Fatal("no answer was dropped")
		}

		// A duplicated batch, and one with a gap, are refused with the
		// expected sequence.
		entries, err := a.runtime.Store.Outbox().Read(t.Context(), a.id, testStream, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, from := range []int64{1, 8} {
			_, err := a.engine.Remote.Push(t.Context(), a.peer(b.id), streamsync.Batch{
				Origin: a.id, Stream: testStream, FromSeq: from, Entries: renumber(entries[:1], from),
			})
			var conflict *streamsync.ConflictError
			if !errors.As(err, &conflict) || conflict.Expected != 7 {
				t.Fatalf("batch from %d: %v, want 409 expecting 7", from, err)
			}
		}
		// A sender that lost its cursor resumes from the expected
		// sequence without loss or duplication.
		c, err := a.runtime.Store.Outbox().Cursor(t.Context(), b.id, testStream)
		if err != nil {
			t.Fatal(err)
		}
		c.AckedSeq = 2
		if err := a.runtime.Store.Outbox().PutCursor(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		a.put("k", "7")
		b.waitApplied(a, 7)

		// A rolled-back change is never delivered.
		boom := errors.New("the change failed")
		if _, err := a.kind.Commit(t.Context(), a.engine, testStream, streamtest.Change{Type: streamtest.Put, Key: "lost"}, boom); !errors.Is(err, boom) {
			t.Fatal(err)
		}
		a.put("k", "8")
		b.waitApplied(a, 8)
		if _, ok := b.state(a).Items["lost"]; ok {
			t.Fatal("a rolled-back change was delivered")
		}
	})

	t.Run("stopped receiver", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		a.put("k", "1")
		b.waitApplied(a, 1)
		b.setHook(func(w http.ResponseWriter, _ *http.Request, _ http.Handler) bool {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		})
		a.put("k", "2")
		a.put("k", "3")
		eventually(t, func() string { return "no failed push recorded" }, func() bool {
			c, err := a.runtime.Store.Outbox().Cursor(t.Context(), b.id, testStream)
			return err == nil && c.LastError != ""
		})
		b.setHook(nil)
		a.sweep()
		b.waitApplied(a, 3)
	})

	t.Run("pull", func(t *testing.T) {
		// B cannot be pushed to: A records it as pulling, and B records A
		// as one it pulls from.
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPulled, model.DeliveryPull)
		for i := range 4 {
			a.put(fmt.Sprint("k", i%2), fmt.Sprint(i))
		}
		b.sweep()
		b.waitApplied(a, 4)
		want := map[string]streamtest.Item{"k0": {Value: "2"}, "k1": {Value: "3"}}
		if got := b.state(a).Items; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("pulled state %v, want %v", got, want)
		}
		// The next pull acknowledges what B applied.
		b.sweep()
		eventually(t, func() string { return fmt.Sprint("cursor ", a.cursor(b)) }, func() bool { return a.cursor(b) == 4 })
		// B still pushes its own entries to A.
		b.put("mine", "x")
		a.waitApplied(b, 1)
	})

	t.Run("blobs", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		body, sum := blobContent("fetched and verified")
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "ok", Content: body})
		b.waitApplied(a, 1)
		if it := b.state(a).Items["ok"]; it.Pending || it.SHA256 != sum {
			t.Fatalf("item %+v", it)
		}
		if ok, err := b.runtime.Blobs.Exists(t.Context(), sum); err != nil || !ok {
			t.Fatalf("blob fetched: %v, %v", ok, err)
		}

		// A blob A will not serve, one corrupted on the way, and one whose
		// entry gives the wrong size are failed binaries; the stream moves
		// on.
		late, lateSum := blobContent("late")
		corrupt, corruptSum := blobContent("corrupt on the way")
		gone, goneSum := blobContent("deleted meanwhile")
		sized, sizedSum := blobContent("another size than its entry gives")
		var refuse atomic.Bool
		refuse.Store(true)
		a.setHook(func(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
			if !refuse.Load() || r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, blobsPath) {
				return false
			}
			switch strings.TrimPrefix(r.URL.Path, blobsPath) {
			case corruptSum:
				rec := httptest.NewRecorder()
				next.ServeHTTP(rec, r)
				for k, v := range rec.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(rec.Code)
				b := rec.Body.Bytes()
				b[0] ^= 0xff
				_, _ = w.Write(b)
				return true
			case lateSum, goneSum:
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			return false
		})
		// A holds the sized content through a stream B does not receive.
		if _, err := a.kind.Commit(t.Context(), a.engine, "test:other", streamtest.Change{
			Type: streamtest.Put, Key: "sized", Content: sized,
		}, nil); err != nil {
			t.Fatal(err)
		}
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "late", Content: late})
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "corrupt", Content: corrupt})
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "gone", Content: gone})
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "size", SHA256: sizedSum, Size: int64(len(sized)) + 1})
		a.put("after", "applied")
		b.waitApplied(a, 6)
		items := b.state(a).Items
		for _, k := range []string{"late", "corrupt", "gone", "size"} {
			if !items[k].Pending {
				t.Fatalf("%s is %+v, want pending", k, items[k])
			}
		}
		if items["after"].Value != "applied" {
			t.Fatalf("the entry after the failures is %+v", items["after"])
		}
		failed, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), testStream)
		if err != nil || len(failed) != 4 {
			t.Fatalf("failed binaries %+v, %v; want 4", failed, err)
		}
		for _, sha := range []string{lateSum, corruptSum, goneSum, sizedSum} {
			if ok, _ := b.runtime.Blobs.Exists(t.Context(), sha); ok {
				t.Fatalf("unfetched content %s is held", sha)
			}
		}
		// Deleted meanwhile: the retry never shows it.
		a.commit(streamtest.Change{Type: streamtest.Delete, Key: "gone"})
		b.waitApplied(a, 7)
		refuse.Store(false)
		// The queue retries with back-off; FED-06's replay calls the same.
		for _, sha := range []string{lateSum, corruptSum, goneSum} {
			eventually(t, func() string { return "retry of " + sha }, func() bool {
				return b.engine.RetryFetch(t.Context(), testStream, sha) == nil
			})
		}
		items = b.state(a).Items
		for _, k := range []string{"late", "corrupt"} {
			if it := items[k]; it.Pending {
				t.Fatalf("%s is %+v after its retry", k, it)
			}
		}
		// Content of another size than its entry gives never publishes.
		if err := b.engine.RetryFetch(t.Context(), testStream, sizedSum); err == nil || !items["size"].Pending {
			t.Fatalf("the wrongly sized item published: %v", err)
		}
		if _, ok := items["gone"]; ok {
			t.Fatal("a deleted item appeared")
		}
		if ok, _ := b.runtime.Blobs.Exists(t.Context(), goneSum); ok {
			t.Fatal("content nothing waits for was fetched")
		}
	})

	t.Run("refusals", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		body, sum := blobContent("refused")
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
		b.waitApplied(a, 1)
		entries, err := a.runtime.Store.Outbox().Read(t.Context(), a.id, testStream, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		batch := streamsync.Batch{Origin: a.id, Stream: testStream, FromSeq: 2, Entries: renumber(entries, 2)}
		remote := a.engine.Remote

		// Without the stream's use case: 403 on push and pull, and the
		// content is not served.
		b.record(a, []model.UseCase{model.UseDistribution}, model.DeliveryPush)
		a.record(b, []model.UseCase{model.UseDistribution}, model.DeliveryPush)
		calls := map[string]func() error{
			"push": func() error { _, err := remote.Push(t.Context(), a.peer(b.id), batch); return err },
			"pull": func() error { _, err := remote.Pull(t.Context(), a.peer(b.id), testStream, 0, 10); return err },
			"blob": func() error {
				return remote.FetchBlob(t.Context(), a.peer(b.id), sum, func(r io.Reader) error { _, err := io.Copy(io.Discard, r); return err })
			},
		}
		for name, want := range map[string]string{"push": "403", "pull": "403", "blob": "404"} {
			if err := calls[name](); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s without the use case: %v, want %s", name, err, want)
			}
		}
		// Unpaired: the signature names no peer, 401.
		if err := b.runtime.Store.Peers().Delete(t.Context(), a.id); err != nil {
			t.Fatal(err)
		}
		for name, call := range calls {
			if err := call(); err == nil || !strings.Contains(err.Error(), "401") {
				t.Errorf("%s unpaired: %v, want 401", name, err)
			}
		}
		if n := len(b.state(a).Applied); n != 1 {
			t.Fatalf("applied %d entries, want only the first", n)
		}
	})

	t.Run("prune", func(t *testing.T) {
		// B is to pull but has not: A keeps what B has not acknowledged
		// inside the window, prunes what has expired, and flags B.
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPulled, model.DeliveryPush)
		// Two entries written as if eight days ago, then a fresh one.
		old := time.Now().Add(-8 * 24 * time.Hour)
		if err := a.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
			for range 2 {
				if _, err := a.runtime.Store.Outbox().Append(ctx, outbox.OutboxEntry{
					Origin: a.id, Stream: testStream, EventType: "noop", Created: old,
					Time: outbox.HLC{Wall: old.UnixMilli()},
				}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		a.put("k", "fresh")
		if err := a.engine.Prune(t.Context()); err != nil {
			t.Fatal(err)
		}
		left, err := a.runtime.Store.Outbox().Read(t.Context(), a.id, testStream, 0, 10)
		if err != nil || len(left) != 1 || left[0].Seq != 3 {
			t.Fatalf("kept %+v, %v; want only the unacknowledged entry inside the window", left, err)
		}
		flags, err := a.runtime.Store.Resyncs().Resyncs(t.Context(), b.id)
		if err != nil || len(flags) != 1 || flags[0].Origin != a.id || flags[0].Position != 0 {
			t.Fatalf("A's flags %+v, %v; want B flagged at 0", flags, err)
		}
		// B now pulls, from below the floor: A answers 410, and B, applying
		// nothing across the gap, recovers from a snapshot (PEER-05) and
		// goes on pulling from its sequence.
		b.record(a, []model.UseCase{model.UseFederatedRepo}, model.DeliveryPull)
		if _, err := b.engine.Remote.Pull(t.Context(), b.peer(a.id), testStream, 0, 10); !errors.Is(err, streamsync.ErrGone) {
			t.Fatalf("pull below the floor: %v, want 410", err)
		}
		b.sweep()
		if run := b.waitResynced(a); run.SnapshotSeq != 3 {
			t.Fatalf("run %+v, want one from sequence 3", run)
		}
		if n := len(b.state(a).Applied); n != 0 {
			t.Fatalf("B applied %d entries across the gap", n)
		}
		a.put("k", "pulled")
		b.sweep()
		eventually(t, func() string { return fmt.Sprint(b.state(a).Items) }, func() bool {
			return b.state(a).Items["k"].Value == "pulled"
		})
	})
}

// renumber gives entries consecutive sequences from from.
func renumber(entries []outbox.OutboxEntry, from int64) []outbox.OutboxEntry {
	out := make([]outbox.OutboxEntry, len(entries))
	for i, en := range entries {
		en.Seq = from + int64(i)
		out[i] = en
	}
	return out
}

func blobContent(s string) ([]byte, string) {
	sum := sha256.Sum256([]byte(s))
	return []byte(s), hex.EncodeToString(sum[:])
}

func TestAccStreamsSQLite(t *testing.T) {
	t.Parallel()
	testStreams(t, "", func() string { return "" })
}

func TestAccStreamsPostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testStreams(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
