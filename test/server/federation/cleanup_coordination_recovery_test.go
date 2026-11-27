package federation_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func coordinatedRecoveryContract(t *testing.T, newWorld coordinationWorldFactory) {
	t.Helper()
	t.Run("remote_other_version_reader_and_unrelated_repository", func(t *testing.T) {
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
		companion := storekit.Get[artmodel.Item](t)(b.artifacts.Items().Get(ctx, b.key, false, "1.0.1.tgz"))
		reader := storekit.Get[artifactsapi.CleanupAdmission](t)(b.service.Exclusions.Admit(ctx, b.key, companion.ID))
		if _, err := a.service.Prepare(ctx, id); err == nil {
			t.Fatal("remote other-version companion reader survived prepared exclusion")
		}
		_, err := b.artifacts.Items().GetByID(ctx, companion.ID)
		storekit.Must(t, err)
		// The drain is confined to the affected repository, even while its reader is
		// unknown. Another hosted repository on this same member remains available.
		outside := *repo.Defaults(&repo.Config{Key: "outside", Class: repo.Federated, PackageType: repo.NPM})
		storekit.Must(t, b.artifacts.Repos().Create(ctx, outside))
		other := storekit.Get[artmodel.Item](t)(b.artifacts.Items().Put(ctx, artmodel.Item{RepoKey: outside.Key, Name: "independent.tgz", Size: 1}))
		independent := storekit.Get[artifactsapi.CleanupAdmission](t)(b.service.Exclusions.Admit(ctx, outside.Key, other.ID))
		storekit.Must(t, b.service.Exclusions.Complete(ctx, independent))
		storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error { return a.service.Abort(ctx, id) }))
		storekit.Must(t, a.service.Finish(ctx, id))
		storekit.Must(t, b.service.Exclusions.Complete(ctx, reader))
	})
	for _, change := range []string{"configuration", "recreate"} {
		t.Run("lost_release_reply_then_"+change, func(t *testing.T) {
			a := signedCleanupNodeOver(t, "coordinator", newWorld(t, "coordinator"), nil)
			b := signedCleanupNodeOver(t, "first", newWorld(t, "first"), nil)
			c := signedCleanupNodeOver(t, "second", newWorld(t, "second"), nil)
			connectCleanupMesh(t, a, b, c)
			ctx := t.Context()
			bID := storekit.Get[string](t)(b.store.Instance().ID(ctx))
			cID := storekit.Get[string](t)(c.store.Instance().ID(ctx))
			// Make the unavailable member earlier in deterministic order. A healthy
			// member must still release after that failure; random instance IDs cannot
			// turn this into an accidental success by ordering the failed one last.
			if bID < cID {
				b, c = c, b
				bID, cID = cID, bID
			}
			scope := storekit.Get[api.CleanupScope](t)(a.service.Scope(ctx, a.key))
			proof := storekit.Get[api.CleanupEvidence](t)(a.service.Collect(ctx, scope, a.versions[:1]))
			var id string
			storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error {
				var err error
				id, err = a.service.Reserve(ctx, proof, 1, a.firstItem)
				return err
			}))
			storekit.Get[api.CleanupReservation](t)(a.service.Prepare(ctx, id))
			storekit.Must(t, a.store.Tx(ctx, func(ctx context.Context) error { return a.service.Abort(ctx, id) }))
			remote := a.service.Remote
			fault := &releaseFaultRemote{
				Remote:       remote,
				coordinated:  remote.(cleanup.CoordinationRemote),
				offline:      map[string]bool{cID: true},
				loseReply:    map[string]bool{bID: true},
				releaseCalls: map[string]int{},
			}
			a.service.Remote = fault
			if err := a.service.Finish(ctx, id); err == nil {
				t.Fatal("lost reply/unavailable participant falsely completed settlement")
			}
			if fault.releaseCalls[bID] != 1 || fault.releaseCalls[cID] != 1 {
				t.Fatal("earlier unreachable member prevented independent release", fault.releaseCalls)
			}
			current := storekit.Get[model.CleanupIntent](t)(a.store.CleanupIntents().Read(ctx, id))
			if slices.Contains(current.ReleasedMembers, bID) || slices.Contains(current.ReleasedMembers, cID) {
				t.Fatal("unconfirmed release was acknowledged", current.ReleasedMembers)
			}
			_, err := c.service.Exclusions.Admit(ctx, c.key, c.firstItem)
			storekit.WantErr(t, err, artifactsapi.ErrCleanupExcluded)
			cfg := storekit.Get[repo.Config](t)(b.artifacts.Repos().Get(ctx, b.key))
			if change == "configuration" {
				cfg.Description = "changed after a lost terminal reply"
				storekit.Must(t, b.artifacts.Repos().Update(ctx, cfg))
			} else {
				members := storekit.Get[[]model.FederationMember](t)(b.store.Federation().Members(ctx, b.key))
				storekit.Must(t, b.artifacts.Repos().Delete(ctx, b.key))
				storekit.Must(t, b.artifacts.Repos().Create(ctx, cfg))
				storekit.Must(t, b.store.Federation().SetMembers(ctx, b.key, members))
				pkg := storekit.Get[artmodel.Package](t)(b.artifacts.Packages().Ensure(ctx, artmodel.Package{RepoKey: b.key, Type: repo.NPM, Name: "widget"}))
				v := storekit.Get[artmodel.PackageVersion](
					t,
				)(
					b.artifacts.Packages().PutVersion(ctx, artmodel.PackageVersion{PackageID: pkg.ID, Version: "1.0.0", Created: storekit.At(1)}),
				)
				item := storekit.Get[artmodel.Item](t)(b.artifacts.Items().Put(ctx, artmodel.Item{RepoKey: b.key, Name: "1.0.0.tgz", Size: 1, Created: storekit.At(1)}))
				storekit.Must(t, b.artifacts.Packages().LinkFile(ctx, artmodel.VersionFile{VersionID: v.ID, ItemID: item.ID}))
			}
			local := storekit.Get[artifactsapi.CleanupRepository](t)(b.service.Metadata.Repository(ctx, b.key))
			rows := storekit.Get[[]artifactsapi.VersionDownload](t)(b.service.Metadata.Downloads(ctx, local, []artifactsapi.DownloadVersion{{Package: "widget", Version: "1.0.0"}}))
			replacement := artifactsapi.CleanupExclusion{
				ID:          rand.Text(),
				Owner:       scope.Self,
				Repository:  b.key,
				Incarnation: local.Incarnation,
				Generation:  local.Generation,
				Versions:    []artifactsapi.CleanupVersionIdentity{{DownloadVersion: rows[0].DownloadVersion, ID: rows[0].ID}},
			}
			storekit.Must(t, b.service.Exclusions.Prepare(ctx, replacement))
			if err := a.service.Finish(ctx, id); err == nil {
				t.Fatal("still-unavailable participant was treated as released")
			}
			storekit.Must(t, b.service.Exclusions.Guard(ctx, replacement))
			current = storekit.Get[model.CleanupIntent](t)(a.store.CleanupIntents().Read(ctx, id))
			if !slices.Contains(current.ReleasedMembers, bID) {
				t.Fatal("terminal old binding could not acknowledge after repository change")
			}
			// Once known completed, an unavailable earlier member is no longer a reason
			// to recontact it or withhold another independently safe release.
			fault.offline[bID] = true
			delete(fault.offline, cID)
			before := fault.releaseCalls[bID]
			storekit.Must(t, a.service.Finish(ctx, id))
			if fault.releaseCalls[bID] != before {
				t.Fatal("durable acknowledgement was ignored")
			}
			current = storekit.Get[model.CleanupIntent](t)(a.store.CleanupIntents().Read(ctx, id))
			if !current.Released || current.State != model.CleanupAborted {
				t.Fatal("resumed terminal outcome did not settle", fmt.Sprint(current))
			}
			storekit.Must(t, b.service.Exclusions.Guard(ctx, replacement))
			storekit.Must(t, b.service.Exclusions.Release(ctx, replacement, func(context.Context) error { return nil }))
		})
	}
}
