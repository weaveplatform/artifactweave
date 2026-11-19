package itemkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// listingSourceError retains the phase as well as the original cause. Errors
// reading cached children keep the kit's existing internal-error boundary.
type listingSourceError struct{ cause error }

func (e *listingSourceError) Error() string { return e.cause.Error() }
func (e *listingSourceError) Unwrap() error { return e.cause }

func newListingSourceError(ctx context.Context, err error) error {
	if ended := ctx.Err(); ended != nil {
		err = ended
	} else if listingStoreFault(err) {
		// A live own-store fault stays 500 even when the driver also
		// reports a timeout. SourceError already understands this marker.
		err = item.MarkStorageFault(ctx, err, err)
	}
	return &listingSourceError{cause: err}
}

// listingReadError leaves ordinary upstream acquisition failures to the
// established cached-child fallback. Only trusted fault/authority types can
// turn this phase into a terminal response; an error's text proves nothing.
func listingReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return newListingSourceError(ctx, err)
	}
	var policy *format.RejectionError
	if listingStoreFault(err) ||
		errors.Is(err, source.ErrDenied) || errors.Is(err, source.ErrSuperseded) ||
		errors.Is(err, kernel.ErrForbidden) || errors.Is(err, kernel.ErrUnauthenticated) ||
		errors.As(err, &policy) {
		return newListingSourceError(ctx, err)
	}
	return nil
}

func listingStoreFault(err error) bool {
	var content *source.ContentOpenError
	return (errors.Is(err, kernel.ErrStore) || errors.Is(err, format.ErrStorageFault)) &&
		!errors.As(err, &content)
}

func (l *virtualListing) answerError(ctx context.Context, err error) error {
	var failure *listingSourceError
	if errors.As(err, &failure) {
		return fmt.Errorf("list folder source: %w", pkgroute.SourceError(failure.cause, l.p.Anonymous))
	}
	return l.s.InternalError(ctx, "list folder", err)
}
