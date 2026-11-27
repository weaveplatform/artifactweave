package cleanup

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

const (
	// Window bounds collection and use of timestamp comparisons. Destructive
	// exclusion additionally requires the independent durable reservation.
	Window = 5 * time.Second
	// AttemptTimeout bounds one member request.
	AttemptTimeout = 1500 * time.Millisecond
	// Concurrency is the maximum simultaneous member requests.
	Concurrency = 4
)

// ErrUnavailable retains content when a current member cannot supply a fresh
// observation, including disabled members and absent remote versions.
var ErrUnavailable = api.ErrCleanupUnavailable

// Store shares repository, instance and relationship publication transactions.
type Store interface {
	tx.Runner
	InTx(context.Context) bool
	Peers() port.PeerStore
	Federation() port.FederationStore
	Instance() instance.InstanceStore
	Configs() configdoc.ConfigStore
}

// Request is a fresh challenge for one bounded version batch on a member.
type Request struct {
	SourceRepository, Challenge, PackageType string
	Versions                                 []api.CleanupVersion
}

// Answer is returned only over the existing request-bound signed transport.
type Answer struct {
	Instance, Incarnation, Generation, Grant, Challenge, PackageType string
	Versions                                                         []api.CleanupDownload
}

// Remote makes exactly one bounded signed request to a current member.
type Remote interface {
	Downloads(context.Context, model.Peer, string, Request) (Answer, error)
}

// Service opens no resource and starts no worker. One shared instance owns the
// local failure fence used by every candidate and destructive operation.
type Service struct {
	Store      Store
	Metadata   artifactsapi.CleanupMetadata
	Exclusions artifactsapi.CleanupExclusions
	// OpenExclusions resolves the runtime dependency only when coordination begins.
	// Exclusions, when supplied directly, takes precedence. Neither is cached.
	OpenExclusions func() artifactsapi.CleanupExclusions
	Remote         Remote
	BaseURL        string
	Now            func() time.Time
	failures       failureFence
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) revisions() (port.PeerRevisions, error) {
	revisions, ok := s.Store.Peers().(port.PeerRevisions)
	if !ok {
		return nil, fmt.Errorf("cleanup trust revisions unavailable: %w", kernel.ErrStore)
	}
	return revisions, nil
}

// Scope returns only bounded current identities; it never opens sealed
// repository credentials or returns metadata outside the named repository.
func (s *Service) Scope(ctx context.Context, key string) (api.CleanupScope, error) {
	if s.Store == nil || s.Metadata == nil {
		return api.CleanupScope{}, fmt.Errorf("cleanup metadata unavailable: %w", kernel.ErrStore)
	}
	var out api.CleanupScope
	var unavailable bool
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		local, err := s.Metadata.Repository(ctx, key)
		if err != nil {
			return fmt.Errorf("read cleanup scope metadata: %w", err)
		}
		if local.Repository.Class != repo.Federated {
			return fmt.Errorf("cleanup repository is not federated: %w", kernel.ErrInvalid)
		}
		out = api.CleanupScope{
			Repository:  key,
			Incarnation: local.Incarnation,
			Generation:  local.Generation,
			Project:     local.Repository.Project,
			Class:       string(local.Repository.Class),
			PackageType: string(local.Repository.PackageType),
		}
		out.Self, err = s.Store.Instance().ID(ctx)
		if err != nil {
			return fmt.Errorf("read cleanup scope metadata: %w", err)
		}
		out.Base, err = configdoc.FederatedBase(ctx, s.Store.Configs(), s.BaseURL)
		if err != nil {
			return fmt.Errorf("read cleanup scope metadata: %w", err)
		}
		if out.Self == "" || out.Base == "" && len(local.Members) > 0 {
			unavailable = true
			return nil
		}
		for _, member := range local.Members {
			if model.IsSelfMember(member.URL, key, out.Base) {
				out.Receiver = member.Mode == repo.ModeReceiver
			}
		}
		resolved := s.resolveMembers(ctx, local, &out)
		// Only an expected local refusal leaves the metadata read successful.
		// Returning it from a joined Tx would poison the owner's transaction
		// before it can revalidate and publish that conservative outcome.
		unavailable = resolved == ErrUnavailable //nolint:errorlint,err113 // only our direct logical refusal may leave a joined Tx successful.
		if unavailable {
			return nil
		}
		return resolved
	})
	if err != nil {
		return api.CleanupScope{}, fmt.Errorf("current federated cleanup scope: %w", err)
	}
	if unavailable {
		return out, fmt.Errorf("current federated cleanup scope: %w", ErrUnavailable)
	}
	return out, nil
}

