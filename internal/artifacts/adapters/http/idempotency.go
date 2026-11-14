package artifactshttp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

// errRequestType is a binding handed another operation's request object,
// which the router's wiring never does.
var errRequestType = errors.New("artifacts: replay binding got another operation's request")

// KeyedBindings returns the replay bindings of the artifactory spec's
// operations that this partial serves and that accept Idempotency-Key, by
// operation ID. The router gives them to the shared replay
// (idempotency.Strict), which runs the handler as the effect.
func (s ArtifactoryAPI) KeyedBindings() map[string]idempotency.Binding {
	return map[string]idempotency.Binding{"appendBuild": s.appendBinding()}
}

// appendResource is a keyed append's canonical resource: the project with
// "default" resolved, and started at the store's microsecond precision, so
// that equivalent spellings of one run share a scope (decision 0043).
type appendResource struct {
	Project string    `json:"project"`
	Name    string    `json:"name"`
	Number  string    `json:"number"`
	Started time.Time `json:"started"`
}

func (s ArtifactoryAPI) appendBinding() idempotency.Binding {
	return idempotency.Binding{
		Name: "append",
		// The handler's own checks, in its order, so that a request they
		// refuse answers as it does without the replay.
		Resource: func(ctx context.Context, request any) (any, error) {
			req, ok := request.(artgen.AppendBuildRequestObject)
			if !ok {
				return nil, errRequestType
			}
			if _, _, _, err := s.buildCaller(ctx); err != nil {
				return nil, err
			}
			ap, err := appendOf(ctx, req, kernel.Principal{})
			if err != nil {
				return nil, err
			}
			return appendResource{
				Project: build.ProjectKey(ap.ProjectKey), Name: ap.Name, Number: ap.Number,
				Started: kernel.Normalise(ap.Started),
			}, nil
		},
		Reload: func(ctx context.Context, request any, receipt idempotency.ReplayReceipt) (any, error) {
			req, ok := request.(artgen.AppendBuildRequestObject)
			if !ok {
				return nil, errRequestType
			}
			svc, p, _, err := s.buildCaller(ctx)
			if err != nil {
				return nil, err
			}
			ap, err := appendOf(ctx, req, p)
			if err != nil {
				return nil, err
			}
			if err := svc.ConfirmAppend(
				ctx,
				ap,
				receipt.ResourceID,
				s.currentBuildAccess(p),
			); err != nil {
				// The prefix keeps the 409 text a keyed append has always
				// given for a renamed run.
				return nil, fmt.Errorf("protected append: build: append once: %w", err)
			}
			return artgen.AppendBuild204Response{}, nil
		},
		Error: func(ctx context.Context, request any, err error) error {
			req, _ := request.(artgen.AppendBuildRequestObject)
			p, _ := kernel.FromContext(ctx)
			project := httpkit.Deref(req.Params.Project)
			return s.buildError(ctx, p, project, noRun(req.BuildName, req.BuildNumber), err)
		},
	}
}

// appendOf reads an append's run selection and modules from req, as
// AppendBuild does, by p.
func appendOf(
	ctx context.Context,
	req artgen.AppendBuildRequestObject,
	p kernel.Principal,
) (build.Append, error) {
	project := httpkit.Deref(req.Params.Project)
	if err := checkBuildRepo(project, req.Params.BuildRepo); err != nil {
		return build.Append{}, err
	}
	at, err := started(req.Params.Started)
	if err != nil {
		return build.Append{}, err
	}
	return build.Append{
		ProjectKey: project, Name: req.BuildName, Number: req.BuildNumber, Started: at,
		Modules: httpkit.RawBody(ctx), By: p.Name,
	}, nil
}

// currentBuildAccess evaluates p's credential and permissions as they stand
// when a keyed append's effect or reload runs, not the request's decider: a
// replay must not recover an append that revoked access no longer allows
// (decision 0043).
func (s ArtifactoryAPI) currentBuildAccess(
	p kernel.Principal,
) func(context.Context) (build.Access, error) {
	return func(ctx context.Context) (build.Access, error) {
		if s.cfg.Access.Authenticator == nil {
			return nil, build.ErrForbidden
		}
		current, err := s.cfg.Access.Authenticator.Current(ctx, p)
		if err != nil {
			if errors.Is(err, kernel.ErrBadCredentials) ||
				errors.Is(err, kernel.ErrPasswordExpired) ||
				errors.Is(err, kernel.ErrAccountLocked) ||
				errors.Is(err, kernel.ErrNotFound) {
				return nil, build.ErrForbidden
			}
			return nil, fmt.Errorf("recheck append credential: %w", err)
		}
		a, err := itemkit.AccessFor(ctx, s.cfg.Access.Authorizer, current, nil)
		return buildAccess{a}, err
	}
}
