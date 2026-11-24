package federation_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type noRemote struct{}

func (noRemote) Push(context.Context, model.Peer, streamsync.Batch) (int64, error) { return 0, nil }
func (noRemote) Pull(context.Context, model.Peer, string, int64, int) (streamsync.Page, error) {
	return streamsync.Page{}, nil
}

func (noRemote) Snapshot(context.Context, model.Peer, string, string, int) (streamsync.SnapshotPage, error) {
	return streamsync.SnapshotPage{}, nil
}

func (noRemote) FetchBlob(context.Context, model.Peer, string, func(io.Reader) error) error {
	return nil
}

// TestUnitOpenStreams builds the delivery engine with no stream kind,
// registers its work once the engine is open, and refuses a store or
// options it cannot deliver with.
func TestUnitOpenStreams(t *testing.T) {
	store := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Streams() != nil || (*federation.Module)(nil).Streams() != nil {
		t.Fatal("streams before OpenStreams")
	}
	queue, err := work.New(store, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenStreams(federation.StreamOptions{Blobs: blobs, Queue: queue}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("no remote: %v", err)
	}
	e, err := m.OpenStreams(federation.StreamOptions{Blobs: blobs, Remote: noRemote{}, Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	if m.Streams() != e || len(e.Kinds.Kinds()) != 0 {
		t.Fatal("the engine is not the module's, or has a kind")
	}
	if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.OpenPull(t.Context(), struct{ artifactsapi.PullSource }{}); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterWork(t.Context(), queue); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WorkQueue().Get(t.Context(), streamsync.SweepKind, "streams"); err != nil {
		t.Fatalf("the sweep is not queued: %v", err)
	}
	m.Close()

	narrow, err := federation.New(federation.Deps{Store: narrowStore{store}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := narrow.OpenStreams(federation.StreamOptions{}); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("a store without an outbox: %v", err)
	}
}

// narrowStore is federation's store without the outbox and peer records.
type narrowStore struct{ federation.Store }
