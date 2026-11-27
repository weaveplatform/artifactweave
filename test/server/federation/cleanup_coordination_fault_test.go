package federation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

var errCoordinationPersistence = errors.New("injected cleanup coordinator persistence failure")

type coordinationFaultStore struct {
	cleanupWorldStore
	intents *coordinationFaultIntents
	events  *coordinationFaultOutbox
}

func (s coordinationFaultStore) CleanupIntents() port.CleanupIntentStore { return s.intents }
func (s coordinationFaultStore) Outbox() outbox.OutboxStore              { return s.events }

type coordinationFaultIntents struct {
	port.CleanupIntentStore
	operation     string
	calls, failAt int
	created       string
}

func (s *coordinationFaultIntents) fail(operation string) bool {
	if s.operation != operation {
		return false
	}
	s.calls++
	return s.calls == s.failAt
}

func (s *coordinationFaultIntents) Read(ctx context.Context, id string) (model.CleanupIntent, error) {
	if s.fail("read") {
		return model.CleanupIntent{}, errCoordinationPersistence
	}
	return s.CleanupIntentStore.Read(ctx, id)
}

func (s *coordinationFaultIntents) Create(ctx context.Context, intent model.CleanupIntent) error {
	if err := s.CleanupIntentStore.Create(ctx, intent); err != nil {
		return err
	}
	s.created = intent.ID
	if s.fail("create") {
		return errCoordinationPersistence
	}
	return nil
}

func (s *coordinationFaultIntents) Advance(ctx context.Context, revision int64, intent model.CleanupIntent) error {
	if err := s.CleanupIntentStore.Advance(ctx, revision, intent); err != nil {
		return err
	}
	if s.fail("advance") {
		return errCoordinationPersistence
	}
	return nil
}

type coordinationFaultOutbox struct {
	outbox.OutboxStore
	armed bool
}

func (s *coordinationFaultOutbox) LastSeq(ctx context.Context, origin, stream string) (int64, error) {
	if s.armed {
		return 0, errCoordinationPersistence
	}
	return s.OutboxStore.LastSeq(ctx, origin, stream)
}

type coordinationFaultExclusions struct {
	artifactsapi.CleanupExclusions
	operation string
}

func (s *coordinationFaultExclusions) Prepare(ctx context.Context, ref artifactsapi.CleanupExclusion) error {
	if s.operation == "prepare" {
		return errCoordinationPersistence
	}
	return s.CleanupExclusions.Prepare(ctx, ref)
}

func (s *coordinationFaultExclusions) Guard(ctx context.Context, ref artifactsapi.CleanupExclusion) error {
	if s.operation == "guard" {
		return errCoordinationPersistence
	}
	return s.CleanupExclusions.Guard(ctx, ref)
}

func (s *coordinationFaultExclusions) Release(ctx context.Context, ref artifactsapi.CleanupExclusion, proof func(context.Context) error) error {
	if s.operation == "release" {
		return errCoordinationPersistence
	}
	return s.CleanupExclusions.Release(ctx, ref, proof)
}

