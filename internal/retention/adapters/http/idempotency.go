package retentionhttp

import (
	"context"
	"errors"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

var errReplayRequest = errors.New("retention: replay received another operation's request")

// KeyedBindings supplies admission replay through the shared transactional
// receipt store. Replays refresh original credential and policy/project scope.
func (s PackagePoliciesAPI) KeyedBindings() map[string]idempotency.Binding {
	binding := idempotency.Binding{
		Name: "cleanup run admission",
		Resource: func(ctx context.Context, request any) (any, error) {
			req, ok := request.(artgen.RunPackageCleanupPolicyRequestObject)
			if !ok {
				return nil, errReplayRequest
			}
			service, err := s.service()
			if err != nil {
				return nil, err
			}
			p, err := service.Get(ctx, req.PolicyKey, false)
			if err != nil {
				return nil, policyError(ctx, err)
			}
			return struct {
				Policy, Project string
			}{p.Key, p.ProjectKey}, nil
		},
		Fingerprint: func(_ context.Context, request any) (any, error) {
			req, ok := request.(artgen.RunPackageCleanupPolicyRequestObject)
			if !ok {
				return nil, errReplayRequest
			}
			return httpkit.Deref(req.Params.DryRun), nil
		},
		Reload: func(ctx context.Context, request any, receipt idempotency.ReplayReceipt) (any, error) {
			req, ok := request.(artgen.RunPackageCleanupPolicyRequestObject)
			if !ok {
				return nil, errReplayRequest
			}
			service, err := s.service()
			if err != nil {
				return nil, err
			}
			trigger, err := service.RunTrigger(ctx, req.PolicyKey, receipt.ResourceID)
			if err != nil {
				return nil, policyError(ctx, err)
			}
			id, err := cleanup.PublicRunID(trigger)
			if err != nil {
				return nil, policyError(ctx, err)
			}
			return artgen.RunPackageCleanupPolicy202JSONResponse{TriggerId: &id}, nil
		},
		Error: func(ctx context.Context, _ any, err error) error { return policyError(ctx, err) },
	}
	if s.Policies != nil {
		binding.Guard = s.Policies.Guard
	}
	return map[string]idempotency.Binding{"runPackageCleanupPolicy": binding}
}
