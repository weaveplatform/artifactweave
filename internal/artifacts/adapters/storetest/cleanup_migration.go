package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunCleanupMigrationAdmissions checks that a local hosted read remains pinned
// through migration. Each store owns its transactions; no response I/O or sleep
// is needed to establish admission-before-migration ordering.
func RunCleanupMigrationAdmissions(t *testing.T, newStore Factory) {
	t.Helper()
	for _, unindexed := range []bool{false, true} {
		name := "indexed local response"
		if unindexed {
			name = "unindexed local response"
		}
		t.Run(name, func(t *testing.T) {
			f := newExclusionFixture(t, newStore)
			ctx := t.Context()
			configuration := f.config
			configuration.Class = repo.Local
			storekit.Must(t, f.store.Repos().Update(ctx, configuration))
			it := f.items[0]
			if unindexed {
				it = storekit.Get[model.Item](t)(f.store.Items().Put(ctx, model.Item{RepoKey: configuration.Key, Name: "unindexed", Size: 1}))
			}
			token := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, configuration.Key, it.ID))
			configuration.Class = repo.Federated
			storekit.Must(t, f.store.Repos().Update(ctx, configuration))
			current := storekit.Get[api.CleanupRepository](t)(f.metadata.Repository(ctx, configuration.Key))
			ref := f.ref
			ref.Incarnation, ref.Generation = current.Incarnation, current.Generation
			wantExcluded(t, f.exclusions.Prepare(ctx, ref))
			if token.ID == "" {
				t.Fatal("local response has no durable migration admission")
			}
			stats := storekit.Get[model.ItemStats](t)(f.store.Items().Stats(ctx, it.ID))
			if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
				t.Fatal("private migration admission changed public statistics")
			}
			storekit.Must(t, f.exclusions.Complete(ctx, token))
			storekit.Must(t, f.exclusions.Complete(ctx, token))
			storekit.Must(t, f.exclusions.Prepare(ctx, ref))
		})
	}
}
