package retentionhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func reportError(ctx context.Context, err error) error {
	if errors.Is(err, kernel.ErrNotFound) {
		return apierror.New(http.StatusNotFound, "Cleanup run not found")
	}
	if errors.Is(err, kernel.ErrConflict) {
		return apierror.New(http.StatusForbidden, "Forbidden")
	}
	return policyError(ctx, err)
}

// GetRunByTriggerId returns exact, currently authorized package execution facts.
func (s PackagePoliciesAPI) GetRunByTriggerId(ctx context.Context, req artgen.GetRunByTriggerIdRequestObject) (artgen.GetRunByTriggerIdResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	report, err := svc.Report(ctx, req.PolicyKey, int64(req.TriggerId))
	if err != nil {
		return nil, reportError(ctx, err)
	}
	out, err := runWire(report.Snapshot, report.Totals.DeletedContentBytes)
	if err != nil {
		return nil, reportError(ctx, err)
	}
	return artgen.GetRunByTriggerId200JSONResponse(out), nil
}

// ViewCleanupPolicyRunSummaryReport uses the immutable rule and actual outcomes.
func (s PackagePoliciesAPI) ViewCleanupPolicyRunSummaryReport(
	ctx context.Context,
	req artgen.ViewCleanupPolicyRunSummaryReportRequestObject,
) (artgen.ViewCleanupPolicyRunSummaryReportResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	report, err := svc.Report(ctx, req.Params.PolicyKey, int64(req.Params.TriggerId))
	if err != nil {
		return nil, reportError(ctx, err)
	}
	out, err := summaryWire(report)
	if err != nil {
		return nil, reportError(ctx, err)
	}
	return artgen.ViewCleanupPolicyRunSummaryReport200JSONResponse(out), nil
}

// GetCleanupPolicyRunsView applies current scope before filtering and paging.
func (s PackagePoliciesAPI) GetCleanupPolicyRunsView(ctx context.Context, req artgen.GetCleanupPolicyRunsViewRequestObject) (artgen.GetCleanupPolicyRunsViewResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	p := req.Params
	q := cleanup.RunViewQuery{
		ProjectKey:   httpkit.Deref(p.ProjectKey),
		PolicyKey:    httpkit.Deref(p.PolicyKey),
		Projects:     splitReportValues(httpkit.Deref(p.ProjectKeyList)),
		FromMillis:   p.From,
		ToMillis:     p.To,
		Page:         httpkit.Deref(p.Page),
		PageSize:     httpkit.Deref(p.PageSize),
		SortBy:       port.RunOrder(httpkit.Deref(p.SortBy)),
		SortOrder:    string(httpkit.Deref(p.SortOrder)),
		ExcludeBlank: httpkit.Deref(p.ExcludeBlankExecutions) == "true",
	}
	// Preserve an explicitly invalid zero instead of turning it into omission.
	if p.Page != nil && *p.Page == 0 {
		q.Page = -1
	}
	if p.PageSize != nil && *p.PageSize == 0 {
		q.PageSize = -1
	}
	for _, v := range splitReportValues(httpkit.Deref(p.ItemTypes)) {
		q.ItemTypes = append(q.ItemTypes, model.CleanupItemType(v))
	}
	for _, v := range splitReportValues(httpkit.Deref(p.Status)) {
		q.Statuses = append(q.Statuses, retapi.RunStatus(v))
	}
	page, err := svc.Runs(ctx, q)
	if err != nil {
		return nil, reportError(ctx, err)
	}
	rows := make([]artgen.CleanupPolicyRunViewEntry, 0, len(page.Runs))
	for _, r := range page.Runs {
		row, err := runWire(r, r.CleanedContentBytes)
		if err != nil {
			return nil, reportError(ctx, err)
		}
		rows = append(rows, row)
	}
	return artgen.GetCleanupPolicyRunsView200JSONResponse{
		RunViews:   &rows,
		Pagination: &artgen.CleanupPagination{CurrentPage: &page.CurrentPage, LastPage: &page.LastPage, PageSize: &page.PageSize, Total: &page.Total},
	}, nil
}

// GetCleanupPoliciesView exposes configuration and genuine scoped last runs.
func (s PackagePoliciesAPI) GetCleanupPoliciesView(ctx context.Context, req artgen.GetCleanupPoliciesViewRequestObject) (artgen.GetCleanupPoliciesViewResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	q := cleanup.PolicyViewQuery{ProjectKey: httpkit.Deref(req.Params.ProjectKey), PolicyKey: httpkit.Deref(req.Params.PolicyKey)}
	for _, v := range req.Params.ItemTypes {
		for _, kind := range strings.Split(string(v), ",") {
			q.ItemTypes = append(q.ItemTypes, model.CleanupItemType(strings.TrimSpace(kind)))
		}
	}
	views, err := svc.PolicyViews(ctx, q)
	if err != nil {
		return nil, reportError(ctx, err)
	}
	rows := make([]artgen.CleanupPolicyEntry, 0, len(views))
	for _, view := range views {
		row, err := policyViewWire(view)
		if err != nil {
			return nil, reportError(ctx, err)
		}
		rows = append(rows, row)
	}
	return artgen.GetCleanupPoliciesView200JSONResponse{Policies: &rows}, nil
}

func splitReportValues(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}
