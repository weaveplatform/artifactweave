package federation_test

import (
	"context"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type coordinationOutcomeRemote struct {
	cleanup.Remote
	call func(context.Context, cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error)
}

func (r coordinationOutcomeRemote) Coordinate(ctx context.Context, _ model.Peer, _ string, request cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
	return r.call(ctx, request)
}

type participantFrontierFault struct{ outbox.OutboxStore }

func (s participantFrontierFault) LockApplied(context.Context, string, string) (outbox.AppliedMarker, error) {
	return outbox.AppliedMarker{}, errCoordinationPersistence
}

type participantStoreFault struct{ cleanupWorldStore }

func (s participantStoreFault) Outbox() outbox.OutboxStore {
	return participantFrontierFault{s.cleanupWorldStore.Outbox()}
}

func participantRequest(evidence api.CleanupEvidence, owner string) cleanup.CoordinationRequest {
	ref := artifactsapi.CleanupExclusion{ID: "participant-operation-1234", Owner: owner, Repository: evidence.Scope.Repository}
	for _, pin := range evidence.Pins {
		if pin.Member == evidence.Scope.Self {
			ref.Incarnation = pin.Incarnation
			ref.Generation = pin.Generation
			ref.Versions = append(
				ref.Versions,
				artifactsapi.CleanupVersionIdentity{ID: pin.ID, DownloadVersion: artifactsapi.DownloadVersion{Package: pin.Package, Version: pin.Version}},
			)
		}
	}
	return cleanup.CoordinationRequest{Action: "prepare", SourceRepository: "remote", PackageType: evidence.Scope.PackageType, Exclusion: ref}
}

// Authenticated ownership, reciprocal membership and current lifetime all have
// to agree before even an authenticated remote outcome can close admission.
func TestAccCleanupParticipantRejectsUnboundAuthority(t *testing.T) {
	for _, mode := range []string{"missing_signer", "wrong_signing_key", "revoked_grant", "wrong_owner", "wrong_repository", "wrong_type", "wrong_lifetime", "wrong_generation", "not_reciprocal", "unsupported_action", "inside_transaction", "short_operation", "duplicate_version", "invalid_version_name", "oversized_version_name"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, 1, 1)
			ctx := t.Context()
			evidence := w.collect(t)
			caller := storekit.Get[model.Peer](t)(w.store.Peers().Get(ctx, evidence.Scope.Members[0].Instance))
			request := participantRequest(evidence, caller.InstanceID)
			calls := 0
			w.service.Remote = coordinationOutcomeRemote{w.service.Remote, func(ctx context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
				calls++
				return cleanup.CoordinationAnswer{State: string(model.CleanupPreparing), Exclusion: r.Exclusion}, nil
			}}
			var expected error = kernel.ErrForbidden
			switch mode {
			case "missing_signer":
				caller.InstanceID = "removed-signer"
				expected = kernel.ErrNotFound
			case "wrong_signing_key":
				caller.PublicKey = []byte("different-public-key")
			case "revoked_grant":
				current := caller
				current.UseCases = nil
				storekit.Must(t, w.store.Peers().Put(ctx, current))
			case "wrong_owner":
				request.Exclusion.Owner = "foreign-owner"
			case "wrong_repository":
				request.Exclusion.Repository = "another-repository"
			case "wrong_type":
				request.PackageType = "generic"
				expected = kernel.ErrConflict
			case "wrong_lifetime":
				request.Exclusion.Incarnation = "replacement"
				expected = kernel.ErrConflict
			case "wrong_generation":
				request.Exclusion.Generation = "replacement"
				expected = kernel.ErrConflict
			case "not_reciprocal":
				request.SourceRepository = "unrelated-repository"
			case "unsupported_action":
				request.Action = "delete"
				expected = kernel.ErrInvalid
			case "inside_transaction":
				expected = kernel.ErrInvalid
			case "short_operation":
				request.Exclusion.ID = "short"
				expected = kernel.ErrInvalid
			case "duplicate_version":
				request.Exclusion.Versions = append(request.Exclusion.Versions, request.Exclusion.Versions[0])
				expected = kernel.ErrInvalid
			case "invalid_version_name":
				request.Exclusion.Versions[0].Version = "invalid\x00version"
				expected = kernel.ErrInvalid
			case "oversized_version_name":
				request.Exclusion.Versions[0].Package = strings.Repeat("p", 4097)
				expected = kernel.ErrInvalid
			}
			call := func(ctx context.Context) error {
				answer, err := w.service.Coordinate(ctx, caller, "packages", request)
				if answer.State != "" {
					t.Fatal("refused request disclosed authority", answer.State)
				}
				return err
			}
			var err error
			if mode == "inside_transaction" {
				err = w.store.Tx(ctx, call)
			} else {
				err = call(ctx)
			}
			storekit.WantErr(t, err, expected)
			if calls != 0 {
				t.Fatal("unbound request reached coordinator", calls)
			}
			pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
			storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
		})
	}
}

