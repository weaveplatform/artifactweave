package federation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type cleanupPublicationWaitIntents struct {
	port.CleanupIntentStore
	reads int
	wait  func()
}

func (s *cleanupPublicationWaitIntents) Read(ctx context.Context, id string) (model.CleanupIntent, error) {
	intent, err := s.CleanupIntentStore.Read(ctx, id)
	s.reads++
	if err == nil && s.reads == 2 {
		s.wait()
	}
	return intent, err
}

type cleanupPublicationWaitStore struct {
	cleanupWorldStore
	intents port.CleanupIntentStore
}

func (s cleanupPublicationWaitStore) CleanupIntents() port.CleanupIntentStore { return s.intents }

// A healed member's old negative-cache expiry is not another observation
// failure. A short publication wait must preserve a fresh post-failure proof;
// the real durable barriers still exclude new readers throughout publication.
func TestAccCleanupHealedFailureEpochSurvivesShortPublicationWait(t *testing.T) {
	w := newWorld(t, 1, 1)
	ctx := t.Context()
	base := time.Now()
	var elapsed atomic.Int64
	w.service.Now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(ctx, "packages"))
	healthy := w.service.Remote
	w.service.Remote = remoteFunc(func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error) {
		return cleanup.Answer{}, errCoordinationPersistence
	})
	_, err := w.service.Collect(ctx, scope, w.versions)
	storekit.WantErr(t, err, cleanup.ErrUnavailable)
	elapsed.Store(int64(cleanup.Window - 100*time.Millisecond))
	w.service.Remote = coordinationOutcomeRemote{healthy, func(_ context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
		state := "prepared"
		if r.Action == "release" {
			state = string(model.CleanupAborted)
		}
		return cleanup.CoordinationAnswer{State: state, Exclusion: r.Exclusion}, nil
	}}
	evidence := w.collect(t)
	if evidence.Scope.Members[0].LocalFailure == 0 {
		t.Fatal("fixture did not retain the real failed observation epoch")
	}
	var id string
	storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
		var err error
		id, err = w.service.Reserve(ctx, evidence, 7, w.firstItem)
		return err
	}))
	waiting := &cleanupPublicationWaitIntents{CleanupIntentStore: w.store.CleanupIntents(), wait: func() { elapsed.Store(int64(cleanup.Window + 100*time.Millisecond)) }}
	w.service.Store = cleanupPublicationWaitStore{w.store, waiting}
	reservation, err := w.service.Prepare(ctx, id)
	if err != nil {
		t.Fatalf("fresh healthy proof refused across old failure expiry after only200ms publication wait: %v", err)
	}
	if reservation.ID != id {
		t.Fatal("prepared a different operation")
	}
	_, err = w.service.Exclusions.Admit(ctx, "packages", w.firstItem)
	storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
	storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
	storekit.Must(t, w.service.Finish(ctx, id))
	pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
	storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
}

// Refreshing an active negative epoch cannot make an actual later failure or
// a genuinely expired proof current again. Neither refusal closes admission.
func TestAccCleanupHealedEpochKeepsFailureAndExpiryFences(t *testing.T) {
	for _, mode := range []string{"new_failure", "expired_evidence"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, 1, 1)
			ctx := t.Context()
			base := time.Now()
			var elapsed atomic.Int64
			w.service.Now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
			failed := remoteFunc(func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error) {
				return cleanup.Answer{}, errCoordinationPersistence
			})
			healthy := w.service.Remote
			scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(ctx, "packages"))
			w.service.Remote = failed
			_, err := w.service.Collect(ctx, scope, w.versions)
			storekit.WantErr(t, err, cleanup.ErrUnavailable)
			elapsed.Store(int64(cleanup.Window - 100*time.Millisecond))
			w.service.Remote = healthy
			evidence := w.collect(t)
			if mode == "new_failure" {
				w.service.Remote = failed
				_, err = w.service.Collect(ctx, evidence.Scope, w.versions)
				storekit.WantErr(t, err, cleanup.ErrUnavailable)
				w.service.Remote = healthy
			} else {
				elapsed.Add(int64(cleanup.Window))
			}
			storekit.WantErr(t, guard(w, ctx, evidence), kernel.ErrConflict)
			_, err = w.artifacts.Items().GetByID(ctx, w.firstItem)
			storekit.Must(t, err)
			pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
			storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
		})
	}
}

// A caller can capture its clock before waiting for another thread's failure
// lookup. Its delayed older timestamp must not shorten a newer scope's window.
func TestAccCleanupHealedEpochOlderLookupCannotBackdateCurrentScope(t *testing.T) {
	w := newWorld(t, 1, 1)
	ctx := t.Context()
	base := time.Now()
	var elapsed atomic.Int64
	w.service.Now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	healthy := w.service.Remote
	scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(ctx, "packages"))
	w.service.Remote = remoteFunc(func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error) {
		return cleanup.Answer{}, errCoordinationPersistence
	})
	_, err := w.service.Collect(ctx, scope, w.versions)
	storekit.WantErr(t, err, cleanup.ErrUnavailable)
	elapsed.Store(int64(4900 * time.Millisecond))
	w.service.Remote = healthy
	current := w.collect(t)
	elapsed.Store(int64(4800 * time.Millisecond))
	older := storekit.Get[api.CleanupScope](t)(w.service.Scope(ctx, "packages"))
	if older.Members[0].LocalFailure != current.Scope.Members[0].LocalFailure {
		t.Fatal("older lookup changed failure generation")
	}
	elapsed.Store(int64(9850 * time.Millisecond))
	storekit.Must(t, w.service.Fresh(ctx, current))
	elapsed.Store(int64(9900 * time.Millisecond))
	storekit.WantErr(t, w.service.Fresh(ctx, current), kernel.ErrConflict)
}
