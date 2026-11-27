package federation_test

import (
	"context"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// These regressions use only the v1 owner API, so the identical tests can be
// executed against the preserved reviewed counterexample and the correction.
func TestAccCleanupCoordinationReviewRegressions(t *testing.T) {
	for _, scenario := range []string{"remote_companion_reader", "lost_release_reply_then_recreate", "unavailable_member_cannot_strand_healthy_member"} {
		t.Run(scenario, func(t *testing.T) {
			a := signedCleanupNodeOver(t, "coordinator", newWorldWithKey(t, "coordinator", 0, 2), nil)
			b := signedCleanupNodeOver(t, "participant", newWorldWithKey(t, "participant", 0, 2), nil)
			nodes := []*signedCleanupNode{a, b}
			var c *signedCleanupNode
			if scenario == "unavailable_member_cannot_strand_healthy_member" {
				c = signedCleanupNodeOver(t, "third", newWorldWithKey(t, "third", 0, 2), nil)
				nodes = append(nodes, c)
			}
			connectCleanupMesh(t, nodes...)
			ctx := t.Context()
			scope := storekit.Get[api.CleanupScope](t)(a.service.Scope(ctx, a.key))
			proof := storekit.Get[api.CleanupEvidence](t)(a.service.Collect(ctx, scope, a.versions[:1]))
			var id string
			storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error {
				var err error
				id, err = a.service.Reserve(ctx, proof, 1, a.firstItem)
				return err
			}))
			if scenario == "remote_companion_reader" {
				companion := storekit.Get[artmodel.Item](t)(b.artifacts.Items().Get(ctx, b.key, false, "1.0.1.tgz"))
				reader := storekit.Get[artifactsapi.CleanupAdmission](t)(b.service.Exclusions.Admit(ctx, b.key, companion.ID))
				_, err := a.service.Prepare(ctx, id)
				if err == nil {
					t.Fatal("remote other-version companion reader survived preparation")
				}
				storekit.Must(t, b.service.Exclusions.Complete(ctx, reader))
				return
			}
			storekit.Get[api.CleanupReservation](t)(a.service.Prepare(ctx, id))
			storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error { return a.service.Abort(ctx, id) }))
			remote := a.service.Remote
			fault := &releaseFaultRemote{
				Remote:       remote,
				coordinated:  remote.(cleanup.CoordinationRemote),
				offline:      map[string]bool{},
				loseReply:    map[string]bool{},
				releaseCalls: map[string]int{},
			}
			a.service.Remote = fault
			bID := storekit.Get[string](t)(b.store.Instance().ID(ctx))
			if c != nil {
				cID := storekit.Get[string](t)(c.store.Instance().ID(ctx))
				if bID < cID {
					b, c = c, b
					bID, cID = cID, bID
				}
				fault.offline[cID] = true
				if err := a.service.Finish(ctx, id); err == nil {
					t.Fatal("unavailable participant falsely acknowledged")
				}
				if fault.releaseCalls[bID] != 1 {
					t.Fatal("earlier unavailable member stranded a healthy participant")
				}
				return
			}
			fault.loseReply[bID] = true
			if err := a.service.Finish(ctx, id); err == nil {
				t.Fatal("lost successful release reply falsely acknowledged")
			}
			// The peer really released; a repository replacement is now legitimate and
			// must not turn its exact terminal acknowledgement into a lifetime conflict.
			cfg := storekit.Get[repo.Config](t)(b.artifacts.Repos().Get(ctx, b.key))
			storekit.Must(t, b.artifacts.Repos().Delete(ctx, b.key))
			storekit.Must(t, b.artifacts.Repos().Create(ctx, cfg))
			if err := a.service.Finish(ctx, id); err != nil {
				t.Fatalf("lost successful release could not recover after recreation: %v", err)
			}
			intent := storekit.Get[model.CleanupIntent](t)(a.store.CleanupIntents().Read(ctx, id))
			if !intent.Released {
				t.Fatal("recovered terminal operation did not finish")
			}
		})
	}
}
