package cleanup

import (
	"context"
	"fmt"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

const itemReportPageSize = 100

// ReportItem describes a retained intent or acknowledged effect. It contains
// no current artifact lookup, original credential, properties or private IDs.
type ReportItem struct {
	Repository, Path, SHA256 string
	Size                     int64
	Outcome                  model.CleanupEntryState
	Reason                   string
}

// ItemReportPage bounds one download read. The continuation is private to the
// response writer and carries no authority; each page refreshes its caller.
type ItemReportPage struct {
	Items []ReportItem
	Next  string
}

// ItemReport prepares the summary and first page before HTTP commits a status.
// Terminal outcomes are immutable; a short transaction ends before client I/O.
type ItemReport struct {
	Summary RunReport
	First   ItemReportPage
}

// OpenItemReport admits a nonempty terminal run, including dry and failed runs.
// Empty or still-changing runs have no downloadable report.
func (s Service) OpenItemReport(ctx context.Context, policyKey string, trigger int64) (ItemReport, error) {
	var out ItemReport
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		report, err := s.readReport(ctx, policyKey, trigger, authority)
		if err != nil {
			return err
		}
		if !report.Snapshot.Run.Status.Terminal() || report.Totals.Items == 0 {
			return kernel.ErrNotFound
		}
		first, err := s.itemReportPage(ctx, trigger, "")
		if err != nil {
			return err
		}
		if len(first.Items) == 0 || int64(len(first.Items)) > report.Totals.Items {
			return fmt.Errorf("cleanup report has inconsistent first page: %w", kernel.ErrStore)
		}
		out = ItemReport{Summary: report, First: first}
		return nil
	})
	if err != nil {
		return ItemReport{}, err
	}
	return out, nil
}

// ReadItemReport supplies one bounded page under current administration and
// original execution ownership. No caller guard survives the returned page.
func (s Service) ReadItemReport(ctx context.Context, policyKey string, trigger int64, after string) (ItemReportPage, error) {
	var out ItemReportPage
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		run, err := s.readRun(ctx, policyKey, trigger, authority)
		if err != nil {
			return err
		}
		if !run.Run.Status.Terminal() {
			return kernel.ErrNotFound
		}
		out, err = s.itemReportPage(ctx, trigger, after)
		return err
	})
	if err != nil {
		return ItemReportPage{}, err
	}
	return out, nil
}

// CompleteItemReport marks only a completed local response, after refreshing
// authority once more. A canceled request never records a successful download.
func (s Service) CompleteItemReport(ctx context.Context, policyKey string, trigger int64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cleanup report response canceled: %w", err)
	}
	return s.run(ctx, func(ctx context.Context, authority Policy) error {
		if _, err := s.readRun(ctx, policyKey, trigger, authority); err != nil {
			return err
		}
		if err := s.Store.Cleanup().MarkReportDownloaded(ctx, trigger); err != nil {
			return fmt.Errorf("record cleanup report download: %w", err)
		}
		return nil
	})
}

func (s Service) itemReportPage(ctx context.Context, run int64, after string) (ItemReportPage, error) {
	entries, next, err := s.Store.Cleanup().Entries(ctx, port.CleanupEntryQuery{RunID: run, Kind: model.CleanupItemEntry}, kernel.Page{Limit: itemReportPageSize, After: after})
	if err != nil {
		return ItemReportPage{}, fmt.Errorf("read cleanup report page: %w", err)
	}
	if len(entries) > itemReportPageSize || next != "" && (next == after || len(entries) == 0) {
		return ItemReportPage{}, fmt.Errorf("cleanup report has invalid continuation: %w", kernel.ErrStore)
	}
	items := make([]ReportItem, 0, len(entries))
	versions := map[int64]artifactsapi.IndexVersion{}
	for _, entry := range entries {
		if entry.RunID != run || entry.Kind != model.CleanupItemEntry {
			return ItemReportPage{}, fmt.Errorf("cleanup report returned another run or entry kind: %w", kernel.ErrStore)
		}
		item, err := s.reportItem(ctx, entry, versions)
		if err != nil {
			return ItemReportPage{}, err
		}
		items = append(items, item)
	}
	return ItemReportPage{Items: items, Next: next}, nil
}

func (s Service) reportItem(ctx context.Context, e model.CleanupEntry, versions map[int64]artifactsapi.IndexVersion) (ReportItem, error) {
	if err := e.Validate(); err != nil {
		return ReportItem{}, fmt.Errorf("invalid cleanup report outcome: %w", kernel.ErrStore)
	}
	file := e.Snapshot
	if file.ID != e.EntityID || file.Version.ID != e.VersionID {
		// Native companions are acknowledged without an enumerated file
		// intent. Their immutable parent decision supplies repository identity.
		if e.State != model.CleanupDeleted || file.ID != 0 {
			return ReportItem{}, fmt.Errorf("cleanup report lost its intent: %w", kernel.ErrStore)
		}
		version, ok := versions[e.VersionID]
		if !ok {
			parent, err := s.Store.Cleanup().Entry(ctx, e.RunID, model.CleanupVersionEntry, e.VersionID)
			if err != nil {
				return ReportItem{}, fmt.Errorf("read cleanup companion parent: %w", err)
			}
			if parent.RunID != e.RunID || parent.Kind != model.CleanupVersionEntry || parent.EntityID != e.VersionID || parent.Snapshot.Version.ID != e.VersionID {
				return ReportItem{}, fmt.Errorf("cleanup companion parent mismatch: %w", kernel.ErrStore)
			}
			version = parent.Snapshot.Version
			versions[e.VersionID] = version
		}
		file.Version = version
	}
	out := ReportItem{Repository: file.Version.Package.Repository.Key, Path: file.Path, Size: file.Size, SHA256: file.Actual.SHA256, Outcome: e.State, Reason: e.Reason}
	if e.State == model.CleanupDeleted {
		out.Path, out.Size, out.SHA256 = e.Removed.Path, e.Removed.Size, e.Removed.SHA256
	}
	if out.Repository == "" || out.Path == "" || out.Size < 0 {
		return ReportItem{}, fmt.Errorf("cleanup report has incomplete item facts: %w", kernel.ErrStore)
	}
	return out, nil
}