func (s *Service) resolveMembers(ctx context.Context, local artifactsapi.CleanupRepository, out *api.CleanupScope) error {
	members, err := s.Store.Federation().Members(ctx, out.Repository)
	if err != nil {
		return fmt.Errorf("read resolved cleanup members: %w", err)
	}
	if len(local.Members) > repo.MaxMembers || len(members) >= repo.MaxMembers {
		return ErrUnavailable
	}
	key := out.Repository
	revisions, err := s.revisions()
	if err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	seen := map[string]bool{}
	self := len(local.Members) == 0
	for _, configured := range local.Members {
		if model.IsSelfMember(configured.URL, key, out.Base) {
			if self || configured.Enabled != nil && !configured.Enabled.Bool() {
				return ErrUnavailable
			}
			self = true
			out.Receiver = out.Receiver || configured.Mode == repo.ModeReceiver
			continue
		}
		base, remoteKey, parseErr := model.ParseMemberURL(configured.URL)
		if parseErr != nil || configured.Enabled != nil && !configured.Enabled.Bool() {
			return ErrUnavailable
		}
		found := false
		for _, member := range members {
			if member.BaseURL != base || member.RemoteRepoKey != remoteKey || member.Mode != configured.Mode || !member.Enabled {
				continue
			}
			if seen[member.PeerInstanceID] {
				return ErrUnavailable
			}
			peer, err := s.Store.Peers().Get(ctx, member.PeerInstanceID)
			if err != nil {
				return fmt.Errorf("read cleanup scope metadata: %w", err)
			}
			if !peer.Compatible() || repo.NormalizeMemberBase(peer.BaseURL) != base || !federatedGrant(peer) {
				return ErrUnavailable
			}
			// The redeemer of /r receives from this peer even when a stale
			// repository entry still says bidirectional. Cleanup cannot
			// originate deletes that its current relationship will not send.
			out.Receiver = out.Receiver || peer.ReceivesFederationOnly && !peer.Has(model.UseFederatedRepo)
			revision, err := revisions.Revision(ctx, peer.InstanceID)
			if err != nil {
				return fmt.Errorf("read cleanup scope metadata: %w", err)
			}
			out.Members = append(
				out.Members,
				api.CleanupMember{
					Instance:     peer.InstanceID,
					Base:         base,
					Repository:   remoteKey,
					Mode:         string(member.Mode),
					Grant:        revision.Grant,
					Failure:      revision.Failure,
					LocalFailure: s.failures.current(peer.InstanceID, revision.Grant, s.now()),
				},
			)
			seen[peer.InstanceID], found = true, true
			break
		}
		if !found {
			return ErrUnavailable
		}
	}
	if !self || len(seen) != len(members) {
		return ErrUnavailable
	}
	slices.SortFunc(out.Members, func(a, b api.CleanupMember) int { return cmp.Compare(a.Instance, b.Instance) })
	return nil
}

func federatedGrant(peer model.Peer) bool {
	return peer.Has(model.UseFederatedRepo) || peer.Has(model.UseFederatedRepoRead)
}

