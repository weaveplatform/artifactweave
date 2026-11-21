package retentionhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

// RunPackageCleanupPolicy acknowledges only an atomically queued durable run.
func (s PackagePoliciesAPI) RunPackageCleanupPolicy(ctx context.Context, req artgen.RunPackageCleanupPolicyRequestObject) (artgen.RunPackageCleanupPolicyResponseObject, error) {
	service, err := s.service()
	if err != nil {
		return nil, err
	}
	run, err := service.ManualRun(ctx, req.PolicyKey, httpkit.Deref(req.Params.DryRun))
	if err != nil {
		return nil, policyError(ctx, err)
	}
	id, err := cleanup.PublicRunID(run.TriggerID)
	if err != nil {
		return nil, policyError(ctx, err)
	}
	idempotency.Report(ctx, run.ID)
	return artgen.RunPackageCleanupPolicy202JSONResponse{TriggerId: &id}, nil
}

// StopRunningCleanupPolicy requests a cooperative stop. Terminal settlement is
// the executor's publication after fencing further destructive work.
func (s PackagePoliciesAPI) StopRunningCleanupPolicy(ctx context.Context, req artgen.StopRunningCleanupPolicyRequestObject) (artgen.StopRunningCleanupPolicyResponseObject, error) {
	service, err := s.service()
	if err != nil {
		return nil, err
	}
	if req.RunId <= 0 || req.Body == nil || string(req.Body.Action) != "stop" {
		return nil, httpkit.BadRequestf("a positive runId and action stop are required")
	}
	if err := service.Stop(ctx, int64(req.RunId)); err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			return nil, apierror.New(http.StatusNotFound, "Cleanup run not found")
		}
		return nil, policyError(ctx, err)
	}
	return artgen.StopRunningCleanupPolicy202JSONResponse("Cleanup run stop requested"), nil
}

// StopAllRunningCleanupPolicies retains the authored and documented 202;
// acceptance requests stops without claiming that workers have already stopped.
func (s PackagePoliciesAPI) StopAllRunningCleanupPolicies(
	ctx context.Context,
	_ artgen.StopAllRunningCleanupPoliciesRequestObject,
) (artgen.StopAllRunningCleanupPoliciesResponseObject, error) {
	service, err := s.service()
	if err != nil {
		return nil, err
	}
	if err := service.StopAll(ctx); err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.StopAllRunningCleanupPolicies202Response{}, nil
}
