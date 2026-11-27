package cleanup

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"slices"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// CoordinationRequest carries a single exact participant binding over signed
// transport. Outcome is queried at the coordinator repository; the other
// operations name the participant repository.
type CoordinationRequest struct {
	Action, SourceRepository, PackageType string
	Exclusion                             artifactsapi.CleanupExclusion
}

// CoordinationAnswer discloses only an immutable participant outcome, never
// credentials or the private retention run identity.
type CoordinationAnswer struct {
	State     string
	Watermark int64
	Exclusion artifactsapi.CleanupExclusion
}

// CoordinationRemote uses the same current-peer authenticated transport as
// observations. Every request is independently bounded and idempotent.
type CoordinationRemote interface {
	Coordinate(context.Context, model.Peer, string, CoordinationRequest) (CoordinationAnswer, error)
}

type coordinationStore interface {
	Store
	CleanupIntents() port.CleanupIntentStore
	Outbox() outbox.OutboxStore
}

func (s *Service) coordination() (coordinationStore, artifactsapi.CleanupExclusions, error) {
	store, ok := s.Store.(coordinationStore)
	if !ok {
		return nil, nil, fmt.Errorf("cleanup coordination unavailable: %w", kernel.ErrStore)
	}
	exclusions := s.Exclusions
	if exclusions == nil && s.OpenExclusions != nil {
		exclusions = s.OpenExclusions()
	}
	if exclusions == nil {
		return nil, nil, fmt.Errorf("cleanup coordination unavailable: %w", kernel.ErrStore)
	}
	return store, exclusions, nil
}

// Reserve joins the original caller's fenced transaction. Its returned identity
// must be persisted with retention progress before any participant is contacted.
func (s *Service) Reserve(ctx context.Context, selected api.CleanupEvidence, runID, fileID int64) (string, error) {
	store, _, err := s.coordination()
	if err != nil {
		return "", fmt.Errorf("coordinate cleanup: %w", err)
	}
	if !store.InTx(ctx) {
		return "", kernel.ErrStore
	}
	if err := s.Guard(ctx, selected); err != nil {
		return "", fmt.Errorf("coordinate cleanup: %w", err)
	}
	before, err := store.Outbox().LastSeq(ctx, selected.Scope.Self, model.RepositoryStream(selected.Scope.Repository))
	if err != nil {
		return "", fmt.Errorf("coordinate cleanup: %w", err)
	}
	intent := model.CleanupIntent{BeforeWatermark: before, ID: rand.Text(), Revision: 1, RunID: runID, FileID: fileID, State: model.CleanupPreparing, Selected: selected}
	if err := store.CleanupIntents().Create(ctx, intent); err != nil {
		return "", fmt.Errorf("reserve cleanup operation: %w", err)
	}
	return intent.ID, nil
}

type participant struct {
	instance, repository string
	ref                  artifactsapi.CleanupExclusion
}

func participants(intent model.CleanupIntent) []participant {
	scope := intent.Selected.Scope
	out := make([]participant, 0, 1+len(scope.Members))
	out = append(out, participant{instance: scope.Self, repository: scope.Repository})
	for _, member := range scope.Members {
		out = append(out, participant{instance: member.Instance, repository: member.Repository})
	}
	for n := range out {
		ref := artifactsapi.CleanupExclusion{ID: intent.ID, Owner: scope.Self, Repository: out[n].repository}
		for _, pin := range intent.Selected.Pins {
			if pin.Member == out[n].instance {
				ref.Incarnation, ref.Generation = pin.Incarnation, pin.Generation
				ref.Versions = append(
					ref.Versions,
					artifactsapi.CleanupVersionIdentity{DownloadVersion: artifactsapi.DownloadVersion{Package: pin.Package, Version: pin.Version}, ID: pin.ID},
				)
			}
		}
		out[n].ref = ref
	}
	slices.SortFunc(out, func(a, b participant) int {
		if order := cmp.Compare(a.instance, b.instance); order != 0 {
			return order
		}
		return cmp.Compare(a.repository, b.repository)
	})
	return out
}

func localExclusion(intent model.CleanupIntent) artifactsapi.CleanupExclusion {
	for _, p := range participants(intent) {
		if p.instance == intent.Selected.Scope.Self {
			return p.ref
		}
	}
	return artifactsapi.CleanupExclusion{}
}

func (s *Service) coordinate(ctx context.Context, instance, key string, request CoordinationRequest) (CoordinationAnswer, error) {
	if s.Store.InTx(ctx) {
		return CoordinationAnswer{}, kernel.ErrStore
	}
	remote, ok := s.Remote.(CoordinationRemote)
	if !ok {
		return CoordinationAnswer{}, ErrUnavailable
	}
	var peer model.Peer
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		peer, err = s.Store.Peers().Get(ctx, instance)
		if err != nil {
			return fmt.Errorf("read current cleanup participant: %w", err)
		}
		return nil
	})
	if err != nil {
		return CoordinationAnswer{}, fmt.Errorf("read cleanup participant: %w", err)
	}
	bounded, cancel := context.WithTimeout(ctx, AttemptTimeout)
	defer cancel()
	answer, err := remote.Coordinate(bounded, peer, key, request)
	if err != nil {
		return CoordinationAnswer{}, fmt.Errorf("signed cleanup participant: %w", err)
	}
	if !reflect.DeepEqual(answer.Exclusion, request.Exclusion) {
		return CoordinationAnswer{}, ErrUnavailable
	}
	return answer, nil
}

