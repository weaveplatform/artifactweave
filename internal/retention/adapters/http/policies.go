package retentionhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

// PackagePoliciesAPI implements package configuration, execution admission and
// cooperative stops and report views. Build, bundle and archive execution
// retain their phase owners.
type PackagePoliciesAPI struct{ Policies *cleanup.Service }

func (s PackagePoliciesAPI) service() (cleanup.Service, error) {
	if s.Policies == nil {
		return cleanup.Service{}, apierror.ErrNotImplemented
	}
	return *s.Policies, nil
}

func policyError(ctx context.Context, err error) error {
	var response *apierror.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &response):
		return err
	case errors.Is(err, kernel.ErrBadCredentials),
		errors.Is(err, kernel.ErrUnauthenticated),
		errors.Is(err, kernel.ErrForbidden),
		errors.Is(err, kernel.ErrPasswordExpired),
		errors.Is(err, kernel.ErrAPIRestricted):
		return err
	case errors.Is(err, kernel.ErrInvalid):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, kernel.ErrConflict):
		return apierror.New(http.StatusConflict, "Cleanup policy already exists")
	case errors.Is(err, kernel.ErrNotFound):
		return apierror.New(http.StatusNotFound, "Cleanup policy not found")
	default:
		httpkit.NoteCause(ctx, err)
		return apierror.New(http.StatusInternalServerError, "internal error")
	}
}

// CreatePackageCleanupPolicy returns the authored 201 with disabled state.
func (s PackagePoliciesAPI) CreatePackageCleanupPolicy(
	ctx context.Context,
	req artgen.CreatePackageCleanupPolicyRequestObject,
) (artgen.CreatePackageCleanupPolicyResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a package policy body is required")
	}
	p, err := svc.Create(ctx, req.PolicyKey, policyInput(*req.Body))
	if err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.CreatePackageCleanupPolicy201JSONResponse(policyWire(p)), nil
}

// GetPackageCleanupPolicy returns stored or availability-projected criteria.
func (s PackagePoliciesAPI) GetPackageCleanupPolicy(ctx context.Context, req artgen.GetPackageCleanupPolicyRequestObject) (artgen.GetPackageCleanupPolicyResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	p, err := svc.Get(ctx, req.PolicyKey, httpkit.Deref(req.Params.CheckRepoAvailability))
	if err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.GetPackageCleanupPolicy200JSONResponse(policyWire(p)), nil
}

// GetAllPackageCleanupPolicies requires an explicit own-project filter for a
// project administrator; omission selects global policies for a platform admin.
func (s PackagePoliciesAPI) GetAllPackageCleanupPolicies(
	ctx context.Context,
	req artgen.GetAllPackageCleanupPoliciesRequestObject,
) (artgen.GetAllPackageCleanupPoliciesResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	all, err := svc.List(ctx, httpkit.Deref(req.Params.ProjectKey))
	if err != nil {
		return nil, policyError(ctx, err)
	}
	out := artgen.GetAllPackageCleanupPolicies200JSONResponse{}
	for _, p := range all {
		out = append(out, policyWire(p))
	}
	return out, nil
}

// UpdatePackageCleanupPolicy replaces configuration while preserving enablement.
func (s PackagePoliciesAPI) UpdatePackageCleanupPolicy(
	ctx context.Context,
	req artgen.UpdatePackageCleanupPolicyRequestObject,
) (artgen.UpdatePackageCleanupPolicyResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a package policy body is required")
	}
	p, err := svc.Update(ctx, req.PolicyKey, policyInput(*req.Body))
	if err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.UpdatePackageCleanupPolicy200JSONResponse(policyWire(p)), nil
}

// DeletePackageCleanupPolicy returns 200 and keeps existing run history.
func (s PackagePoliciesAPI) DeletePackageCleanupPolicy(
	ctx context.Context,
	req artgen.DeletePackageCleanupPolicyRequestObject,
) (artgen.DeletePackageCleanupPolicyResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, req.PolicyKey); err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.DeletePackageCleanupPolicy200JSONResponse("Cleanup policy deleted"), nil
}

// EnableDisablePackageCleanupPolicy commits eligibility and its private fence.
func (s PackagePoliciesAPI) EnableDisablePackageCleanupPolicy(
	ctx context.Context,
	req artgen.EnableDisablePackageCleanupPolicyRequestObject,
) (artgen.EnableDisablePackageCleanupPolicyResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("an enablement body is required")
	}
	if err := svc.Enable(ctx, req.PolicyKey, req.Body.Enabled); err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.EnableDisablePackageCleanupPolicy200Response{}, nil
}

// DisableAllCleanupPolicies rotates every matching policy's durable fence.
func (s PackagePoliciesAPI) DisableAllCleanupPolicies(
	ctx context.Context,
	req artgen.DisableAllCleanupPoliciesRequestObject,
) (artgen.DisableAllCleanupPoliciesResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if err := svc.DisableAll(ctx, httpkit.Deref(req.Params.ProjectKey)); err != nil {
		return nil, policyError(ctx, err)
	}
	return artgen.DisableAllCleanupPolicies200Response{}, nil
}
