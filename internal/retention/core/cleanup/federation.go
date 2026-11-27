package cleanup

import (
	"context"
	"errors"
	"fmt"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Federation selection reasons retain content without exposing member metadata.
const (
	ReasonFederationUnknown  Reason = "federation_evidence_unavailable"
	ReasonFederationReceiver Reason = "receiving_only_member"
)

func (s *Selection) federationCandidate(ctx context.Context, out CandidatePage) (CandidatePage, error) {
	if out.Reason != ReasonSelected || out.Version.Package.Repository.Class != repo.Federated {
		return out, nil
	}
	federation := s.service.Federation
	if federation == nil && !out.Version.Package.Repository.HasFederatedMembers {
		return out, nil
	}
	if federation == nil {
		return CandidatePage{}, fmt.Errorf("federated cleanup evidence unavailable: %w", kernel.ErrStore)
	}
	var bound federationapi.CleanupScope
	var unavailable bool
	err := s.run(ctx, func(ctx context.Context) error {
		var err error
		bound, unavailable, err = s.readFederationScope(ctx, out.Version)
		return err
	})
	if err != nil {
		return CandidatePage{}, err
	}
	var reason Reason
	var evidence *federationapi.CleanupEvidence
	switch {
	case unavailable:
		reason = ReasonFederationUnknown
	case bound.Receiver:
		reason = ReasonFederationReceiver
	default:
		proof, err := federation.Collect(ctx, bound, []federationapi.CleanupVersion{{Package: out.Version.Package.Name, Version: out.Version.Version}})
		if ctx.Err() != nil {
			return CandidatePage{}, fmt.Errorf("federated selection cancelled: %w", ctx.Err())
		}
		switch {
		case errors.Is(err, federationapi.ErrCleanupUnavailable):
			reason = ReasonFederationUnknown
		case err != nil:
			return CandidatePage{}, fmt.Errorf("federated candidate evidence: %w", err)
		default:
			evidence = &proof
			reason = s.federationDownloadReason(proof)
		}
	}
	// Every peer-derived outcome, including a conservative refusal, belongs to
	// the original caller and repository context. Network I/O has already ended.
	err = s.run(ctx, func(ctx context.Context) error {
		current, _, err := s.readFederationScope(ctx, out.Version)
		if err != nil {
			return err
		}
		if !sameFederationContext(current, bound) {
			return fmt.Errorf("federated cleanup repository context changed: %w", kernel.ErrConflict)
		}
		if evidence != nil {
			if err := federation.Guard(ctx, *evidence); err != nil {
				return fmt.Errorf("guard federated outcome evidence: %w", err)
			}
			return federation.Fresh(ctx, *evidence)
		}
		return nil
	})
	if err != nil {
		return CandidatePage{}, fmt.Errorf("current federated candidate outcome: %w", err)
	}
	out.Reason = reason
	if reason != ReasonSelected {
		out.Items, out.Next = nil, ""
		return out, nil
	}
	out.Federation = evidence
	return out, nil
}

// readFederationScope requires Selection.run's current project authority. The
// owner lifetime lock and immutable version identity bind scope acquisition to
// the version just evaluated, even after a same-key repository recreation.
func (s *Selection) readFederationScope(ctx context.Context, ref artifactsapi.IndexVersion) (federationapi.CleanupScope, bool, error) {
	if err := s.repository(ctx, ref.Package.Repository); err != nil {
		return federationapi.CleanupScope{}, false, err
	}
	scope, err := s.service.Federation.Scope(ctx, ref.Package.Repository.Key)
	unavailable := errors.Is(err, federationapi.ErrCleanupUnavailable)
	if err != nil && !unavailable {
		return federationapi.CleanupScope{}, false, fmt.Errorf("read current federation scope: %w", err)
	}
	expected := ref.Package.Repository
	if scope.Repository != expected.Key || scope.Project != expected.Project || scope.Class != string(expected.Class) || scope.PackageType != string(expected.PackageType) ||
		scope.Incarnation == "" ||
		scope.Generation == "" {
		return federationapi.CleanupScope{}, false, fmt.Errorf("federation scope differs from selected repository: %w", kernel.ErrConflict)
	}
	current, err := s.currentVersion(ctx, ref)
	if err != nil {
		return federationapi.CleanupScope{}, false, err
	}
	if !sameVersionRevision(current, ref) {
		return federationapi.CleanupScope{}, false, errCandidateChanged
	}
	return scope, unavailable, nil
}

func sameFederationContext(a, b federationapi.CleanupScope) bool {
	return a.Repository == b.Repository && a.Incarnation == b.Incarnation && a.Generation == b.Generation && a.Project == b.Project && a.Class == b.Class &&
		a.PackageType == b.PackageType &&
		a.Self == b.Self &&
		a.Base == b.Base &&
		a.Receiver == b.Receiver
}

func (s *Selection) federationDownloadReason(evidence federationapi.CleanupEvidence) Reason {
	criteria := s.criteria.criteria
	for _, pin := range evidence.Pins {
		downloaded := pin.LastDownloaded
		if len(evidence.Scope.Members) > 0 && pin.AdmittedAt.After(downloaded) {
			downloaded = pin.AdmittedAt
		}
		if downloaded.IsZero() {
			// Local format-owned leads already supplied the creation fallback.
			// Package-index creation can be newer than an imported lead.
			if pin.Member == evidence.Scope.Self {
				continue
			}
			downloaded = pin.Created
		}
		// A signed timestamp can still come from a slow member clock. Move
		// its comparison bound toward recent use; this changes no lease/expiry.
		downloaded = downloaded.Add(pin.ClockAllowance)
		if !selectionAge(downloaded, s.criteria.now, criteria.LastDownloadedBeforeInDays, criteria.LastDownloadedBeforeInMonths) {
			return ReasonDownloaded
		}
	}
	return ReasonSelected
}

// A fresh challenge may renew a time window, but it cannot adopt a new member,
// repository incarnation, grant or download generation from a saved candidate.
func sameFederation(a, b *federationapi.CleanupEvidence) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.SameDownloads(*b)
}

