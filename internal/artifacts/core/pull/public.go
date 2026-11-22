package pull

import (
	"context"
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Public exposes owner operations with published recovery categories. No other
// business module needs to import source HTTP errors or implementation sentinels.
type Public struct{ Source }

func publicError(err error) error {
	if err == nil {
		return nil
	}
	var category error
	var failure *source.HTTPError
	switch {
	case errors.Is(err, source.ErrAssumedOffline):
		category = api.ErrPullOffline
	case errors.Is(err, source.ErrDenied), errors.Is(err, kernel.ErrForbidden), errors.Is(err, kernel.ErrBadCredentials):
		category = api.ErrPullDenied
	case errors.Is(err, source.ErrSuperseded), errors.Is(err, kernel.ErrConflict), errors.Is(err, kernel.ErrNotFound):
		category = api.ErrPullChanged
	case errors.Is(err, source.ErrInvalidResult):
		category = api.ErrPullIntegrity
	case errors.Is(err, source.ErrUnsupported):
		category = api.ErrPullUnsupported
	case errors.Is(err, source.ErrUnavailable):
		category = api.ErrPullTemporary
	case errors.As(err, &failure):
		switch {
		case failure.Kind == source.FailureKindIntegrity:
			category = api.ErrPullIntegrity
		case failure.Status == http.StatusUnauthorized || failure.Status == http.StatusForbidden:
			category = api.ErrPullDenied
		case failure.Kind == source.FailureKindProtocol:
			category = api.ErrPullUnsupported
		default:
			category = api.ErrPullTemporary
		}
	}
	if category == nil {
		return err
	}
	return errors.Join(category, err)
}

// Repository publishes the current source/layout/options binding without I/O.
func (p Public) Repository(ctx context.Context, key string, selection api.PullSelection) (api.PullRepository, error) {
	r, err := p.Source.Repository(ctx, key, selection)
	return r, publicError(err)
}

// Items publishes a bounded current selected upstream page.
func (p Public) Items(ctx context.Context, repository api.PullRepository, selection api.PullSelection, page kernel.Page) ([]api.PullItem, string, error) {
	items, next, err := p.Source.Items(ctx, repository, selection, page)
	return items, next, publicError(err)
}

// Fetch publishes an actual persisted cache receipt and recovery category.
func (p Public) Fetch(ctx context.Context, item api.PullItem, publication api.PullPublication) (api.PullCached, error) {
	result, err := p.Source.Fetch(ctx, item, publication)
	return result, publicError(err)
}

// WithCurrent keeps checkpoint authority at the owner transaction boundary.
func (p Public) WithCurrent(ctx context.Context, item api.PullCached, effect func(context.Context) error) error {
	return publicError(p.Source.WithCurrent(ctx, item, effect))
}

// CachedItems publishes only bounded local cache candidates.
func (p Public) CachedItems(ctx context.Context, repository api.PullRepository, selection api.PullSelection, page kernel.Page) ([]api.PullCacheCandidate, string, error) {
	items, next, err := p.Source.CachedItems(ctx, repository, selection, page)
	return items, next, publicError(err)
}

// Remove publishes exact local removal results, never an upstream mutation.
func (p Public) Remove(ctx context.Context, item api.PullCacheCandidate, publication api.PullPublication, record func(context.Context, []string) error) ([]string, error) {
	paths, err := p.Source.Remove(ctx, item, publication, record)
	return paths, publicError(err)
}

var _ api.PullSource = Public{}
