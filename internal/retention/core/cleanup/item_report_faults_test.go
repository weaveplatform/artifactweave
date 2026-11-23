package cleanup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// corruptReportStore returns stored report rows after a caller-chosen change,
// standing in for a damaged or inconsistent store. The report must refuse the
// download rather than publish facts that disagree with the retained run.
type corruptReportStore struct {
	port.CleanupStore
	entries func([]model.CleanupEntry, string) ([]model.CleanupEntry, string, error)
	parent  func(model.CleanupEntry) (model.CleanupEntry, error)
	mark    error
}

func (s corruptReportStore) Entries(ctx context.Context, q port.CleanupEntryQuery, page kernel.Page) ([]model.CleanupEntry, string, error) {
	entries, next, err := s.CleanupStore.Entries(ctx, q, page)
	if err != nil || s.entries == nil {
		return entries, next, err //nolint:wrapcheck // transparent test double
	}
	return s.entries(entries, next)
}

func (s corruptReportStore) Entry(ctx context.Context, run int64, kind model.CleanupEntryKind, id int64) (model.CleanupEntry, error) {
	e, err := s.CleanupStore.Entry(ctx, run, kind, id)
	if err != nil || s.parent == nil {
		return e, err //nolint:wrapcheck // transparent test double
	}
	return s.parent(e)
}

func (s corruptReportStore) MarkReportDownloaded(ctx context.Context, run int64) error {
	if s.mark != nil {
		return s.mark
	}
	return s.CleanupStore.MarkReportDownloaded(ctx, run) //nolint:wrapcheck // transparent test double
}

// change edits one row of a page; rows are copied so the stored run is intact.
func change(i int, edit func(*model.CleanupEntry)) func([]model.CleanupEntry, string) ([]model.CleanupEntry, string, error) {
	return func(entries []model.CleanupEntry, next string) ([]model.CleanupEntry, string, error) {
		out := append([]model.CleanupEntry(nil), entries...)
		if out[i].Removed != nil {
			removed := *out[i].Removed
			out[i].Removed = &removed
		}
		edit(&out[i])
		return out, next, nil
	}
}

