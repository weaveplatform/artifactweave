package federation_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type coordinationWorldFactory func(*testing.T, string) *world

func TestAccCleanupCoordinatedExclusion(t *testing.T) {
	factory := func(t *testing.T, key string) *world { return newWorldWithKey(t, key, 0, 2) }
	coordinatedExclusionContract(t, factory)
	coordinatedRecoveryContract(t, factory)
}

// Both independent owners use actual migrated databases and signed loopback
// RPCs. Serial barriers establish adversarial orders without a timing sleep.
func TestAccCleanupCoordinatedExclusionSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			factory := func(t *testing.T, key string) *world { db := newStore(t); return newWorldOver(t, key, 0, 2, db, db) }
			coordinatedExclusionContract(t, factory)
			coordinatedRecoveryContract(t, factory)
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func coordinatedExclusionContract(t *testing.T, newWorld coordinationWorldFactory) {
	t.Helper()
	for _, scenario := range []string{"admission_wins", "barrier_wins_abort_replay", "unknown_reader_never_expires", "committed_waits_for_tombstone", "rollback_keeps_prepared", "membership_fenced"} {
		t.Run(scenario, func(t *testing.T) {
			a := signedCleanupNodeOver(t, "coordinator", newWorld(t, "coordinator"), nil)
			b := signedCleanupNodeOver(t, "participant", newWorld(t, "participant"), nil)
			connectCleanupNodes(t, a, b)
			ctx := t.Context()
			scope := storekit.Get[api.CleanupScope](t)(a.service.Scope(ctx, a.key))
			evidence := storekit.Get[api.CleanupEvidence](t)(a.service.Collect(ctx, scope, a.versions[:1]))
			var id string
			storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error {
				var err error
				id, err = a.service.Reserve(ctx, evidence, 1, a.firstItem)
				return err
			}))
			var remoteRef artifactsapi.CleanupExclusion
			for _, pin := range evidence.Pins {
				if pin.Member != scope.Self {
					remoteRef = artifactsapi.CleanupExclusion{
						ID:          id,
						Owner:       scope.Self,
						Repository:  b.key,
						Incarnation: pin.Incarnation,
						Generation:  pin.Generation,
						Versions:    []artifactsapi.CleanupVersionIdentity{{DownloadVersion: artifactsapi.DownloadVersion{Package: pin.Package, Version: pin.Version}, ID: pin.ID}},
					}
				}
			}
			reserveReader := func() artifactsapi.CleanupAdmission {
				return storekit.Get[artifactsapi.CleanupAdmission](t)(b.service.Exclusions.Admit(ctx, b.key, b.firstItem))
			}
			finishAbort := func() {
				t.Helper()
				storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error { return a.service.Abort(ctx, id) }))
				storekit.Must(t, a.service.Finish(ctx, id))
			}
			if scenario == "admission_wins" || scenario == "unknown_reader_never_expires" {
				reader := reserveReader()
				if scenario == "unknown_reader_never_expires" {
					a.service.Now = func() time.Time { return time.Now().Add(100 * 365 * 24 * time.Hour) }
				}
				_, err := a.service.Prepare(ctx, id)
				if err == nil {
					t.Fatal("active or unknown admitted response permitted cleanup")
				}
				// Reconstructing the owner models a process restart: no liveness expiry is
				// substituted for the durable ambiguous admission.
				previous := b.service
				b.service = &cleanup.Service{
					Store:      previous.Store,
					Metadata:   previous.Metadata,
					Exclusions: previous.Exclusions,
					Remote:     previous.Remote,
					BaseURL:    previous.BaseURL,
					Now:        previous.Now,
				}
				extra := reserveReader()
				storekit.Must(t, b.service.Exclusions.Complete(ctx, extra))
				finishAbort()
				storekit.Must(t, b.service.Exclusions.Complete(ctx, reader))
				return
			}
			reservation := storekit.Get[api.CleanupReservation](t)(a.service.Prepare(ctx, id))
			_, err := b.service.Exclusions.Admit(ctx, b.key, b.firstItem)
			storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
			if scenario == "membership_fenced" {
				peer := storekit.Get[model.Peer](t)(a.store.Peers().Get(ctx, scope.Members[0].Instance))
				storekit.Must(t, a.store.Peers().Put(ctx, peer))
				err = a.store.Tx(ctx, func(ctx context.Context) error { return a.service.GuardReservation(ctx, id) })
				storekit.WantErr(t, err, kernel.ErrConflict)
				finishAbort()
				return
			}
			if scenario == "barrier_wins_abort_replay" {
				finishAbort()
				reader := reserveReader()
				storekit.Must(t, b.service.Exclusions.Complete(ctx, reader))
				// Release writes a permanent terminal binding even on an idempotent retry.
				storekit.Must(t, a.service.Finish(ctx, id))
				err = b.service.Exclusions.Prepare(ctx, remoteRef)
				storekit.WantErr(t, err, kernel.ErrConflict)
				return
			}
			rollback := errors.New("rollback after committed outcome was staged")
			err = a.store.Tx(ctx, func(ctx context.Context) error {
				if err := a.service.GuardReservation(ctx, id); err != nil {
					return err
				}
				localRef := artifactsapi.CleanupExclusion{ID: reservation.ID, Owner: scope.Self, Repository: scope.Repository}
				for _, pin := range reservation.Evidence.Pins {
					if pin.Member == scope.Self {
						localRef.Incarnation, localRef.Generation = pin.Incarnation, pin.Generation
						localRef.Versions = append(localRef.Versions, artifactsapi.CleanupVersionIdentity{
							DownloadVersion: artifactsapi.DownloadVersion{Package: pin.Package, Version: pin.Version}, ID: pin.ID,
						})
					}
				}
				bound := artifactsapi.WithCleanupExclusion(ctx, localRef)
				if _, err := a.artifacts.Items().Delete(bound, a.key, false, "1.0.0.tgz"); err != nil {
					return err
				}
				if _, err := a.store.Outbox().
					Append(ctx, outbox.OutboxEntry{Origin: scope.Self, Stream: model.RepositoryStream(a.key), EventType: "item.delete", Payload: []byte(`{"path":"1.0.0.tgz"}`), Created: time.Now()}); err != nil {
					return err
				}
				if err := a.service.Commit(ctx, id); err != nil {
					return err
				}
				if scenario == "rollback_keeps_prepared" {
					return rollback
				}
				return nil
			})
			if scenario == "rollback_keeps_prepared" {
				storekit.WantErr(t, err, rollback)
				intent := storekit.Get[model.CleanupIntent](t)(a.store.CleanupIntents().Read(ctx, id))
				if intent.State != model.CleanupPrepared {
					t.Fatal("rolled back deletion published terminal intent", intent.State)
				}
				_, err = a.artifacts.Items().GetByID(ctx, a.firstItem)
				storekit.Must(t, err)
				finishAbort()
				return
			}
			storekit.Must(t, err)
			intent := storekit.Get[model.CleanupIntent](t)(a.store.CleanupIntents().Read(ctx, id))
			if intent.State != model.CleanupCommitted || intent.Watermark <= intent.BeforeWatermark {
				t.Fatal("deletion did not commit its own final frontier")
			}
			// A replacement worker cannot undo the committed effect, and even a signed
			// outcome cannot release the remote barrier while replication is behind.
			storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error { return a.service.Abort(ctx, id) }))
			if err = a.service.Finish(ctx, id); err == nil {
				t.Fatal("member reopened before tombstone application")
			}
			_, err = b.service.Exclusions.Admit(ctx, b.key, b.firstItem)
			storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
			storekit.Must(t, b.store.Tx(ctx, func(ctx context.Context) error {
				if _, err := b.artifacts.Items().Delete(artifactevents.WithRemote(ctx), b.key, false, "1.0.0.tgz"); err != nil {
					return err
				}
				return b.store.Outbox().SetApplied(ctx, outbox.AppliedMarker{Origin: scope.Self, Stream: model.RepositoryStream(a.key), LastSeq: intent.Watermark})
			}))
			storekit.Must(t, a.service.Finish(ctx, id))
			item, err := b.artifacts.Items().Get(ctx, b.key, false, "1.0.1.tgz")
			storekit.Must(t, err)
			admitted := storekit.Get[artifactsapi.CleanupAdmission](t)(b.service.Exclusions.Admit(ctx, b.key, item.ID))
			storekit.Must(t, b.service.Exclusions.Complete(ctx, admitted))
		})
	}
}

// Keep a compile-time assertion on the artifact side of the SQL composition.
var _ artport.Store = (*appsql.Store)(nil)