func TestAccCleanupParticipantOutcomeFailuresKeepGate(t *testing.T) {
	for _, mode := range []string{"prepare_transport", "prepare_binding", "prepare_terminal", "prepare_second_scope", "prepare_persistence", "release_transport", "release_pending", "release_zero_frontier", "release_frontier_read"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, 1, 1)
			ctx := t.Context()
			evidence := w.collect(t)
			caller := storekit.Get[model.Peer](t)(w.store.Peers().Get(ctx, evidence.Scope.Members[0].Instance))
			request := participantRequest(evidence, caller.InstanceID)
			release := strings.HasPrefix(mode, "release_")
			if release {
				storekit.Must(t, w.service.Exclusions.Prepare(ctx, request.Exclusion))
				request.Action = "release"
			}
			fault := &coordinationFaultExclusions{CleanupExclusions: w.service.Exclusions}
			w.service.Exclusions = fault
			if mode == "prepare_persistence" {
				fault.operation = "prepare"
			}
			if mode == "release_frontier_read" {
				w.service.Store = participantStoreFault{w.store}
			}
			calls := 0
			w.service.Remote = coordinationOutcomeRemote{w.service.Remote, func(ctx context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
				calls++
				if w.store.InTx(ctx) {
					t.Fatal("outcome request retained metadata transaction")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("outcome request has no bound")
				}
				state := string(model.CleanupPreparing)
				if release {
					state = string(model.CleanupCommitted)
				}
				answer := cleanup.CoordinationAnswer{State: state, Watermark: 7, Exclusion: r.Exclusion}
				switch mode {
				case "prepare_transport", "release_transport":
					return cleanup.CoordinationAnswer{}, errCoordinationPersistence
				case "prepare_binding":
					answer.Exclusion.ID = "foreign-operation"
				case "prepare_terminal":
					answer.State = string(model.CleanupAborted)
				case "prepare_second_scope":
					peer := storekit.Get[model.Peer](t)(w.store.Peers().Get(ctx, caller.InstanceID))
					storekit.Must(t, w.store.Peers().Put(ctx, peer))
				case "release_pending":
					answer.State = string(model.CleanupPreparing)
				case "release_zero_frontier":
					answer.Watermark = 0
				}
				return answer, nil
			}}
			_, err := w.service.Coordinate(ctx, caller, "packages", request)
			if err == nil || calls != 1 {
				t.Fatal("unknown outcome authorized participant", mode, calls)
			}
			if mode == "prepare_transport" || mode == "release_transport" || mode == "prepare_persistence" || mode == "release_frontier_read" {
				storekit.WantErr(t, err, errCoordinationPersistence)
			}
			fault.operation = ""
			if release {
				_, err = w.service.Exclusions.Admit(ctx, "packages", w.firstItem)
				storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
				w.service.Remote = coordinationOutcomeRemote{w.service.Remote, func(_ context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
					return cleanup.CoordinationAnswer{State: string(model.CleanupAborted), Exclusion: r.Exclusion}, nil
				}}
				_, err = w.service.Coordinate(ctx, caller, "packages", request)
				storekit.Must(t, err)
			}
			pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
			storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
		})
	}
}

func TestAccCleanupOutcomeDisclosureRequiresExactParticipant(t *testing.T) {
	for _, mode := range []string{"missing_intent", "wrong_repository", "wrong_type", "wrong_participant_repository", "wrong_binding", "stale_preparing_grant"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, 1, 1)
			ctx := t.Context()
			evidence := w.collect(t)
			var id string
			storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
				var err error
				id, err = w.service.Reserve(ctx, evidence, 7, w.firstItem)
				return err
			}))
			caller := storekit.Get[model.Peer](t)(w.store.Peers().Get(ctx, evidence.Scope.Members[0].Instance))
			request := cleanup.CoordinationRequest{
				Action:           "outcome",
				SourceRepository: "remote",
				PackageType:      evidence.Scope.PackageType,
				Exclusion:        artifactsapi.CleanupExclusion{ID: id, Owner: evidence.Scope.Self, Repository: "remote"},
			}
			for _, pin := range evidence.Pins {
				if pin.Member == caller.InstanceID {
					request.Exclusion.Incarnation = pin.Incarnation
					request.Exclusion.Generation = pin.Generation
					request.Exclusion.Versions = append(
						request.Exclusion.Versions,
						artifactsapi.CleanupVersionIdentity{ID: pin.ID, DownloadVersion: artifactsapi.DownloadVersion{Package: pin.Package, Version: pin.Version}},
					)
				}
			}
			key := "packages"
			var expected error = kernel.ErrForbidden
			switch mode {
			case "missing_intent":
				request.Exclusion.ID = "nonexistent-operation-1234"
				expected = kernel.ErrNotFound
			case "wrong_repository":
				key = "another-repository"
			case "wrong_type":
				request.PackageType = "generic"
			case "wrong_participant_repository":
				request.SourceRepository = "unrelated"
			case "wrong_binding":
				request.Exclusion.Versions[0].ID++
			case "stale_preparing_grant":
				storekit.Must(t, w.store.Peers().Put(ctx, caller))
				expected = kernel.ErrConflict
			}
			answer, err := w.service.Coordinate(ctx, caller, key, request)
			storekit.WantErr(t, err, expected)
			if answer.State != "" {
				t.Fatal("refused disclosure exposed outcome", answer.State)
			}
			intent := storekit.Get[model.CleanupIntent](t)(w.store.CleanupIntents().Read(ctx, id))
			if intent.State != model.CleanupPreparing || intent.Released {
				t.Fatal("disclosure mutated authority", intent.State)
			}
			pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
			storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
		})
	}
}
