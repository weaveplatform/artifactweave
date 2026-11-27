package retention_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// This native owner composes two real guarded removals into one native effect.
// Its generated companion was never a selected index file. The executor must
// acknowledge actual returned effects, and roll back both on record failure.
type cleanupRealCompanionRemoval struct {
	artifactsapi.ItemRemoval
	tx.Runner
	selected  int64
	companion artifactsapi.IndexFile
}

func (r cleanupRealCompanionRemoval) Remove(
	ctx context.Context,
	request artifactsapi.RemovalRequest,
	validate func(context.Context) error,
	record func(context.Context, artifactsapi.RemovalResult) error,
) (artifactsapi.RemovalResult, error) {
	if request.Item.ID != r.selected {
		return r.ItemRemoval.Remove(ctx, request, validate, record)
	}
	var combined artifactsapi.RemovalResult
	err := r.Tx(ctx, func(ctx context.Context) error {
		var err error
		noop := func(context.Context, artifactsapi.RemovalResult) error { return nil }
		combined, err = r.ItemRemoval.Remove(ctx, request, validate, noop)
		if err != nil {
			return err
		}
		request.Item = r.companion
		companion, err := r.ItemRemoval.Remove(ctx, request, func(context.Context) error { return nil }, noop)
		if err != nil {
			return err
		}
		combined.Items = append(combined.Items, companion.Items...)
		combined.Files += companion.Files
		combined.Bytes += companion.Bytes
		combined.VersionMutationRevision = companion.VersionMutationRevision
		return record(ctx, combined)
	})
	if err != nil {
		return artifactsapi.RemovalResult{}, err
	}
	return combined, nil
}

func TestAccCleanupUnselectedNativeCompanionAcknowledgement(t *testing.T) {
	for _, fault := range []bool{false, true} {
		name := "committed"
		if fault {
			name = "record_write_rollback"
		}
		t.Run(name, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			const path = "package/native-metadata.index"
			reply := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/"+path, support.Basic("admin", "pw"), "real-native-index")
			if reply.Code != http.StatusCreated {
				t.Fatal("companion fixture upload status", reply.Code)
			}
			companion := storekit.Get[artmodel.Item](t)(f.db.Items().Get(f.ctx, "execution", false, path))
			run, item := f.selected(t)
			progress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			_, err := f.db.Cleanup().Entry(f.ctx, run.ID, model.CleanupItemEntry, companion.ID)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			snapshot := artifactsapi.IndexFile{
				Version:            progress.Version,
				ID:                 companion.ID,
				Path:               companion.FullPath(),
				Size:               companion.Size,
				Actual:             companion.Actual,
				Created:            companion.Created,
				Modified:           companion.Modified,
				Updated:            companion.Updated,
				PropertiesModified: companion.PropertiesModified,
			}
			f.executor.Removal = cleanupRealCompanionRemoval{f.executor.Removal, f.store, f.ids[0], snapshot}
			hit := false
			if fault {
				wrapped := cleanupExecutionBoundaryStore{
					CleanupExecutionStore: f.store,
					cleanup: storetest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, method string) error {
						if method == "CreateEntry" {
							hit = true
							return errExecutorCoordination
						}
						return nil
					}},
				}
				failed := f.executor
				failed.Store = wrapped
				failed.Policies.Store = wrapped
				storekit.WantErr(t, failed.Execute(f.ctx, item), errExecutorCoordination)
				if !hit {
					t.Fatal("companion acknowledgement fault was not reached")
				}
				f.preserved(t)
				_, err = f.db.Items().GetByID(f.ctx, companion.ID)
				storekit.Must(t, err)
				_, err = f.db.Cleanup().Entry(f.ctx, run.ID, model.CleanupItemEntry, companion.ID)
				storekit.WantErr(t, err, kernel.ErrNotFound)
				storekit.Same(t, "native compound rollback retains exact continuation", progress, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
				trash := storekit.Get[[]artmodel.TrashEntry](t)(f.db.Trash().Find(f.ctx, "execution", path))
				if len(trash) != 0 {
					t.Fatal("rolled-back companion leaked trash")
				}
			}
			storekit.Must(t, f.executor.Execute(f.ctx, item))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
			if finished.Status != retapi.RunCompleted || finished.TotalItems != 2 || finished.CleanedItems != 3 {
				t.Fatal("native companion accounting lost actual effects", finished.Status, finished.TotalItems, finished.CleanedItems)
			}
			entry := storekit.Get[model.CleanupEntry](t)(f.db.Cleanup().Entry(f.ctx, run.ID, model.CleanupItemEntry, companion.ID))
			if entry.State != model.CleanupDeleted || entry.Reason != "companion_removed" || entry.Removed == nil || entry.Removed.ID != companion.ID {
				t.Fatal("companion outcome lost immutable native identity", entry.State, entry.Reason)
			}
			_, err = f.db.Items().GetByID(f.ctx, companion.ID)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			storekit.Must(t, f.executor.Execute(f.ctx, item))
			storekit.Same(t, "terminal replay never double counts companion", finished, storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID)))
		})
	}
}