// Failures after real writes must roll back the durable state transition; a
// participant already closed by Prepare stays closed until durable settlement.
func TestAccCleanupCoordinatorPersistenceFaults(t *testing.T) {
	for _, phase := range []string{"reserve", "prepare", "guard", "commit", "abort", "finish"} {
		operations := []string{"read", "advance"}
		switch phase {
		case "reserve":
			operations = []string{"last_sequence", "create"}
		case "prepare":
			operations = []string{"read", "prepare", "guard", "advance"}
		case "guard":
			operations = []string{"read", "guard"}
		case "commit":
			operations = []string{"read", "guard", "last_sequence", "advance"}
		case "finish":
			operations = []string{"read", "release", "ack_read", "advance"}
		}
		for _, operation := range operations {
			t.Run(phase+"/"+operation, func(t *testing.T) {
				ctx := t.Context()
				w := newWorld(t, 0, 1)
				evidence := w.collect(t)
				original := w.store
				intents := &coordinationFaultIntents{CleanupIntentStore: original.CleanupIntents(), failAt: 1}
				events := &coordinationFaultOutbox{OutboxStore: original.Outbox()}
				exclusions := &coordinationFaultExclusions{CleanupExclusions: w.service.Exclusions}
				w.service.Store = coordinationFaultStore{original, intents, events}
				w.service.Exclusions = exclusions
				var id string
				reserve := func(ctx context.Context) error {
					var err error
					id, err = w.service.Reserve(ctx, evidence, 7, w.firstItem)
					return err
				}
				if phase != "reserve" {
					storekit.Must(t, original.Tx(ctx, reserve))
				}
				if phase == "guard" || phase == "commit" || phase == "abort" || phase == "finish" {
					_, err := w.service.Prepare(ctx, id)
					storekit.Must(t, err)
				}
				if phase == "finish" {
					storekit.Must(t, original.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
				}
				intents.operation = operation
				if operation == "ack_read" {
					intents.operation = "read"
					intents.failAt = 3
				}
				events.armed = operation == "last_sequence"
				exclusions.operation = operation
				var err error
				switch phase {
				case "reserve":
					err = original.Tx(ctx, reserve)
				case "prepare":
					_, err = w.service.Prepare(ctx, id)
				case "guard":
					err = original.Tx(ctx, func(ctx context.Context) error { return w.service.GuardReservation(ctx, id) })
				case "commit":
					err = original.Tx(ctx, func(ctx context.Context) error { return w.service.Commit(ctx, id) })
				case "abort":
					err = original.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) })
				case "finish":
					err = w.service.Finish(ctx, id)
				}
				storekit.WantErr(t, err, errCoordinationPersistence)
				intents.operation = ""
				events.armed = false
				exclusions.operation = ""
				if phase == "reserve" {
					if id != "" {
						t.Fatal("failed reservation disclosed destructive authority", id)
					}
					if intents.created != "" {
						_, err = original.CleanupIntents().Read(ctx, intents.created)
						storekit.WantErr(t, err, kernel.ErrNotFound)
					}
				} else {
					intent := storekit.Get[model.CleanupIntent](t)(original.CleanupIntents().Read(ctx, id))
					expected := model.CleanupPrepared
					if phase == "prepare" {
						expected = model.CleanupPreparing
					}
					if phase == "finish" {
						expected = model.CleanupAborted
					}
					if intent.State != expected || intent.Released {
						t.Fatal("failed publication escaped its durable boundary", intent.State, intent.Released)
					}
					if phase != "prepare" || operation != "read" && operation != "prepare" {
						if phase != "finish" || operation == "read" || operation == "release" {
							_, err = exclusions.Admit(ctx, "packages", w.firstItem)
							storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
						}
					}
					// Replaying settlement after a rolled-back ack is also safe when the
					// member has already physically released its exact terminal binding.
					storekit.Must(t, original.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
					storekit.Must(t, w.service.Finish(ctx, id))
					settled := storekit.Get[model.CleanupIntent](t)(original.CleanupIntents().Read(ctx, id))
					if !settled.Released || settled.State != model.CleanupAborted {
						t.Fatal("recovery lost terminal outcome", settled)
					}
				}
				pin := storekit.Get[artifactsapi.CleanupAdmission](t)(exclusions.Admit(ctx, "packages", w.firstItem))
				storekit.Must(t, exclusions.Complete(ctx, pin))
			})
		}
	}
}

func TestAccCleanupCoordinatorRejectsWrongTransactionOwnership(t *testing.T) {
	w := newWorld(t, 0, 1)
	ctx := t.Context()
	evidence := w.collect(t)
	_, err := w.service.Reserve(ctx, evidence, 1, w.firstItem)
	storekit.WantErr(t, err, kernel.ErrStore)
	var id string
	storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
		var err error
		id, err = w.service.Reserve(ctx, evidence, 1, w.firstItem)
		return err
	}))
	for name, call := range map[string]func(context.Context) error{
		"guard":  func(ctx context.Context) error { return w.service.GuardReservation(ctx, id) },
		"commit": func(ctx context.Context) error { return w.service.Commit(ctx, id) },
		"abort":  func(ctx context.Context) error { return w.service.Abort(ctx, id) },
	} {
		t.Run(name, func(t *testing.T) { storekit.WantErr(t, call(ctx), kernel.ErrStore) })
	}
	storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
		_, err := w.service.Prepare(ctx, id)
		storekit.WantErr(t, err, kernel.ErrStore)
		storekit.WantErr(t, w.service.Finish(ctx, id), kernel.ErrStore)
		return nil
	}))
	storekit.WantErr(t, w.service.Finish(ctx, id), kernel.ErrConflict)
	storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
	_, err = w.service.Prepare(ctx, id)
	storekit.WantErr(t, err, kernel.ErrConflict)
	storekit.Must(t, w.service.Finish(ctx, id))
}

