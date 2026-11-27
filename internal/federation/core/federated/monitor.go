package federated

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// StatusLagThreshold is the monitoring threshold chosen by artifactweave.
// A fresh event in flight does not make an otherwise healthy member delayed.
const StatusLagThreshold = time.Minute

// MemberStatus is the shared read model for legacy and RTFS-era monitoring.
// It describes the outbound cursor; receiver mode and connection health are
// independent, so a healthy receiver can still receive promptly.
type MemberStatus struct {
	Member           model.FederationMember
	LocalPlatformURL string
	Status           string
	Mode             string
	Supported        bool
	Disabled         bool
	QueuedEvents     int64
	LagInMS          int64
	RegisteredAt     int64
	CreateEvents     int64
	UpdateEvents     int64
	DeleteEvents     int64
	PropsEvents      int64
	ErrorEvents      int64
}

// RepositoryStatus is one transaction's view of a federated repository.
// Detailed reads additionally count actual files and pending binary changes.
type RepositoryStatus struct {
	Key             string
	Status          model.SyncStatus
	Priority        model.FederationPriority
	Members         []MemberStatus
	FailedTasks     int
	InProgressTasks int
	FullyReplicated int64
	MetadataOnly    int64
}

// ConfigurationRemote reads a member's shared configuration over the signed
// peer transport. An unreachable member is not proof of configuration drift.
type ConfigurationRemote interface {
	Configuration(context.Context, model.Peer, string) (map[string]any, error)
}

// Monitoring supplies the read-only FED-05 APIs using current durable records.
// Only Inconsistent calls peers, and it does so outside the metadata transaction.
type Monitoring struct {
	Operations
	Items  artifactsapi.FederatedItemReader
	Remote func() ConfigurationRemote
}

type monitorStore interface {
	Instance() instance.InstanceStore
	Resyncs() port.StreamResyncStore
}

// Read reports one repository and optionally the detailed binary/artifact counts.
func (m Monitoring) Read(ctx context.Context, key string, detailed bool) (RepositoryStatus, error) {
	var result RepositoryStatus
	err := m.tx(ctx, func(ctx context.Context) error {
		stored, cached := repo.ResolveKey(key)
		cfg, err := m.Repositories.Get(ctx, stored)
		if errors.Is(err, kernel.ErrNotFound) {
			return ErrNoRepository
		}
		if err != nil {
			return fmt.Errorf("status repository %s: %w", key, err)
		}
		if cached && cfg.Class != repo.Remote || !cached && cfg.Class != repo.Federated && cfg.Class != repo.Local {
			return ErrNoRepository
		}
		if cfg.Class != repo.Federated {
			result = RepositoryStatus{Key: key, Status: model.SyncSyncing, Priority: model.PriorityStandard, Members: []MemberStatus{}}
			if detailed {
				return m.artifacts(ctx, key, &result)
			}
			return nil
		}
		result, err = m.read(ctx, cfg, detailed)
		return err
	})
	return result, err //nolint:wrapcheck // each read names its cause
}

// List reports every currently federated repository in repository-key order.
func (m Monitoring) List(ctx context.Context) ([]RepositoryStatus, error) {
	return m.list(ctx, false)
}

// ListWithBase keeps the RTFS state-list/summary base-URL prerequisite inside
// the same authorized snapshot, including when the catalog has no federations.
func (m Monitoring) ListWithBase(ctx context.Context) ([]RepositoryStatus, error) {
	return m.list(ctx, true)
}

func (m Monitoring) list(ctx context.Context, requireBase bool) ([]RepositoryStatus, error) {
	result := []RepositoryStatus{}
	err := m.tx(ctx, func(ctx context.Context) error {
		if requireBase {
			base, err := m.Membership.SelfBase(ctx)
			if err != nil {
				return err
			}
			if base == "" {
				return ErrNoConnectionBaseURL
			}
		}
		cfgs, err := m.Repositories.List(ctx, artifactsapi.RepoFilter{})
		if err != nil {
			return fmt.Errorf("federation status repositories: %w", err)
		}
		slices.SortFunc(cfgs, func(a, b repo.Config) int { return cmp.Compare(a.Key, b.Key) })
		for _, cfg := range cfgs {
			if cfg.Class != repo.Federated {
				continue
			}
			row, err := m.read(ctx, cfg, false)
			if err != nil {
				return err
			}
			result = append(result, row)
		}
		return nil
	})
	return result, err //nolint:wrapcheck // each read names its cause
}