// completedReport retains a terminal run with one deleted file, one skipped
// file and one native companion acknowledged under its version decision.
func completedReport(t *testing.T) (cleanup.Service, string, model.CleanupRun) {
	t.Helper()
	s, db := world(t, "*")
	s.Work = db.WorkQueue()
	ctx := t.Context()
	p, err := s.Create(ctx, "checked-report", input("checked-report", "alpha"))
	must(t, err)
	must(t, s.Enable(ctx, p.Key, true))
	r, err := s.ManualRun(ctx, p.Key, false)
	must(t, err)
	if _, err := s.ReadItemReport(ctx, p.Key, r.ID, ""); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("an active run served a report page", err)
	}
	version := artifactsapi.IndexVersion{ID: 1, Package: artifactsapi.IndexPackage{Repository: artifactsapi.IndexRepository{Key: "alpha-npm"}}}
	hash := strings.Repeat("b", 64)
	must(t, db.Tx(ctx, func(ctx context.Context) error {
		for _, e := range []model.CleanupEntry{
			{RunID: r.ID, EntityID: 1, VersionID: 1, Revision: 1, Kind: model.CleanupVersionEntry, State: model.CleanupSelected, Snapshot: artifactsapi.IndexFile{Version: version}},
			{
				RunID: r.ID, EntityID: 1, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: model.CleanupDeleted,
				Snapshot: artifactsapi.IndexFile{ID: 1, Version: version, Path: "package/a.tgz", Size: 7},
				Removed:  &artifactsapi.RemovedItem{ID: 1, Path: "package/a.tgz", Size: 7, SHA256: hash}, Reason: "removed",
			},
			{
				RunID: r.ID, EntityID: 2, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: model.CleanupSkipped,
				Snapshot: artifactsapi.IndexFile{ID: 2, Version: version, Path: "package/b.tgz", Size: 3}, Reason: "kept",
			},
			{
				RunID: r.ID, EntityID: 3, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: model.CleanupDeleted,
				Removed: &artifactsapi.RemovedItem{ID: 3, Path: "native-companion", Size: 1, SHA256: strings.Repeat("c", 64)}, Reason: "companion_removed",
			},
		} {
			if _, err := db.Cleanup().CreateEntry(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}))
	r.Status, r.Ended, r.TotalItems, r.CleanedItems, r.CleanedSize = api.RunCompleted, s.Now(), 3, 2, 8
	must(t, db.Cleanup().UpdateRun(ctx, r))
	return s, p.Key, r
}

func TestUnitItemReportRefusesInconsistentRetainedRows(t *testing.T) {
	errRead := errors.New("injected cleanup report read failure")
	for _, tc := range []struct {
		name  string
		store func(port.CleanupStore) corruptReportStore
		want  error
	}{
		{"page read", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: func([]model.CleanupEntry, string) ([]model.CleanupEntry, string, error) {
				return nil, "", errRead
			}}
		}, errRead},
		{"continuation without rows", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: func([]model.CleanupEntry, string) ([]model.CleanupEntry, string, error) {
				return nil, "more", nil
			}}
		}, kernel.ErrStore},
		{"empty first page", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: func([]model.CleanupEntry, string) ([]model.CleanupEntry, string, error) {
				return nil, "", nil
			}}
		}, kernel.ErrStore},
		{"another run", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: change(0, func(e *model.CleanupEntry) { e.RunID += 1000 })}
		}, kernel.ErrStore},
		{"invalid outcome", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: change(0, func(e *model.CleanupEntry) { e.State = "unknown" })}
		}, kernel.ErrStore},
		{"skipped row lost its intent", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: change(1, func(e *model.CleanupEntry) { e.Snapshot.ID = 99 })}
		}, kernel.ErrStore},
		{"deleted row lost its effect path", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, entries: change(0, func(e *model.CleanupEntry) { e.Removed.Path = "" })}
		}, kernel.ErrStore},
		{"companion parent read", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, parent: func(model.CleanupEntry) (model.CleanupEntry, error) {
				return model.CleanupEntry{}, errRead
			}}
		}, errRead},
		{"companion parent of another version", func(c port.CleanupStore) corruptReportStore {
			return corruptReportStore{CleanupStore: c, parent: func(e model.CleanupEntry) (model.CleanupEntry, error) {
				e.EntityID++
				return e, nil
			}}
		}, kernel.ErrStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, key, r := completedReport(t)
			healthy := s
			got, err := healthy.OpenItemReport(t.Context(), key, r.ID)
			must(t, err)
			if len(got.First.Items) != 3 || got.First.Items[2].Repository != "alpha-npm" {
				t.Fatalf("fixture report is not complete: %+v", got.First.Items)
			}
			db := s.Store
			s.Store = cleanupBoundaryStore{Store: db, cleanup: tc.store(db.Cleanup())}
			report, err := s.OpenItemReport(t.Context(), key, r.ID)
			if !errors.Is(err, tc.want) || len(report.First.Items) != 0 {
				t.Fatalf("inconsistent report opened: items=%d err=%v", len(report.First.Items), err)
			}
			// A later page may be empty; only opening requires a first row.
			if tc.name == "empty first page" {
				return
			}
			page, err := s.ReadItemReport(t.Context(), key, r.ID, "")
			if !errors.Is(err, tc.want) || len(page.Items) != 0 {
				t.Fatalf("inconsistent report page served: items=%d err=%v", len(page.Items), err)
			}
		})
	}
}

func TestUnitItemReportDownloadMarkerFailureKeepsRunUnchanged(t *testing.T) {
	s, key, r := completedReport(t)
	db := s.Store
	errMark := errors.New("injected report marker failure")
	s.Store = cleanupBoundaryStore{Store: db, cleanup: corruptReportStore{CleanupStore: db.Cleanup(), mark: errMark}}
	if err := s.CompleteItemReport(t.Context(), key, r.ID); !errors.Is(err, errMark) {
		t.Fatalf("marker failure was hidden: %v", err)
	}
	stored, err := db.Cleanup().Run(t.Context(), r.ID)
	must(t, err)
	if stored.ReportDownloaded {
		t.Fatal("failed marker recorded a completed download")
	}
}
