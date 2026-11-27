package cleanup

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Coordinate validates the authenticated signer again at the owning boundary.
// No metadata connection remains held while asking the coordinator for proof.
func (s *Service) Coordinate(ctx context.Context, caller model.Peer, key string, request CoordinationRequest) (CoordinationAnswer, error) {
	store, _, err := s.coordination()
	if err != nil {
		return CoordinationAnswer{}, err
	}
	if !validCoordinationReference(request) || store.InTx(ctx) {
		return CoordinationAnswer{}, kernel.ErrInvalid
	}
	var current model.Peer
	err = store.Tx(ctx, func(ctx context.Context) error {
		var err error
		current, err = store.Peers().Get(ctx, caller.InstanceID)
		if err != nil {
			return fmt.Errorf("read current cleanup signer: %w", err)
		}
		if !bytes.Equal(current.PublicKey, caller.PublicKey) || !current.Compatible() || !federatedGrant(current) {
			return kernel.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return CoordinationAnswer{}, fmt.Errorf("authenticate cleanup coordination: %w", err)
	}
	if request.Action == "outcome" {
		return s.outcome(ctx, current, key, request)
	}
	if request.Exclusion.Owner != current.InstanceID || request.Exclusion.Repository != key {
		return CoordinationAnswer{}, kernel.ErrForbidden
	}
	switch request.Action {
	case "prepare":
		return s.prepareParticipant(ctx, current, key, request)
	case "release":
		return s.releaseParticipant(ctx, current, key, request)
	default:
		return CoordinationAnswer{}, kernel.ErrInvalid
	}
}

func (s *Service) participantScope(ctx context.Context, current model.Peer, key string, request CoordinationRequest) (api.CleanupScope, error) {
	var scope api.CleanupScope
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		scope, err = s.Scope(ctx, key)
		if err != nil {
			return err
		}
		ref := request.Exclusion
		if scope.PackageType != request.PackageType || scope.Incarnation != ref.Incarnation || scope.Generation != ref.Generation {
			return kernel.ErrConflict
		}
		found := false
		for _, member := range scope.Members {
			if member.Instance == current.InstanceID && member.Repository == request.SourceRepository {
				found = true
			}
		}
		if !found {
			return kernel.ErrForbidden
		}
		return s.guardScope(ctx, scope)
	})
	if err != nil {
		return api.CleanupScope{}, fmt.Errorf("validate cleanup participant scope: %w", err)
	}
	return scope, nil
}

func (s *Service) prepareParticipant(ctx context.Context, current model.Peer, key string, request CoordinationRequest) (CoordinationAnswer, error) {
	store, exclusions, err := s.coordination()
	if err != nil {
		return CoordinationAnswer{}, err
	}
	scope, err := s.participantScope(ctx, current, key, request)
	if err != nil {
		return CoordinationAnswer{}, err
	}
	status, err := s.coordinate(
		ctx,
		current.InstanceID,
		request.SourceRepository,
		CoordinationRequest{Action: "outcome", SourceRepository: key, PackageType: request.PackageType, Exclusion: request.Exclusion},
	)
	if err != nil {
		return CoordinationAnswer{}, err
	}
	if status.State != string(model.CleanupPreparing) {
		return CoordinationAnswer{}, kernel.ErrConflict
	}
	err = store.Tx(ctx, func(ctx context.Context) error {
		actual, err := s.participantScope(ctx, current, key, request)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, scope) {
			return kernel.ErrConflict
		}
		if err := exclusions.Prepare(ctx, request.Exclusion); err != nil {
			return fmt.Errorf("persist participant exclusion: %w", err)
		}
		return nil
	})
	if err != nil {
		return CoordinationAnswer{}, fmt.Errorf("prepare cleanup participant: %w", err)
	}
	return CoordinationAnswer{State: "prepared", Exclusion: request.Exclusion}, nil
}