// Prepare closes all admissions in deterministic order, then refreshes facts
// under those durable barriers. An error leaves a recoverable intent; it never
// opens a participant merely because a request timed out.
func (s *Service) Prepare(ctx context.Context, id string) (api.CleanupReservation, error) {
	store, exclusions, err := s.coordination()
	if err != nil {
		return api.CleanupReservation{}, fmt.Errorf("coordinate cleanup: %w", err)
	}
	if store.InTx(ctx) {
		return api.CleanupReservation{}, kernel.ErrStore
	}
	intent, err := store.CleanupIntents().Read(ctx, id)
	if err != nil {
		return api.CleanupReservation{}, fmt.Errorf("coordinate cleanup: %w", err)
	}
	if intent.State == model.CleanupPrepared {
		return api.CleanupReservation{ID: id, Evidence: intent.Prepared}, nil
	}
	if intent.State != model.CleanupPreparing {
		return api.CleanupReservation{}, kernel.ErrConflict
	}
	for _, p := range participants(intent) {
		if p.instance == intent.Selected.Scope.Self {
			err = exclusions.Prepare(ctx, p.ref)
		} else {
			answer, callErr := s.coordinate(
				ctx,
				p.instance,
				p.repository,
				CoordinationRequest{Action: "prepare", SourceRepository: intent.Selected.Scope.Repository, PackageType: intent.Selected.Scope.PackageType, Exclusion: p.ref},
			)
			err = callErr
			if err == nil && (answer.State != "prepared" || answer.Watermark != 0) {
				err = ErrUnavailable
			}
		}
		if err != nil {
			return api.CleanupReservation{}, fmt.Errorf("prepare cleanup exclusion: %w", err)
		}
	}
	versions := []api.CleanupVersion{}
	for _, pin := range intent.Selected.Pins {
		if pin.Member == intent.Selected.Scope.Self {
			versions = append(versions, pin.CleanupVersion)
		}
	}
	evidence, err := s.Collect(ctx, intent.Selected.Scope, versions)
	if err != nil {
		return api.CleanupReservation{}, fmt.Errorf("coordinate cleanup: %w", err)
	}
	// Own prior tombstones may change timestamps, but never identities or either
	// recorded-public or hidden-admission mutation generations.
	if !intent.Selected.SameDownloads(evidence) {
		return api.CleanupReservation{}, kernel.ErrConflict
	}
	err = store.Tx(ctx, func(ctx context.Context) error {
		current, err := store.CleanupIntents().Read(ctx, id)
		if err != nil {
			return fmt.Errorf("coordinate cleanup: %w", err)
		}
		if current.State != model.CleanupPreparing {
			return kernel.ErrConflict
		}
		if err := s.Guard(ctx, evidence); err != nil {
			return fmt.Errorf("coordinate cleanup: %w", err)
		}
		if err := exclusions.Guard(ctx, localExclusion(current)); err != nil {
			return fmt.Errorf("coordinate cleanup: %w", err)
		}
		next := current
		next.Revision++
		next.State = model.CleanupPrepared
		next.Prepared = evidence
		return store.CleanupIntents().Advance(ctx, current.Revision, next)
	})
	if err != nil {
		return api.CleanupReservation{}, fmt.Errorf("publish prepared cleanup operation: %w", err)
	}
	return api.CleanupReservation{ID: id, Evidence: evidence}, nil
}

// GuardReservation locks the durable intent before native mutations acquire
// stream locks. Commit reuses these locks after every companion effect.
func (s *Service) GuardReservation(ctx context.Context, id string) error {
	store, exclusions, err := s.coordination()
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if !store.InTx(ctx) {
		return kernel.ErrStore
	}
	intent, err := store.CleanupIntents().Read(ctx, id)
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if intent.State != model.CleanupPrepared {
		return kernel.ErrConflict
	}
	if err := s.Guard(ctx, intent.Prepared); err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if err := exclusions.Guard(ctx, localExclusion(intent)); err != nil {
		return fmt.Errorf("guard cleanup participant: %w", err)
	}
	return nil
}

