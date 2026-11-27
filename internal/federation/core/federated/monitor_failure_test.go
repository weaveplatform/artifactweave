package federated_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// A status read must fail when any required durable record is unavailable;
// inventing an empty cursor, membership or task list would report false health.
func TestUnitMonitoringDoesNotConvertStoreOutagesIntoHealthyStatus(t *testing.T) {
	for _, failure := range []string{"identity", "settings", "members", "peer", "cursor", "head", "events", "full-sync", "request", "runs", "fetches", "stamps", "items", "repository"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f, m, self := monitorFixture(t)
			appendMonitorEvent(t, f, self, federated.EventPut, "create", "queued", t0)
			broken := monitorFailureStore{Store: f.s, original: f.s, fail: failure}
			m.Membership.Store = broken
			if failure == "items" {
				f.items.listErr = kernel.ErrStore
			}
			if failure == "repository" {
				f.repos.getErr = kernel.ErrStore
			}
			if _, err := m.Read(t.Context(), "fed", true); !errors.Is(err, kernel.ErrStore) {
				t.Fatalf("%s outage lost: %v", failure, err)
			}
		})
	}
}

type monitorFailureStore struct {
	federated.Store
	original store
	fail     string
}

func (s monitorFailureStore) Instance() instance.InstanceStore {
	return failedIdentity{s.original.Instance(), s.fail}
}

func (s monitorFailureStore) Settings() settings.SettingsStore {
	return failedStatusSettings{s.original.Settings(), s.fail}
}

func (s monitorFailureStore) Federation() port.FederationStore {
	return failedStatusFederation{s.original.Federation(), s.fail}
}

func (s monitorFailureStore) Peers() port.PeerStore {
	return failedStatusPeer{s.original.Peers(), s.fail}
}

func (s monitorFailureStore) Outbox() outbox.OutboxStore {
	return failedStatusOutbox{s.original.Outbox(), s.fail}
}

func (s monitorFailureStore) Resyncs() port.StreamResyncStore {
	return failedStatusResync{s.original.Resyncs(), s.fail}
}

func (s monitorFailureStore) WorkQueue() work.WorkQueue {
	return failedStatusWork{s.original.WorkQueue(), s.fail}
}

func (s monitorFailureStore) Stamps() port.ItemStampStore {
	return failedStatusStamps{s.original.Stamps(), s.fail}
}

type failedIdentity struct {
	instance.InstanceStore
	fail string
}

func (s failedIdentity) Get(ctx context.Context) (instance.InstanceIdentity, error) {
	if s.fail == "identity" {
		return instance.InstanceIdentity{}, kernel.ErrStore
	}
	return s.InstanceStore.Get(ctx)
}

type failedStatusSettings struct {
	settings.SettingsStore
	fail string
}

func (s failedStatusSettings) Get(ctx context.Context, key string, into any) error {
	if s.fail == "settings" {
		return kernel.ErrStore
	}
	return s.SettingsStore.Get(ctx, key, into)
}

type failedStatusFederation struct {
	port.FederationStore
	fail string
}

func (s failedStatusFederation) Members(ctx context.Context, key string) ([]model.FederationMember, error) {
	if s.fail == "members" {
		return nil, kernel.ErrStore
	}
	return s.FederationStore.Members(ctx, key)
}

func (s failedStatusFederation) FullSyncs(ctx context.Context, key string, page kernel.Page) ([]model.FullSyncRun, string, error) {
	if s.fail == "runs" {
		return nil, "", kernel.ErrStore
	}
	return s.FederationStore.FullSyncs(ctx, key, page)
}

type failedStatusPeer struct {
	port.PeerStore
	fail string
}

func (s failedStatusPeer) Get(ctx context.Context, key string) (model.Peer, error) {
	if s.fail == "peer" {
		return model.Peer{}, kernel.ErrStore
	}
	return s.PeerStore.Get(ctx, key)
}

type failedStatusOutbox struct {
	outbox.OutboxStore
	fail string
}

func (s failedStatusOutbox) Cursor(ctx context.Context, peer, stream string) (outbox.PeerCursor, error) {
	if s.fail == "cursor" {
		return outbox.PeerCursor{}, kernel.ErrStore
	}
	return s.OutboxStore.Cursor(ctx, peer, stream)
}

func (s failedStatusOutbox) LastSeq(ctx context.Context, origin, stream string) (int64, error) {
	if s.fail == "head" {
		return 0, kernel.ErrStore
	}
	return s.OutboxStore.LastSeq(ctx, origin, stream)
}

func (s failedStatusOutbox) Read(ctx context.Context, origin, stream string, after int64, limit int) ([]outbox.OutboxEntry, error) {
	if s.fail == "events" {
		return nil, kernel.ErrStore
	}
	return s.OutboxStore.Read(ctx, origin, stream, after, limit)
}

func (s failedStatusOutbox) FailedFetches(ctx context.Context, stream string) ([]outbox.FailedBlobFetch, error) {
	if s.fail == "fetches" {
		return nil, kernel.ErrStore
	}
	return s.OutboxStore.FailedFetches(ctx, stream)
}

type failedStatusResync struct {
	port.StreamResyncStore
	fail string
}

func (s failedStatusResync) Get(ctx context.Context, origin, receiver, stream string) (model.StreamResync, error) {
	if s.fail == "full-sync" {
		return model.StreamResync{}, kernel.ErrStore
	}
	return s.StreamResyncStore.Get(ctx, origin, receiver, stream)
}

type failedStatusWork struct {
	work.WorkQueue
	fail string
}

func (s failedStatusWork) Get(ctx context.Context, kind, key string) (work.WorkItem, error) {
	if s.fail == "request" {
		return work.WorkItem{}, kernel.ErrStore
	}
	return s.WorkQueue.Get(ctx, kind, key)
}

type failedStatusStamps struct {
	port.ItemStampStore
	fail string
}

func (s failedStatusStamps) List(ctx context.Context, key, after string, limit int) ([]model.ItemStamp, error) {
	if s.fail == "stamps" {
		return nil, kernel.ErrStore
	}
	return s.ItemStampStore.List(ctx, key, after, limit)
}