func (s *Service) releaseParticipant(ctx context.Context, current model.Peer, key string, request CoordinationRequest) (CoordinationAnswer, error) {
	store, exclusions, err := s.coordination()
	if err != nil {
		return CoordinationAnswer{}, err
	}
	status, err := s.coordinate(
		ctx,
		current.InstanceID,
		request.SourceRepository,
		CoordinationRequest{Action: "outcome", SourceRepository: key, PackageType: request.PackageType, Exclusion: request.Exclusion},
	)
	if err != nil {
		return CoordinationAnswer{}, err
	}
	if status.State != string(model.CleanupAborted) && status.State != string(model.CleanupCommitted) {
		return CoordinationAnswer{}, kernel.ErrConflict
	}
	err = exclusions.Release(ctx, request.Exclusion, func(ctx context.Context) error {
		// A signed terminal outcome cannot become another permission to delete.
		// Committed release still requires the exact local applied frontier.
		if status.State == string(model.CleanupAborted) {
			return nil
		}
		if status.Watermark <= 0 {
			return kernel.ErrConflict
		}
		applied, err := store.Outbox().LockApplied(ctx, current.InstanceID, model.RepositoryStream(request.SourceRepository))
		if err != nil {
			return fmt.Errorf("read participant applied cleanup frontier: %w", err)
		}
		if applied.LastSeq < status.Watermark {
			return kernel.ErrConflict
		}
		return nil
	})
	if err != nil {
		return CoordinationAnswer{}, fmt.Errorf("release cleanup participant: %w", err)
	}
	return status, nil
}

func (s *Service) outcome(ctx context.Context, caller model.Peer, key string, request CoordinationRequest) (CoordinationAnswer, error) {
	store, _, err := s.coordination()
	if err != nil {
		return CoordinationAnswer{}, err
	}
	var answer CoordinationAnswer
	err = store.Tx(ctx, func(ctx context.Context) error {
		intent, err := store.CleanupIntents().Read(ctx, request.Exclusion.ID)
		if err != nil {
			return fmt.Errorf("read cleanup outcome: %w", err)
		}
		scope := intent.Selected.Scope
		if scope.Repository != key || scope.PackageType != request.PackageType {
			return kernel.ErrForbidden
		}
		permitted := false
		for _, p := range participants(intent) {
			if p.instance == caller.InstanceID && p.repository == request.SourceRepository && reflect.DeepEqual(p.ref, request.Exclusion) {
				permitted = true
			}
		}
		if !permitted {
			return kernel.ErrForbidden
		}
		if intent.State == model.CleanupPreparing {
			if err := s.guardScope(ctx, scope); err != nil {
				return fmt.Errorf("read cleanup outcome: %w", err)
			}
		}
		answer = CoordinationAnswer{State: string(intent.State), Watermark: intent.Watermark, Exclusion: request.Exclusion}
		return nil
	})
	if err != nil {
		return CoordinationAnswer{}, fmt.Errorf("read cleanup participant outcome: %w", err)
	}
	return answer, nil
}

func validCoordinationReference(request CoordinationRequest) bool {
	ref := request.Exclusion
	for _, field := range []struct {
		value   string
		maximum int
	}{{ref.ID, 128}, {ref.Owner, 128}, {ref.Repository, 1024}, {ref.Incarnation, 128}, {ref.Generation, 128}, {request.SourceRepository, 1024}, {request.PackageType, 64}} {
		if field.value == "" || len(field.value) > field.maximum || !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			return false
		}
	}
	if len(ref.ID) < 16 || len(ref.Versions) == 0 || len(ref.Versions) > 64 {
		return false
	}
	seen := map[int64]bool{}
	names := map[[2]string]bool{}
	for _, version := range ref.Versions {
		key := [2]string{version.Package, version.Version}
		if version.ID <= 0 || seen[version.ID] || names[key] {
			return false
		}
		seen[version.ID] = true
		names[key] = true
		for _, value := range key {
			if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return false
			}
		}
	}
	return true
}