// Missing runtime dependencies never create an intent or change an admission.
func TestAccCleanupCoordinatorMissingRuntimeFailsClosed(t *testing.T) {
	for _, mode := range []string{"no_coordination_store", "no_exclusions", "nil_lazy_exclusions"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, 0, 1)
			ctx := t.Context()
			evidence := w.collect(t)
			switch mode {
			case "no_coordination_store":
				w.service.Store = struct{ cleanup.Store }{w.store}
			case "no_exclusions":
				w.service.Exclusions = nil
			case "nil_lazy_exclusions":
				w.service.Exclusions = nil
				w.service.OpenExclusions = func() artifactsapi.CleanupExclusions { return nil }
			}
			_, err := w.service.Reserve(ctx, evidence, 1, w.firstItem)
			storekit.WantErr(t, err, kernel.ErrStore)
			_, err = w.service.Prepare(ctx, "unavailable")
			storekit.WantErr(t, err, kernel.ErrStore)
			storekit.WantErr(t, w.service.GuardReservation(ctx, "unavailable"), kernel.ErrStore)
			storekit.WantErr(t, w.service.Commit(ctx, "unavailable"), kernel.ErrStore)
			storekit.WantErr(t, w.service.Abort(ctx, "unavailable"), kernel.ErrStore)
			storekit.WantErr(t, w.service.Finish(ctx, "unavailable"), kernel.ErrStore)
			pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.artifacts.(interface {
				CleanupExclusions() artifactsapi.CleanupExclusions
			}).CleanupExclusions().Admit(ctx, "packages", w.firstItem))
			storekit.Must(t, w.artifacts.(interface {
				CleanupExclusions() artifactsapi.CleanupExclusions
			}).CleanupExclusions().Complete(ctx, pin))
		})
	}
}

