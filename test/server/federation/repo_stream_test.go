package federation_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

const repoStream = "repo:fed"

// call sends an administrator's request to in and checks its status.
func (in *streamInstance) call(method, path, body string, want int) string {
	in.t.Helper()
	req, err := http.NewRequestWithContext(in.t.Context(), method, in.url+path, strings.NewReader(body))
	if err != nil {
		in.t.Fatal(err)
	}
	req.SetBasicAuth("admin", "pw")
	if strings.HasPrefix(path, "/artifactory/api/") {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		in.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		in.t.Fatal(err)
	}
	if resp.StatusCode != want {
		in.t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, data, want)
	}
	return string(data)
}

// federate creates the federated repository fed on in with peer as its
// one member, recorded as configuration sync would leave it.
func (in *streamInstance) federate(peer *streamInstance) {
	in.t.Helper()
	in.call(http.MethodPut, "/artifactory/api/repositories/fed", `{"rclass":"federated","packageType":"generic"}`, http.StatusOK)
	in.members(model.FederationMember{PeerInstanceID: peer.id, BaseURL: peer.url, RemoteRepoKey: "fed", Enabled: true})
}

func (in *streamInstance) members(ms ...model.FederationMember) {
	in.t.Helper()
	if err := in.runtime.Store.Federation().SetMembers(in.t.Context(), "fed", ms); err != nil {
		in.t.Fatal(err)
	}
}

func (in *streamInstance) entries() []outbox.OutboxEntry {
	in.t.Helper()
	es, err := in.runtime.Store.Outbox().Read(in.t.Context(), in.id, repoStream, 0, 100)
	if err != nil {
		in.t.Fatal(err)
	}
	return es
}

func (in *streamInstance) stamp(path string) (model.ItemStamp, bool) {
	in.t.Helper()
	s, err := in.runtime.Store.Stamps().Stamp(in.t.Context(), "fed", path)
	if errors.Is(err, kernel.ErrNotFound) {
		return s, false
	}
	if err != nil {
		in.t.Fatal(err)
	}
	return s, true
}

func (in *streamInstance) hasItem(path string) bool {
	in.t.Helper()
	_, err := in.runtime.Store.Items().Get(in.t.Context(), "fed", false, path)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		in.t.Fatal(err)
	}
	return err == nil
}

// testRepositoryStream runs the repository stream between two real
// instances (decision 0022): a deploy enters the stream in its own
// transaction and a member applies it under the origin's stamp without
// appending it again; an applied entry's item, stamp and applied position
// commit together; a rolled-back change never enters the stream; and a
// peer that is not a member, or lacks federated-repo/*, is refused.
func testRepositoryStream(t *testing.T, driver string, dsn func() string) {
	a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
	a.federate(b)
	b.federate(a)

	a.call(http.MethodPut, "/artifactory/fed/dir/f.txt", "hello", http.StatusCreated)
	eventually(t, func() string { return "f.txt never reached B" }, func() bool { return b.hasItem("dir/f.txt") })
	if got := b.call(http.MethodGet, "/artifactory/fed/dir/f.txt", "", http.StatusOK); got != "hello" {
		t.Fatalf("B serves %q", got)
	}
	sa, _ := a.stamp("dir/f.txt")
	sb, _ := b.stamp("dir/f.txt")
	if sa.Stamp != sb.Stamp || sa.Origin != a.id || sb.SHA256 != sa.SHA256 {
		t.Fatalf("stamps differ: A %+v, B %+v", sa, sb)
	}
	if es := a.entries(); len(es) != 1 || es[0].EventType != federated.EventPut {
		t.Fatalf("A's stream %+v", es)
	}
	if es := b.entries(); len(es) != 0 {
		t.Fatalf("B appended what it applied: %+v", es)
	}

	// A change that rolls back takes its entry and stamp with it.
	boom := errors.New("the change failed")
	err := a.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
		if err := a.runtime.Store.ItemFeed().ItemChanged(ctx, artifactsapi.ItemChange{
			Repository: "fed", Path: "lost", Folder: true,
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, ok := a.stamp("lost"); ok || len(a.entries()) != 1 {
		t.Fatalf("a rolled-back change entered the stream: %+v", a.entries())
	}

	// An applied entry's item, stamp and applied position roll back
	// together, and apply again when the batch is resent.
	a.call(http.MethodPut, "/artifactory/api/storage/fed/dir?properties=k=v", "", http.StatusNoContent)
	es := a.entries()
	last := es[len(es)-1]
	// A recursive edit changes the folder, then the file below it.
	eventually(t, func() string { return "the property never reached B" }, func() bool {
		s, ok := b.stamp("dir/f.txt")
		return ok && s.Time == last.Time
	})
	// An entry appended without queueing its send, which this scenario
	// delivers itself inside a transaction of B's that it rolls back.
	if err := a.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
		_, err := a.runtime.Store.Outbox().Append(ctx, outbox.OutboxEntry{
			Origin: a.id, Stream: repoStream, EventType: federated.EventPut,
			Payload: []byte(`{"path":"made","folder":true}`), Time: outbox.HLC{Wall: last.Time.Wall + 1},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	made := a.entries()[len(a.entries())-1]
	err = b.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
		if _, err := b.engine.Receive(ctx, b.peer(a.id), streamsync.Batch{
			Origin: a.id, Stream: repoStream, FromSeq: made.Seq, Entries: []outbox.OutboxEntry{made},
		}); err != nil {
			return err
		}
		if !b.hasItemIn(ctx, "made") {
			return errors.New("not applied inside the transaction")
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, ok := b.stamp("made"); ok || b.hasItem("made") {
		t.Fatal("a rolled-back apply kept its item or stamp")
	}
	if m, err := b.runtime.Store.Outbox().Applied(t.Context(), a.id, repoStream); err != nil || m.LastSeq != made.Seq-1 {
		t.Fatalf("applied position %+v, %v; want %d", m, err, made.Seq-1)
	}
	a.sweep()
	eventually(t, func() string { return "made never applied" }, func() bool { return b.hasItem("made") })

	// A peer that is not a member is not sent the stream; one without
	// federated-repo/* is refused.
	entries := a.entries()
	batch := streamsync.Batch{
		Origin: a.id, Stream: repoStream, FromSeq: int64(len(entries)) + 1,
		Entries: renumber(entries[:1], int64(len(entries))+1),
	}
	b.members()
	if _, err := a.engine.Remote.Push(t.Context(), a.peer(b.id), batch); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("push to a non-member: %v, want 404", err)
	}
	b.members(model.FederationMember{PeerInstanceID: a.id, BaseURL: a.url, RemoteRepoKey: "fed", Enabled: true})
	b.record(a, []model.UseCase{model.UseDistribution}, model.DeliveryPush)
	if _, err := a.engine.Remote.Push(t.Context(), a.peer(b.id), batch); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("push without federated-repo/*: %v, want 403", err)
	}
}

func (in *streamInstance) hasItemIn(ctx context.Context, path string) bool {
	_, err := in.runtime.Store.Items().Get(ctx, "fed", false, path)
	return err == nil
}

func TestAccRepositoryStreamSQLite(t *testing.T) {
	t.Parallel()
	testRepositoryStream(t, "", func() string { return "" })
}

func TestAccRepositoryStreamPostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testRepositoryStream(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