func (s *Service) guardScope(ctx context.Context, expected api.CleanupScope) error {
	if !s.Store.InTx(ctx) {
		return fmt.Errorf("cleanup scope fence requires a transaction: %w", kernel.ErrStore)
	}
	actual, err := s.Scope(ctx, expected.Repository)
	if err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("federated cleanup context changed: %w", kernel.ErrConflict)
	}
	revisions, err := s.revisions()
	if err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	for _, member := range expected.Members {
		if err := revisions.GuardRevision(ctx, member.Instance, port.PeerRevision{Grant: member.Grant, Failure: member.Failure}); err != nil {
			return fmt.Errorf("read cleanup scope metadata: %w", err)
		}
	}
	return nil
}

// Fresh deliberately reads the clock again after any publication/lease wait.
func (s *Service) Fresh(ctx context.Context, evidence api.CleanupEvidence) error {
	if err := evidence.Validate(); err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	now := s.now()
	if evidence.Started.IsZero() || evidence.Expires.Sub(evidence.Started) != Window || now.Before(evidence.Started) || !now.Before(evidence.Expires) ||
		s.failures.changed(evidence.Scope.Members, now) {
		return fmt.Errorf("federated cleanup observation expired or failed: %w", kernel.ErrConflict)
	}
	return nil
}

// Guard locks local lifetime/trust publications and rechecks local recorded
// downloads. Remote writes after an observation are not claimed to be atomic.
func (s *Service) Guard(ctx context.Context, evidence api.CleanupEvidence) error {
	if err := s.Fresh(ctx, evidence); err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	if err := s.guardScope(ctx, evidence.Scope); err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	var wanted []api.CleanupVersion
	var pins []api.CleanupDownload
	for _, pin := range evidence.Pins {
		if pin.Member == evidence.Scope.Self {
			wanted = append(wanted, pin.CleanupVersion)
			pins = append(pins, pin.CleanupDownload)
		}
	}
	if len(wanted) == 0 || len(wanted) > api.CleanupBatchSize {
		return kernel.ErrInvalid
	}
	rows, err := s.localDownloads(ctx, evidence.Scope, wanted)
	if err != nil {
		return fmt.Errorf("read cleanup scope metadata: %w", err)
	}
	if !reflect.DeepEqual(rows, pins) {
		return fmt.Errorf("local download evidence changed: %w", kernel.ErrConflict)
	}
	return s.Fresh(ctx, evidence)
}

func (s *Service) localDownloads(ctx context.Context, scope api.CleanupScope, versions []api.CleanupVersion) ([]api.CleanupDownload, error) {
	local, err := s.Metadata.Repository(ctx, scope.Repository)
	if err != nil {
		return nil, fmt.Errorf("read cleanup download metadata: %w", err)
	}
	if local.Incarnation != scope.Incarnation || local.Generation != scope.Generation || local.Repository.Project != scope.Project ||
		string(local.Repository.Class) != scope.Class ||
		string(local.Repository.PackageType) != scope.PackageType {
		return nil, kernel.ErrConflict
	}
	requested := make([]artifactsapi.DownloadVersion, len(versions))
	for n, version := range versions {
		requested[n] = artifactsapi.DownloadVersion{Package: version.Package, Version: version.Version}
	}
	rows, err := s.Metadata.Downloads(ctx, local, requested)
	if err != nil {
		return nil, fmt.Errorf("read cleanup download metadata: %w", err)
	}
	byIdentity := map[api.CleanupVersion]api.CleanupDownload{}
	for _, row := range rows {
		identity := api.CleanupVersion{Package: row.Package, Version: row.Version}
		byIdentity[identity] = api.CleanupDownload{
			CleanupVersion:    identity,
			ID:                row.ID,
			Revision:          row.Revision,
			Created:           row.Created,
			LastDownloaded:    row.LastDownloaded,
			AdmittedAt:        row.AdmittedAt,
			AdmissionRevision: row.AdmissionRevision,
		}
	}
	out := make([]api.CleanupDownload, 0, len(versions))
	for _, version := range versions {
		row, found := byIdentity[version]
		if !found {
			return nil, ErrUnavailable
		}
		out = append(out, row)
	}
	return out, nil
}