func TestAccCleanupCoordinatorRejectsObsoleteOrAmbiguousTransitions(t *testing.T) {
	for _, mode := range []string{"prepare_second_read", "prepare_changed_downloads", "prepare_final_metadata", "prepare_bad_remote_state", "prepare_bad_remote_watermark", "prepare_missing_remote_capability", "prepare_peer_read", "prepare_idempotent", "guard_before_prepare", "commit_before_prepare", "commit_changed_grant", "commit_expired_proof", "commit_without_tombstone", "finish_proof_read", "finish_final_read", "finish_remote_outcome"} {
		t.Run(mode, func(t *testing.T) {
			members := 1
			if strings.HasPrefix(mode, "finish_") && mode != "finish_remote_outcome" {
				members = 0
			}
			w := newWorld(t, members, 1)
			ctx := t.Context()
			now := time.Now()
			w.service.Now = func() time.Time { return now }
			evidence := w.collect(t)
			var id string
			storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
				var err error
				id, err = w.service.Reserve(ctx, evidence, 7, w.firstItem)
				return err
			}))
			originalMetadata := w.service.Metadata
			originalRemote := w.service.Remote
			calls := 0
			coordinate := func(_ context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
				calls++
				state := "prepared"
				var watermark int64
				if r.Action == "release" {
					intent, err := w.store.CleanupIntents().Read(ctx, id)
					if err != nil {
						return cleanup.CoordinationAnswer{}, err
					}
					state = string(intent.State)
					watermark = intent.Watermark
				}
				if mode == "prepare_bad_remote_state" && r.Action == "prepare" {
					state = "aborted"
				}
				if mode == "prepare_bad_remote_watermark" && r.Action == "prepare" {
					watermark = 1
				}
				if mode == "finish_remote_outcome" && r.Action == "release" {
					state = "preparing"
				}
				return cleanup.CoordinationAnswer{State: state, Watermark: watermark, Exclusion: r.Exclusion}, nil
			}
			w.service.Remote = coordinationOutcomeRemote{originalRemote, coordinate}
			intents := &coordinationFaultIntents{CleanupIntentStore: w.store.CleanupIntents(), failAt: 2}
			switch mode {
			case "prepare_second_read":
				intents.operation = "read"
				w.service.Store = coordinationFaultStore{w.store, intents, &coordinationFaultOutbox{OutboxStore: w.store.Outbox()}}
			case "prepare_changed_downloads":
				w.service.Remote = coordinationOutcomeRemote{remoteFunc(func(_ context.Context, p model.Peer, _ string, r cleanup.Request) (cleanup.Answer, error) {
					out := answer(p, r)
					out.Versions[0].Revision = "changed-after-selection"
					return out, nil
				}), coordinate}
			case "prepare_final_metadata":
				w.service.Metadata = &cleanupMetadataFault{CleanupMetadata: originalMetadata, downloadsAt: 3}
			case "prepare_missing_remote_capability":
				w.service.Remote = originalRemote
			case "prepare_peer_read":
				peers := &cleanupPeerFault{PeerStore: w.store.Peers(), PeerRevisions: w.store.Peers().(port.PeerRevisions), getAt: 1}
				w.service.Store = cleanupDependencyFault{cleanupWorldStore: w.store, peers: peers}
			}
			expected := error(kernel.ErrConflict)
			if mode == "prepare_second_read" || mode == "prepare_final_metadata" || mode == "prepare_peer_read" {
				expected = errCoordinationPersistence
			}
			if mode == "prepare_bad_remote_state" || mode == "prepare_bad_remote_watermark" || mode == "prepare_missing_remote_capability" || mode == "finish_remote_outcome" {
				expected = cleanup.ErrUnavailable
			}
			beforePrepare := mode == "guard_before_prepare" || mode == "commit_before_prepare"
			var err error
			if !beforePrepare {
				_, err = w.service.Prepare(ctx, id)
			}
			if strings.HasPrefix(mode, "prepare_") && mode != "prepare_idempotent" {
				storekit.WantErr(t, err, expected)
			} else {
				storekit.Must(t, err)
				switch mode {
				case "prepare_idempotent":
					previous := calls
					again, err := w.service.Prepare(ctx, id)
					storekit.Must(t, err)
					if calls != previous || again.ID != id {
						t.Fatal("idempotent prepare recontacted or changed binding")
					}
				case "guard_before_prepare":
					err = w.store.Tx(ctx, func(ctx context.Context) error { return w.service.GuardReservation(ctx, id) })
				case "commit_before_prepare":
					err = w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Commit(ctx, id) })
				case "commit_changed_grant":
					peer := storekit.Get[model.Peer](t)(w.store.Peers().Get(ctx, evidence.Scope.Members[0].Instance))
					storekit.Must(t, w.store.Peers().Put(ctx, peer))
					err = w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Commit(ctx, id) })
				case "commit_expired_proof":
					now = now.Add(cleanup.Window)
					err = w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Commit(ctx, id) })
				case "commit_without_tombstone":
					err = w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Commit(ctx, id) })
				default:
					storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
					if mode != "finish_remote_outcome" {
						intents.operation = "read"
						intents.failAt = 2
						if mode == "finish_final_read" {
							intents.failAt = 4
						}
						w.service.Store = coordinationFaultStore{w.store, intents, &coordinationFaultOutbox{OutboxStore: w.store.Outbox()}}
						expected = errCoordinationPersistence
					}
					err = w.service.Finish(ctx, id)
				}
				if mode != "prepare_idempotent" {
					storekit.WantErr(t, err, expected)
				}
			}
			w.service.Store = w.store
			w.service.Metadata = originalMetadata
			intents.operation = ""
			w.service.Remote = coordinationOutcomeRemote{originalRemote, func(_ context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
				intent, err := w.store.CleanupIntents().Read(ctx, id)
				return cleanup.CoordinationAnswer{State: string(intent.State), Watermark: intent.Watermark, Exclusion: r.Exclusion}, err
			}}
			intent := storekit.Get[model.CleanupIntent](t)(w.store.CleanupIntents().Read(ctx, id))
			if intent.State == model.CleanupCommitted {
				t.Fatal("refused transition published destructive success")
			}
			storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
			storekit.Must(t, w.service.Finish(ctx, id))
			_, err = w.artifacts.Items().GetByID(ctx, w.firstItem)
			storekit.Must(t, err)
			pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
			storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
		})
	}
}
