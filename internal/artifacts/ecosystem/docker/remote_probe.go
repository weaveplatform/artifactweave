package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// BindAuthority preserves the normal registry policy's credential/options
// binding for no-network authorization and later native existence probes.
func (c *RegistryContent) BindAuthority(ctx context.Context, q source.Request, permit source.Permit) (source.AuthorityBinding, error) {
	if c.Policy == nil {
		return source.AuthorityBinding{}, source.ErrUnavailable
	}
	binding, err := c.Policy.BindAuthority(ctx, q, permit)
	if err != nil {
		return binding, fmt.Errorf("bind registry source authority: %w", err)
	}
	return binding, nil
}

// Missing proves native absence after the caller separately verified the peer
// repository namespace. It uses the existing registry transport/authentication
// and source authority without publishing or negatively caching a HEAD response.
// Storage aliases alone cannot establish that a tag or digest is gone.
func (c *RegistryContent) Missing(ctx context.Context, service *source.Service, q source.Request, path string) (bool, error) {
	resource, ok := StorageResource(path)
	if !ok || service == nil || c.Policy == nil || c.Registries == nil {
		return false, source.ErrUnsupported
	}
	if q.CacheOnly || enabled(q.Repo.Offline) {
		return false, source.ErrAssumedOffline
	}
	q.Resource, q.Publication, q.SelectedProperties = resource, nil, nil
	q.SelectedPropertiesStarted = time.Time{}
	q.Resource.Method = http.MethodHead
	authority, err := service.Authorize(ctx, q)
	if err != nil {
		return false, fmt.Errorf("authorize registry absence probe: %w", err)
	}
	state, err := c.Policy.State(ctx, q, authority.Revision())
	if err != nil {
		return false, source.ErrUnavailable
	}
	if err := c.Policy.Cooldown(state); err != nil {
		return false, fmt.Errorf("registry absence probe cooldown: %w", err)
	}
	if err := authority.Revalidate(ctx); err != nil {
		return false, fmt.Errorf("current registry absence authority: %w", err)
	}
	image, operation, reference, err := ParseRegistryPath(resource.UpstreamPath)
	if err != nil {
		return false, source.ErrUnsupported
	}
	client, err := c.Registries.Open(ctx, q.Repo, authority.Identity(), authority.Revision())
	if err != nil {
		return false, source.ErrSuperseded
	}
	response, err := client.Do(ctx, image, remote.Request{Method: http.MethodHead, Path: operation + "/" + reference, Header: http.Header{"Accept": {resource.Accept}}})
	if err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("registry absence probe cancelled: %w", ctx.Err())
		}
		if errors.Is(err, remote.ErrRegistryDenied) {
			return false, &source.HTTPError{Kind: source.FailureKindHTTP, Status: http.StatusUnauthorized}
		}
		return false, &source.HTTPError{Kind: source.FailureKindConnectivity, Status: http.StatusBadGateway}
	}
	defer response.Body.Close()
	if err := authority.Revalidate(ctx); err != nil {
		return false, fmt.Errorf("registry absence publication authority: %w", err)
	}
	switch response.StatusCode {
	case http.StatusOK:
		return false, nil
	case http.StatusNotFound:
		// A foreign layer may remain referenced outside this registry. A
		// registry miss cannot authorize removing that valid cached dependency.
		if operation == "blobs" && enabled(q.Repo.CachingLocalForeignLayersEnabled) {
			return false, source.ErrUnsupported
		}
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, &source.HTTPError{Kind: source.FailureKindHTTP, Status: response.StatusCode}
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false, source.ErrUnsupported
	default:
		return false, &source.HTTPError{Kind: source.FailureKindHTTP, Status: http.StatusBadGateway}
	}
}
