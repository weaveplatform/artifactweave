package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

type observedReportStore struct {
	port.CleanupStore
	calls *int
}

func (s observedReportStore) Entries(ctx context.Context, q port.CleanupEntryQuery, page kernel.Page) ([]model.CleanupEntry, string, error) {
	(*s.calls)++
	if page.Limit != 100 || q.Kind != model.CleanupItemEntry {
		return nil, "", errCleanupBoundary
	}
	return s.CleanupStore.Entries(ctx, q, page)
}

func TestUnitItemReportPagesRetainEffectsAndRefreshAuthority(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "paged-report", input("paged-report", "alpha"))
	must(t, err)
	must(t, s.Enable(ctx, p.Key, true))
	r, err := s.ManualRun(ctx, p.Key, false)
	must(t, err)
	_, err = s.OpenItemReport(ctx, p.Key, r.ID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("active run acquired a downloadable report", err)
	}
	version := artifactsapi.IndexVersion{ID: 1, Package: artifactsapi.IndexPackage{Repository: artifactsapi.IndexRepository{Key: "alpha-npm"}}}
	hash := strings.Repeat("a", 64)
	must(t, db.Tx(ctx, func(ctx context.Context) error {
		if _, err := db.Cleanup().
			CreateEntry(ctx, model.CleanupEntry{RunID: r.ID, EntityID: 1, VersionID: 1, Revision: 1, Kind: model.CleanupVersionEntry, State: model.CleanupSelected, Snapshot: artifactsapi.IndexFile{Version: version}}); err != nil {
			return err
		}
		for id := int64(1); id <= 202; id++ {
			e := model.CleanupEntry{
				RunID: r.ID, EntityID: id, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: model.CleanupDeleted,
				Snapshot: artifactsapi.IndexFile{ID: id, Version: version, Path: "old-intent", Size: 999},
				Removed:  &artifactsapi.RemovedItem{ID: id, Path: fmt.Sprintf("package/item-%03d", id), Size: 5, SHA256: hash}, Reason: "removed",
			}
			if id == 202 {
				e.Snapshot = artifactsapi.IndexFile{}
				e.Removed.Path, e.Reason = "native-companion", "companion_removed"
			}
			if _, err := db.Cleanup().CreateEntry(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}))
	r.Status, r.Ended, r.TotalItems, r.CleanedItems, r.CleanedSize = api.RunCompleted, s.Now(), 202, 202, 1010
	must(t, db.Cleanup().UpdateRun(ctx, r))
	must(t, s.Delete(ctx, p.Key))
	allowed, calls := true, 0
	guarded := s
	guarded.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
		if err := db.Projects().Guard(ctx); err != nil {
			return nil, err
		}
		if !allowed {
			return policyAuthority("beta"), nil
		}
		return policyAuthority("alpha"), nil
	}
	guarded.Store = cleanupBoundaryStore{Store: db, cleanup: observedReportStore{CleanupStore: db.Cleanup(), calls: &calls}}
	file, err := guarded.OpenItemReport(ctx, p.Key, r.ID)
	must(t, err)
	if calls != 1 || len(file.First.Items) != 100 || file.First.Next == "" || file.Summary.Totals.DeletedContentBytes != 5 {
		t.Fatal("opening report collected unbounded rows or lost unique content")
	}
	if file.First.Items[0].Path != "package/item-001" || file.First.Items[0].Size != 5 {
		t.Fatal("acknowledged report substituted old selection facts")
	}
	allowed = false
	page, err := guarded.ReadItemReport(ctx, p.Key, r.ID, file.First.Next)
	if !errors.Is(err, kernel.ErrForbidden) || len(page.Items) != 0 || calls != 1 {
		t.Fatal("revoked caller read another page", err)
	}
	if err := guarded.CompleteItemReport(ctx, p.Key, r.ID); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("revoked caller marked a completed download", err)
	}
	allowed = true
	page, err = guarded.ReadItemReport(ctx, p.Key, r.ID, file.First.Next)
	must(t, err)
	if len(page.Items) != 100 || page.Next == "" {
		t.Fatal("middle report page lost its continuation")
	}
	page, err = guarded.ReadItemReport(ctx, p.Key, r.ID, page.Next)
	must(t, err)
	if len(page.Items) != 2 || page.Next != "" || page.Items[1].Repository != "alpha-npm" || page.Items[1].Path != "native-companion" {
		t.Fatal("final page lost native companion's immutable parent/effect")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := guarded.CompleteItemReport(canceled, p.Key, r.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled response marked downloaded", err)
	}
	stored, err := db.Cleanup().Run(ctx, r.ID)
	must(t, err)
	if stored.ReportDownloaded {
		t.Fatal("refused download changed report metadata")
	}
	must(t, guarded.CompleteItemReport(ctx, p.Key, r.ID))
	must(t, guarded.CompleteItemReport(ctx, p.Key, r.ID))
	stored, err = db.Cleanup().Run(ctx, r.ID)
	must(t, err)
	if !stored.ReportDownloaded || stored.CleanedItems != 202 || stored.CleanedSize != 1010 {
		t.Fatal("successful download lost its marker or changed execution facts")
	}
}

func TestUnitItemReportEmptyRunHasNoDownload(t *testing.T) {
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "empty-report", input("empty-report", "alpha"))
	must(t, err)
	r, err := s.ManualRun(ctx, p.Key, true)
	must(t, err)
	r.Status, r.Ended = api.RunCompleted, s.Now()
	must(t, db.Cleanup().UpdateRun(ctx, r))
	_, err = s.OpenItemReport(ctx, p.Key, r.ID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("empty run acquired a synthetic report", err)
	}
}