type federatedPermit struct {
	federation  federationapi.Cleanup
	evidence    federationapi.CleanupEvidence
	version     artifactsapi.IndexVersion
	reservation federationapi.CleanupReservation
}

func (p federatedPermit) Revalidate(ctx context.Context, key string, id int64) error {
	if key != p.evidence.Scope.Repository || key != p.version.Package.Repository.Key || id != p.version.ID || p.evidence.Scope.Receiver {
		return kernel.ErrConflict
	}
	coordinated, ok := p.federation.(federationapi.CleanupCoordination)
	if !ok || p.reservation.ID == "" {
		return kernel.ErrConflict
	}
	return coordinated.GuardReservation(ctx, p.reservation.ID) //nolint:wrapcheck // the owner identifies the failed publication fence
}

func (p federatedPermit) Fresh(ctx context.Context) error {
	return p.federation.Fresh(ctx, p.evidence) //nolint:wrapcheck // the owner identifies expiry or observed failure
}

func (p federatedPermit) Exclusion() artifactsapi.CleanupExclusion {
	scope := p.reservation.Evidence.Scope
	ref := artifactsapi.CleanupExclusion{ID: p.reservation.ID, Owner: scope.Self, Repository: scope.Repository}
	for _, pin := range p.reservation.Evidence.Pins {
		if pin.Member == scope.Self {
			ref.Incarnation, ref.Generation = pin.Incarnation, pin.Generation
			ref.Versions = append(ref.Versions, artifactsapi.CleanupVersionIdentity{
				DownloadVersion: artifactsapi.DownloadVersion{Package: pin.Package, Version: pin.Version}, ID: pin.ID,
			})
		}
	}
	return ref
}

func (p federatedPermit) Commit(ctx context.Context) error {
	coordinated, ok := p.federation.(federationapi.CleanupCoordination)
	if !ok || p.reservation.ID == "" {
		return kernel.ErrConflict
	}
	if err := coordinated.Commit(ctx, p.reservation.ID); err != nil {
		return fmt.Errorf("commit coordinated removal: %w", err)
	}
	return nil
}
