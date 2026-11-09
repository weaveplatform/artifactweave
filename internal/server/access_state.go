package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/migration/access/core/identity"
	"github.com/weaveplatform/artifactweave/migration/access/core/permission"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// accessState is what the access partials share: the configuration, the
// OIDC verifier and the helpers that reach the store and the logger and map
// the identity and permission services' errors. accessService and
// securityAPI embed one value, so neither builds an artifactoryService to
// reach them. It defines no method named after an operation: promoted into
// a composite beside the generated stubs, such a method would make the
// operation ambiguous.
type accessState struct {
	cfg Config
	// idTokens verifies the subject tokens of the OIDC token exchange and
	// caches each provider's signing keys for the life of the handler.
	idTokens *idtoken.Verifier
}

// accessService implements the access spec's operations; the composite
// strict server (composites.go) adds the generated stubs.
type accessService struct{ accessState }

// securityAPI implements the artifactory spec's security operations: users,
// groups, permission targets, key pairs and certificates, API restrictions,
// passwords and policies, and the legacy tokens. It is a partial of the
// artifactory composite (composites.go).
type securityAPI struct{ accessState }

// content returns the store. A router built without one (tests of the
// routing alone) answers like an unimplemented operation.
func (s accessState) content() (store.Store, error) {
	if s.cfg.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	return s.cfg.Store, nil
}

// internalError logs err and returns a 500 that names nothing internal.
func (s accessState) internalError(ctx context.Context, op string, err error) error {
	s.log().ErrorContext(ctx, op, "request_id", httpkit.RequestIDFrom(ctx), "error", err)
	return apierror.New(http.StatusInternalServerError, "internal error")
}

// log is the configured logger, or slog's default for handlers built
// without one in tests.
func (s accessState) log() *slog.Logger {
	if s.cfg.Logger == nil {
		return slog.Default()
	}
	return s.cfg.Logger
}

// identityErr maps the identity service's errors: ErrInvalid is a 400 with
// its message, ErrNotFound a 404 with notFound, anything else a 500.
func (s accessState) identityErr(ctx context.Context, op, notFound string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, identity.ErrInvalid):
		// The message is what follows the sentinel, whatever wraps it.
		msg := err.Error()
		if _, after, ok := strings.Cut(msg, identity.ErrInvalid.Error()+": "); ok {
			msg = after
		}
		return BadRequestf("%s", msg)
	case errors.Is(err, store.ErrNotFound) && notFound != "":
		return apierror.New(http.StatusNotFound, notFound)
	default:
		return s.internalError(ctx, op, err)
	}
}

// permissionError maps the service's errors: 400 for a document to fix,
// 404 and 409 for names, and 500 for anything else. auth and apierror
// errors pass through.
func (s accessState) permissionError(ctx context.Context, op string, err error) error {
	var ae *apierror.Error
	switch {
	case errors.As(err, &ae):
		return err
	case errors.Is(err, permission.ErrInvalid):
		return BadRequestf("%s", after(err, permission.ErrInvalid))
	case errors.Is(err, types.ErrLossyProjection):
		return BadRequestf("%s; read it through /access/api/v2/permissions", err)
	case errors.Is(err, permission.ErrNotFound):
		return apierror.New(http.StatusNotFound, after(err, permission.ErrNotFound))
	case errors.Is(err, permission.ErrConflict):
		return apierror.New(http.StatusConflict, after(err, permission.ErrConflict))
	case errors.Is(err, permission.ErrForbidden):
		return apierror.New(http.StatusForbidden, after(err, permission.ErrForbidden))
	default:
		return s.internalError(ctx, op, err)
	}
}
