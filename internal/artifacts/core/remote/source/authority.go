package source

import (
	"context"
	"fmt"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// AuthorityBinding pins backend-specific dataset authority without retrieving bytes.
// Revision is opaque and may enter only a hashed cache identity.
type AuthorityBinding struct {
	Revision   string
	Revalidate func(context.Context) error
}

// AuthorityBackend optionally binds a cached format intermediate to the same
// credential/options fence the backend uses for ordinary reads.
type AuthorityBackend interface {
	BindAuthority(context.Context, Request, Permit) (AuthorityBinding, error)
}

// Authority is a single incoming request's source/permission snapshot. It owns
// no body and performs no backend reads. Create it anew when serving a cached
// format intermediate; never cache Authority or its request-bound checks.
type Authority struct {
	service *Service
	request Request
	permit  Permit
	once    sync.Once
	fences  Result
	binding AuthorityBinding
}

// Authorize applies the same pre-backend checks as Read without fetching bytes.
// Resolve and Revalidate are the service's existing no-network authority hooks.
func (s *Service) Authorize(ctx context.Context, q Request) (*Authority, error) {
	return s.authorize(ctx, q, true)
}

func (s *Service) authorize(ctx context.Context, q Request, bind bool) (*Authority, error) {
	if err := authorize(q); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	if s.Resolve == nil {
		return nil, ErrUnsupported
	}
	identity, err := s.Resolve(ctx, q.Repo)
	if err != nil {
		return nil, safeError(err)
	}
	if identity.Kind != model.ExternalHTTP {
		return nil, ErrUnsupported
	}
	if identity.Repository == "" || identity.ID == "" || identity.Generation == "" {
		return nil, ErrUnavailable
	}
	permit := Permit{Identity: identity, Intent: RetrievalIntentMayFetch, UpstreamNetworkAllowed: true}
	if q.CacheOnly {
		permit.Intent = RetrievalIntentCacheOnly
		permit.UpstreamNetworkAllowed = false
	}
	if err := s.revalidate(ctx, q, permit); err != nil {
		return nil, err
	}
	// Only a full pattern check authorizes this policy namespace. Internal
	// blackout-only lookups retain their ordinary publication admission.
	if q.Resource.Kind == ResourceKindArtifact && q.Resource.PolicyPath != "" && !q.Resource.BlackoutOnly && !q.Resource.permissionOnly {
		permit.cachePolicy, err = format.NewCachePolicy(q.Repo, identity, q.Resource.Path, q.Resource.PolicyPath)
		if err != nil {
			return nil, fmt.Errorf("source cache policy: %w", err)
		}
	}
	a := &Authority{service: s, request: q, permit: permit}
	backend := s.HTTP
	if q.CacheOnly {
		backend = s.Cache
	}
	if binder, ok := backend.(AuthorityBackend); bind && ok {
		a.binding, err = binder.BindAuthority(ctx, q, permit)
		if err != nil {
			return nil, safeError(err)
		}
	}
	return a, nil
}

// Identity is the pinned source incarnation, including its settings generation.
func (a *Authority) Identity() model.SourceIdentity { return a.permit.Identity }

// Revision is the backend-specific authority snapshot, empty for cache-only reads.
func (a *Authority) Revision() string { return a.binding.Revision }

func (a *Authority) checks() *Result {
	a.once.Do(func() {
		a.fences.Revalidate = a.binding.Revalidate
		a.service.fenceDelivery(a.request, a.permit, a.permit.Identity, &a.fences)
	})
	return &a.fences
}

// Revalidate repeats current caller/repository/source checks before disclosure.
func (a *Authority) Revalidate(ctx context.Context) error { return a.checks().Revalidate(ctx) }

// CheckPermission checks a disclosed descendant against current Read authority,
// bounded by the metadata scope originally admitted by Authorize.
func (a *Authority) CheckPermission(ctx context.Context, path string) error {
	return a.checks().CheckPermission(ctx, path)
}

// CheckRead also applies the descendant's current download policy.
func (a *Authority) CheckRead(ctx context.Context, path string) error {
	return a.checks().CheckRead(ctx, path)
}