// Commit is part of the ordinary removal transaction. FED02 appends repository
// tombstones synchronously inside native ItemChanged, so LastSeq includes every
// companion event before any participant can observe this irreversible outcome.
func (s *Service) Commit(ctx context.Context, id string) error {
	store, exclusions, err := s.coordination()
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if !store.InTx(ctx) {
		return kernel.ErrStore
	}
	intent, err := store.CleanupIntents().Read(ctx, id)
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if intent.State != model.CleanupPrepared {
		return kernel.ErrConflict
	}
	if err := s.guardScope(ctx, intent.Prepared.Scope); err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if err := s.Fresh(ctx, intent.Prepared); err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if err := exclusions.Guard(ctx, localExclusion(intent)); err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	watermark, err := store.Outbox().LastSeq(ctx, intent.Selected.Scope.Self, model.RepositoryStream(intent.Selected.Scope.Repository))
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if len(intent.Selected.Scope.Members) > 0 && watermark <= intent.BeforeWatermark {
		return kernel.ErrConflict
	}
	next := intent
	next.Revision++
	next.State = model.CleanupCommitted
	next.Watermark = watermark
	if err := store.CleanupIntents().Advance(ctx, intent.Revision, next); err != nil {
		return fmt.Errorf("publish cleanup outcome: %w", err)
	}
	return nil
}

// Abort cannot relabel a committed removal. The retention owner supplies its
// current worker fence in this transaction before publishing the terminal state.
func (s *Service) Abort(ctx context.Context, id string) error {
	store, _, err := s.coordination()
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if !store.InTx(ctx) {
		return kernel.ErrStore
	}
	intent, err := store.CleanupIntents().Read(ctx, id)
	if err != nil {
		return fmt.Errorf("coordinate cleanup: %w", err)
	}
	if intent.State == model.CleanupCommitted || intent.State == model.CleanupAborted {
		return nil
	}
	next := intent
	next.Revision++
	next.State = model.CleanupAborted
	if err := store.CleanupIntents().Advance(ctx, intent.Revision, next); err != nil {
		return fmt.Errorf("publish cleanup outcome: %w", err)
	}
	return nil
}

// Finish attempts every unacknowledged participant independently. One unavailable
// peer cannot strand another whose irreversible outcome already permits release.
// Per-member acknowledgements survive retries; a lost reply safely replays the
// participant's exact durable terminal binding even after its repository changes.
func (s *Service) Finish(ctx context.Context, id string) error {
	store, exclusions, err := s.coordination()
	if err != nil {
		return err
	}
	if store.InTx(ctx) {
		return kernel.ErrStore
	}
	intent, err := store.CleanupIntents().Read(ctx, id)
	if err != nil {
		return fmt.Errorf("read cleanup settlement: %w", err)
	}
	if intent.Released {
		return nil
	}
	if intent.State != model.CleanupCommitted && intent.State != model.CleanupAborted {
		return kernel.ErrConflict
	}
	var unresolved error
	for _, p := range participants(intent) {
		if slices.Contains(intent.ReleasedMembers, p.instance) {
			continue
		}
		if p.instance == intent.Selected.Scope.Self {
			err = exclusions.Release(ctx, p.ref, func(ctx context.Context) error {
				current, err := store.CleanupIntents().Read(ctx, id)
				if err != nil {
					return fmt.Errorf("read terminal cleanup outcome: %w", err)
				}
				if current.State != intent.State || current.Watermark != intent.Watermark {
					return kernel.ErrConflict
				}
				return nil
			})
		} else {
			answer, callErr := s.coordinate(
				ctx,
				p.instance,
				p.repository,
				CoordinationRequest{Action: "release", SourceRepository: intent.Selected.Scope.Repository, PackageType: intent.Selected.Scope.PackageType, Exclusion: p.ref},
			)
			err = callErr
			if err == nil && (answer.State != string(intent.State) || answer.Watermark != intent.Watermark) {
				err = ErrUnavailable
			}
		}
		if err == nil {
			err = s.acknowledgeRelease(ctx, intent, p.instance)
		}
		unresolved = errors.Join(unresolved, err)
	}
	// Another recovery worker may already have completed the same immutable
	// outcome while this attempt encountered an obsolete transport failure.
	current, readErr := store.CleanupIntents().Read(ctx, id)
	if readErr == nil && current.Released {
		return nil
	}
	if unresolved = errors.Join(unresolved, readErr); unresolved != nil {
		return fmt.Errorf("cleanup participant settlement unresolved: %w", unresolved)
	}
	return fmt.Errorf("cleanup participant acknowledgements incomplete: %w", kernel.ErrConflict)
}

func (s *Service) acknowledgeRelease(ctx context.Context, expected model.CleanupIntent, member string) error {
	store, _, err := s.coordination()
	if err != nil {
		return err
	}
	err = store.Tx(ctx, func(ctx context.Context) error {
		current, err := store.CleanupIntents().Read(ctx, expected.ID)
		if err != nil {
			return fmt.Errorf("lock cleanup release acknowledgement: %w", err)
		}
		if current.State != expected.State || current.Watermark != expected.Watermark {
			return kernel.ErrConflict
		}
		if slices.Contains(current.ReleasedMembers, member) {
			return nil
		}
		next := current
		next.Revision++
		next.ReleasedMembers = append(slices.Clone(current.ReleasedMembers), member)
		slices.Sort(next.ReleasedMembers)
		next.Released = len(next.ReleasedMembers) == 1+len(next.Selected.Scope.Members)
		return store.CleanupIntents().Advance(ctx, current.Revision, next)
	})
	if err != nil {
		return fmt.Errorf("record cleanup participant release: %w", err)
	}
	return nil
}
