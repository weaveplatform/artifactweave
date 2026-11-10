package accesshttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/identity"
	"github.com/weaveplatform/artifactweave/migration/access/core/permission"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// accessState is what the access partials share: their dependencies and
// the helpers that reach the store and the logger and map the identity and
// permission services' errors. AccessService and SecurityAPI embed one
// value. It defines no method named after an operation: promoted into a
// composite beside the generated stubs, such a method would make the
// operation ambiguous.
type accessState struct {
	deps Deps
}

// AccessService implements the access spec's operations. It is the access
// spec's partial strict server: the composition root embeds it beside the
// generated stubs, which answer 501 for every operation it leaves out.
type AccessService struct{ accessState }

// SecurityAPI implements the artifactory spec's security operations: users,
// groups, permission targets, key pairs and certificates, API restrictions,
// passwords and policies, and the legacy tokens. It is a partial strict
// server of the artifactory spec, which the composition root embeds beside
// the other partials and the generated stubs.
type SecurityAPI struct{ accessState }

// deciderFor returns the decider of p's permissions: d, the decider the
// middleware stored with the request's principal (accessapi.DeciderFrom), or,
// when d is nil, az's evaluation of p, for a principal the middleware did not
// see. The error is For's, unwrapped: it already names the read that failed,
// and callers wrap it with their own operation.
func deciderFor(
	ctx context.Context,
	az accessapi.Authorizer,
	p kernel.Principal,
	d accessapi.Decider,
) (accessapi.Decider, error) {
	if d != nil {
		return d, nil
	}
	return az.For(ctx, p) //nolint:wrapcheck // For names the read that failed
}

// content returns the store. A router built without one (tests of the
// routing alone) answers like an unimplemented operation.
func (s accessState) content() (Store, error) {
	if s.deps.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	return s.deps.Store, nil
}

// internalError logs err and returns a 500 that names nothing internal.
func (s accessState) internalError(ctx context.Context, op string, err error) error {
	s.log().ErrorContext(ctx, op, "request_id", httpkit.RequestIDFrom(ctx), "error", err)
	return apierror.New(http.StatusInternalServerError, "internal error")
}

// log is the configured logger, or slog's default for handlers built
// without one in tests.
func (s accessState) log() *slog.Logger {
	if s.deps.Log == nil {
		return slog.Default()
	}
	return s.deps.Log
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
		return httpkit.BadRequestf("%s", msg)
	case errors.Is(err, kernel.ErrNotFound) && notFound != "":
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
		return httpkit.BadRequestf("%s", after(err, permission.ErrInvalid))
	case errors.Is(err, types.ErrLossyProjection):
		return httpkit.BadRequestf("%s; read it through /access/api/v2/permissions", err)
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