func (m Monitoring) read(ctx context.Context, cfg repo.Config, detailed bool) (RepositoryStatus, error) {
	result := RepositoryStatus{Key: cfg.Key, Priority: model.PriorityStandard, Members: []MemberStatus{}}
	members, disabled, running, err := m.readMembers(ctx, cfg, detailed)
	if err != nil {
		return result, err
	}
	result.Members = members
	high, err := HighPriority(ctx, m.Membership.Store.Settings())
	if err != nil {
		return result, err
	}
	if slices.Contains(high, cfg.Key) {
		result.Priority = model.PriorityHigh
	}
	lags := make([]time.Duration, 0, len(members))
	enabledPeers := map[string]bool{}
	for _, row := range members {
		if row.Disabled {
			continue
		}
		enabledPeers[row.Member.PeerInstanceID] = true
		lag := time.Duration(row.LagInMS) * time.Millisecond
		if row.Status == "ERROR" || row.Status == "N/A" || row.Status == "UNSUPPORTED" || row.Status == "PENDING_FS" {
			lag = max(lag, StatusLagThreshold+time.Millisecond)
		}
		lags = append(lags, lag)
	}
	// FullSyncs also covers an incoming run whose flag was settled at the
	// snapshot boundary, while the run still applies its final stream entries.
	after := ""
	for {
		runs, next, err := m.Membership.Store.Federation().FullSyncs(ctx, cfg.Key, kernel.Page{Limit: 100, After: kernel.Cursor(after)})
		if err != nil {
			return result, fmt.Errorf("federation status full syncs: %w", err)
		}
		for _, run := range runs {
			running = running || run.Status == retentionapi.RunInProgress && enabledPeers[run.PeerInstanceID]
		}
		if next == "" {
			break
		}
		after = next
	}
	failures, err := m.Membership.Store.Outbox().FailedFetches(ctx, model.RepositoryStream(cfg.Key))
	if err != nil {
		return result, fmt.Errorf("federation status failed binaries: %w", err)
	}
	result.FailedTasks = len(failures)
	if len(failures) > 0 {
		lags = append(lags, StatusLagThreshold+time.Millisecond)
	}
	result.Status = model.DeriveSyncStatus(model.FederationSettings{Disabled: disabled}, running, lags, StatusLagThreshold)
	if detailed {
		if err := m.artifacts(ctx, cfg.Key, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ConnectionStates is the bounded member-health projection used by the
// existing connectionStates endpoint. History and binary lists are not needed.
// The source resource answers unknown and non-federated keys with an empty list,
// after checking administrator authority.
func (m Monitoring) ConnectionStates(ctx context.Context, key string) ([]MemberStatus, error) {
	result := []MemberStatus{}
	err := m.tx(ctx, func(ctx context.Context) error {
		cfg, _, err := m.connectionConfig(ctx, key)
		if err != nil {
			return err
		}
		if cfg.Class != repo.Federated {
			return nil
		}
		result, _, _, err = m.readMembers(ctx, cfg, false)
		return err
	})
	return result, err //nolint:wrapcheck // each member read names its cause
}

func (m Monitoring) readMembers(ctx context.Context, cfg repo.Config, detailed bool) (rows []MemberStatus, disabled, running bool, err error) {
	st, ok := m.Membership.Store.(monitorStore)
	if !ok {
		return nil, false, false, fmt.Errorf("federation monitoring store: %w", kernel.ErrStore)
	}
	self, err := m.Membership.SelfBase(ctx)
	if err != nil {
		return nil, false, false, err
	}
	disabled = selfMemberDisabled(cfg, self)
	members, err := m.Membership.Store.Federation().Members(ctx, cfg.Key)
	if err != nil {
		return nil, false, false, fmt.Errorf("federation status members: %w", err)
	}
	rows = make([]MemberStatus, 0, len(members))
	if len(members) == 0 {
		return rows, disabled, false, nil
	}
	id, err := st.Instance().Get(ctx)
	if err != nil {
		return nil, false, false, fmt.Errorf("federation status identity: %w", err)
	}
	for _, member := range members {
		row, active, err := m.member(ctx, st, id.InstanceID, self, member, disabled, detailed)
		if err != nil {
			return nil, false, false, err
		}
		rows = append(rows, row)
		running = running || active && !row.Disabled
	}
	return rows, disabled, running, nil
}

func (m Monitoring) member(ctx context.Context, st monitorStore, self, selfBase string, member model.FederationMember, disabled, detailed bool) (MemberStatus, bool, error) {
	row := MemberStatus{Member: member, LocalPlatformURL: selfBase, Disabled: disabled || !member.Enabled}
	if member.Mode == repo.ModeReceiver {
		row.Mode = "Receiving_Only"
	}
	peer, err := m.Membership.Store.Peers().Get(ctx, member.PeerInstanceID)
	if err != nil {
		return row, false, fmt.Errorf("federation status peer: %w", err)
	}
	row.Supported = peer.Compatible()
	stream := model.RepositoryStream(member.RepoKey)
	cursor, err := m.Membership.Store.Outbox().Cursor(ctx, peer.InstanceID, stream)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return row, false, fmt.Errorf("federation status cursor: %w", err)
	}
	head, err := m.Membership.Store.Outbox().LastSeq(ctx, self, stream)
	if err != nil {
		return row, false, fmt.Errorf("federation status stream head: %w", err)
	}
	row.QueuedEvents = cursor.Behind(head)
	gap := false
	if row.QueuedEvents > 0 {
		after := cursor.AckedSeq
		limit := 1
		if detailed {
			limit = 100
		}
		for {
			entries, err := m.Membership.Store.Outbox().Read(ctx, self, stream, after, limit)
			if err != nil {
				return row, false, fmt.Errorf("federation status events: %w", err)
			}
			if after == cursor.AckedSeq {
				gap = len(entries) == 0 || entries[0].Seq > after+1
				if len(entries) > 0 {
					row.RegisteredAt = entries[0].Created.UnixMilli()
					row.LagInMS = max(0, m.Membership.now().Sub(entries[0].Created).Milliseconds())
				}
			}
			for _, entry := range entries {
				countEvent(&row, entry)
			}
			if !detailed || len(entries) < limit {
				break
			}
			after = entries[len(entries)-1].Seq
		}
	}
	if cursor.LastError != "" {
		row.ErrorEvents = row.QueuedEvents
	}
	active, pending, failed, err := m.memberSync(ctx, st, self, peer.InstanceID, member.RepoKey)
	if err != nil {
		return row, false, err
	}
	switch {
	case row.Disabled:
		row.Status = "DISABLED"
	case !row.Supported:
		row.Status = "UNSUPPORTED"
	case peer.Status == model.PeerUnauthorized || cursor.LastError != "" || failed:
		row.Status = "ERROR"
	case peer.Status == model.PeerOffline:
		row.Status = "N/A"
	case active:
		row.Status = "FULL_SYNC_RUNNING"
	case pending || gap:
		row.Status = "PENDING_FS"
	case row.LagInMS > StatusLagThreshold.Milliseconds():
		row.Status = "DELAYED"
	default:
		row.Status = "HEALTHY"
	}
	return row, active, nil
}

func countEvent(row *MemberStatus, entry outbox.OutboxEntry) {
	switch entry.EventType {
	case EventDelete:
		row.DeleteEvents++
	case EventPut:
		var p payload
		if json.Unmarshal(entry.Payload, &p) != nil {
			p.Operation = ""
		}
		switch p.Operation {
		case artifactsapi.ItemCreate:
			row.CreateEvents++
		case artifactsapi.ItemProperties:
			row.PropsEvents++
		default:
			row.UpdateEvents++
		}
	}
}

func (m Monitoring) memberSync(ctx context.Context, st monitorStore, self, peer, key string) (active, pending, failed bool, err error) {
	for _, pair := range [][2]string{{self, peer}, {peer, self}} {
		_, err := st.Resyncs().Get(ctx, pair[0], pair[1], model.RepositoryStream(key))
		if err == nil {
			active = true
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return false, false, false, fmt.Errorf("federation status full sync: %w", err)
		}
	}
	for _, forced := range []bool{false, true} {
		item, err := m.Membership.Store.WorkQueue().Get(ctx, FullSyncRequestKind, requestKey(peer, key, forced))
		if err == nil {
			pending = pending || item.State == work.WorkPending || item.State == work.WorkLeased
			failed = failed || item.State == work.WorkParked
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return false, false, false, fmt.Errorf("federation status full-sync request: %w", err)
		}
	}
	return active, pending, failed, nil
}

func (m Monitoring) artifacts(ctx context.Context, key string, result *RepositoryStatus) error {
	if m.Items == nil {
		return fmt.Errorf("federation status item reader: %w", kernel.ErrStore)
	}
	files := map[string]bool{}
	after := ""
	for {
		items, err := m.Items.List(ctx, key, after, 100, false)
		if err != nil {
			return fmt.Errorf("federation status artifacts: %w", err)
		}
		for _, item := range items {
			if !item.Folder {
				files[item.Path] = true
			}
		}
		if len(items) < 100 {
			break
		}
		after = items[len(items)-1].Path
	}
	pendingDigests := map[string]bool{}
	after = ""
	for {
		stamps, err := m.Membership.Store.Stamps().List(ctx, key, after, 100)
		if err != nil {
			return fmt.Errorf("federation status pending artifacts: %w", err)
		}
		for _, stamp := range stamps {
			if stamp.Deleted || stamp.Folder || len(stamp.Pending) == 0 {
				continue
			}
			delete(files, stamp.Path)
			result.MetadataOnly++
			pendingDigests[stamp.SHA256] = true
		}
		if len(stamps) < 100 {
			break
		}
		after = stamps[len(stamps)-1].Path
	}
	result.FullyReplicated = int64(len(files))
	failures, err := m.Membership.Store.Outbox().FailedFetches(ctx, model.RepositoryStream(key))
	if err != nil {
		return fmt.Errorf("federation status binary tasks: %w", err)
	}
	result.FailedTasks = len(failures)
	for _, failure := range failures {
		delete(pendingDigests, failure.SHA256)
	}
	result.InProgressTasks = len(pendingDigests)
	return nil
}
