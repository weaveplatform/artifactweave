package federated

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Connection is one member connection in connectionStates. Mode's JSON
// name is inferred: receiving-only documentation distinguishes target mode
// Receiving_Only from connection status HEALTHY (research 40 section 6).
type Connection struct {
	LocalRepoKey      string
	LocalPlatformURL  string
	RemoteRepoKey     string
	RemotePlatformURL string
	Status            string
	Mode              string
	LagInMS           int64
	QueuedEvents      int64
	Disabled          bool
	Supported         bool
}

// ConnectionLagThreshold is artifactweave's one-minute monitoring threshold.
// It is a project choice; a fresh in-flight event remains HEALTHY.
const ConnectionLagThreshold = time.Minute

type connectionStore interface {
	Outbox() outbox.OutboxStore
	Instance() instance.InstanceStore
	Resyncs() port.StreamResyncStore
}

// Connections reports current membership, role and delivery state, after
// administrator authorization. It never calls a peer while holding the
// metadata transaction. Broader federation monitoring belongs to FED-05.
func (o Operations) Connections(ctx context.Context, key string) ([]Connection, error) {
	var out []Connection
	err := o.tx(ctx, func(ctx context.Context) error {
		c, err := o.federated(ctx, key)
		if err != nil {
			return err
		}
		self, err := o.Membership.SelfBase(ctx)
		if err != nil {
			return err
		}
		st, ok := o.Membership.Store.(connectionStore)
		if !ok {
			return fmt.Errorf("federation connection status store: %w", kernel.ErrStore)
		}
		id, err := st.Instance().Get(ctx)
		if err != nil {
			return fmt.Errorf("federation connection identity: %w", err)
		}
		members, err := o.Membership.Store.Federation().Members(ctx, key)
		if err != nil {
			return fmt.Errorf("federated repository members: %w", err)
		}
		head, err := st.Outbox().LastSeq(ctx, id.InstanceID, model.RepositoryStream(key))
		if err != nil {
			return fmt.Errorf("federation stream head: %w", err)
		}
		selfDisabled := selfMemberDisabled(c, self)
		out = make([]Connection, 0, len(members))
		for _, member := range members {
			p, err := o.Membership.Store.Peers().Get(ctx, member.PeerInstanceID)
			if err != nil {
				return fmt.Errorf("federation connection peer: %w", err)
			}
			cursor, err := st.Outbox().Cursor(ctx, p.InstanceID, model.RepositoryStream(key))
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("federation connection cursor: %w", err)
			}
			row := Connection{
				LocalRepoKey:      key,
				LocalPlatformURL:  self,
				RemoteRepoKey:     member.RemoteRepoKey,
				RemotePlatformURL: member.BaseURL,
				Supported:         p.Compatible(),
				Disabled:          !member.Enabled || selfDisabled,
				QueuedEvents:      max(0, head-cursor.AckedSeq),
			}
			if member.Mode == repo.ModeReceiver {
				row.Mode = "Receiving_Only"
			}

			needsSnapshot := false
			if row.QueuedEvents > 0 {
				entries, err := st.Outbox().Read(ctx, id.InstanceID, model.RepositoryStream(key), cursor.AckedSeq, 1)
				if err != nil {
					return fmt.Errorf("federation connection lag: %w", err)
				}
				needsSnapshot = len(entries) == 0 || entries[0].Seq > cursor.AckedSeq+1
				if len(entries) > 0 {
					row.LagInMS = max(0, o.Membership.now().Sub(entries[0].Created).Milliseconds())
				}
			}
			active, pending, failed, err := o.connectionSync(ctx, st, id.InstanceID, p.InstanceID, key)
			if err != nil {
				return err
			}
			switch {
			case row.Disabled:
				row.Status = "DISABLED"
			case !row.Supported:
				row.Status = "UNSUPPORTED"
			case p.Status == model.PeerUnauthorized || cursor.LastError != "" || failed:
				row.Status = "ERROR"
			case p.Status == model.PeerOffline:
				row.Status = "N/A"
			case active:
				row.Status = "FULL_SYNC_RUNNING"
			case pending || needsSnapshot:
				row.Status = "PENDING_FS"
			case row.LagInMS > ConnectionLagThreshold.Milliseconds():
				row.Status = "DELAYED"
			default:
				row.Status = "HEALTHY"
			}
			out = append(out, row)
		}
		return nil
	})
	return out, err
}

// connectionSync reads the bounded per-peer flags and the two request keys;
// completed work is removed, and parked requests are an error, not pending.
func (o Operations) connectionSync(ctx context.Context, st connectionStore, self, peer, key string) (active, pending, failed bool, err error) {
	for _, pair := range [][2]string{{self, peer}, {peer, self}} {
		_, err := st.Resyncs().Get(ctx, pair[0], pair[1], model.RepositoryStream(key))
		if err == nil {
			active = true
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return false, false, false, fmt.Errorf("connection full sync: %w", err)
		}
	}
	for _, forced := range []bool{false, true} {
		item, err := o.Membership.Store.WorkQueue().Get(ctx, FullSyncRequestKind, requestKey(peer, key, forced))
		if err == nil {
			pending = pending || item.State == work.WorkPending || item.State == work.WorkLeased
			failed = failed || item.State == work.WorkParked
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return false, false, false, fmt.Errorf("connection full-sync request: %w", err)
		}
	}
	return active, pending, failed, nil
}
