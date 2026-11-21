package retentionhttp

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

// DownloadCleanupPolicyRunReport streams saved item outcomes as the authored
// octet-stream CSV. Summary and first-page failures precede the HTTP status.
func (s PackagePoliciesAPI) DownloadCleanupPolicyRunReport(
	ctx context.Context,
	req artgen.DownloadCleanupPolicyRunReportRequestObject,
) (artgen.DownloadCleanupPolicyRunReportResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	report, err := svc.OpenItemReport(ctx, req.Params.PolicyKey, int64(req.Params.RunId))
	if err != nil {
		return nil, reportError(ctx, err)
	}
	return itemReportResponse{ctx: ctx, service: svc, report: report}, nil
}

type itemReportResponse struct {
	ctx     context.Context
	service cleanup.Service
	report  cleanup.ItemReport
}

func (r itemReportResponse) abort(err error) {
	httpkit.NoteCause(r.ctx, fmt.Errorf("cleanup CSV download aborted: %w", err))
	panic(http.ErrAbortHandler)
}

func (r itemReportResponse) VisitDownloadCleanupPolicyRunReportResponse(w http.ResponseWriter) error {
	if err := r.ctx.Err(); err != nil {
		r.abort(err)
	}
	run := r.report.Summary.Snapshot.Run
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cleanup-%d.csv"`, run.ID))
	w.WriteHeader(http.StatusOK)
	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write([]string{"repository", "path", "size", "sha256", "outcome", "reason"}); err != nil {
		return nil
	}
	page, written := r.report.First, int64(0)
	for {
		for _, item := range page.Items {
			if err := r.ctx.Err(); err != nil {
				r.abort(err)
			}
			if written >= r.report.Summary.Totals.Items {
				r.abort(fmt.Errorf("cleanup CSV exceeds saved item count: %w", kernel.ErrStore))
			}
			if err := csvWriter.Write([]string{item.Repository, item.Path, strconv.FormatInt(item.Size, 10), item.SHA256, string(item.Outcome), item.Reason}); err != nil {
				return nil
			}
			written++
		}
		csvWriter.Flush()
		if csvWriter.Error() != nil {
			return nil
		}
		if err := r.ctx.Err(); err != nil {
			r.abort(err)
		}
		if page.Next == "" {
			break
		}
		var err error
		page, err = r.service.ReadItemReport(r.ctx, run.PolicyKey, run.ID, page.Next)
		if err != nil {
			r.abort(err)
		}
	}
	if written != r.report.Summary.Totals.Items {
		r.abort(fmt.Errorf("cleanup CSV ended before saved item count: %w", kernel.ErrStore))
	}
	if err := r.service.CompleteItemReport(r.ctx, run.PolicyKey, run.ID); err != nil {
		r.abort(err)
	}
	// Generated visitors interpret returned errors as a second HTTP response.
	// Client write failures end quietly; post-status read failures abort above.
	return nil
}
