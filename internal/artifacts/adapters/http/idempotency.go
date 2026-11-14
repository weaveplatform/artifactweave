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
	return map[string]idempotency.Binding{
		"appendBuild":          s.appendBinding(),
		"deleteBuilds":         s.deleteBinding(),
		"deleteBuildsMultiple": s.deleteMultipleBinding(),
		"promoteBuild":         s.promoteBinding(),
	}
}

// reportRetention is how long a keyed delete's or promotion's report is
// kept: an hour past its receipt, so that a receipt still in force never
// names a report pruned under a clock a little ahead of the replay's.
const reportRetention = idempotency.Retention + time.Hour

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

// deleteResource is a keyed delete's canonical resource: the build and the
// runs its query selects, numbers trimmed as the delete reads them and none
// when it deletes every run, so that equivalent spellings share a scope.
type deleteResource struct {
	Project string   `json:"project"`
	Name    string   `json:"name"`
	Numbers []string `json:"numbers"`
	All     bool     `json:"all"`
}

func (s ArtifactoryAPI) deleteBinding() idempotency.Binding {
	return idempotency.Binding{
		Name: "delete",
		Resource: func(ctx context.Context, request any) (any, error) {
			req, ok := request.(artgen.DeleteBuildsRequestObject)
			if !ok {
				return nil, errRequestType
			}
			if _, _, _, err := s.buildCaller(ctx); err != nil {
				return nil, err
			}
			d := deleteOf(req)
			if d.All {
				d.Numbers = nil
			}
			return deleteResource{
				Project: build.ProjectKey(d.ProjectKey),
				Name:    d.Name, Numbers: d.Numbers, All: d.All,
			}, nil
		},
		// Deleting the runs' artifacts too changes what a delete does, not
		// which runs it deletes.
		Fingerprint: func(_ context.Context, request any) (any, error) {
			req, ok := request.(artgen.DeleteBuildsRequestObject)
			if !ok {
				return nil, errRequestType
			}
			return struct {
				Artifacts bool `json:"artifacts"`
			}{deleteOf(req).Artifacts}, nil
		},
		Reload: func(ctx context.Context, request any, receipt idempotency.ReplayReceipt) (any, error) {
			req, ok := request.(artgen.DeleteBuildsRequestObject)
			if !ok {
				return nil, errRequestType
			}
			msg, err := s.confirmDelete(ctx, deleteOf(req), receipt)
			if err != nil {
				return nil, err
			}
			return artgen.DeleteBuilds200TextResponse(msg), nil
		},
		Error: func(ctx context.Context, request any, err error) error {
			req, _ := request.(artgen.DeleteBuildsRequestObject)
			p, _ := kernel.FromContext(ctx)
			return s.buildError(ctx, p, "", noBuild(req.BuildName), err)
		},
	}
}

// deleteMultipleBinding binds the body form of a delete, whose body names
// the build and its runs and which the fingerprint digests whole, so its
// key is scoped by the credential and the operation alone.
func (s ArtifactoryAPI) deleteMultipleBinding() idempotency.Binding {
	return idempotency.Binding{
		Name: "delete",
		Resource: func(ctx context.Context, request any) (any, error) {
			if _, ok := request.(artgen.DeleteBuildsMultipleRequestObject); !ok {
				return nil, errRequestType
			}
			if _, _, _, err := s.buildCaller(ctx); err != nil {
				return nil, err
			}
			return struct{}{}, nil
		},
		Reload: func(ctx context.Context, request any, receipt idempotency.ReplayReceipt) (any, error) {
			req, ok := request.(artgen.DeleteBuildsMultipleRequestObject)
			if !ok {
				return nil, errRequestType
			}
			msg, err := s.confirmDelete(ctx, deleteMultipleOf(req), receipt)
			if err != nil {
				return nil, err
			}
			return artgen.DeleteBuildsMultiple200TextResponse(msg), nil
		},
		Error: func(ctx context.Context, request any, err error) error {
			req, _ := request.(artgen.DeleteBuildsMultipleRequestObject)
			p, _ := kernel.FromContext(ctx)
			// The strict server decodes the required body before any of
			// this runs; only another operation's request has none.
			var d build.Delete
			if req.Body != nil {
				d = deleteMultipleOf(req)
			}
			return s.buildError(ctx, p, d.ProjectKey, noBuild(d.Name), err)
		},
	}
}

// confirmDelete is a keyed delete's reload: the answer for the runs the
// receipt's report lists, once the current credential may still delete
// them.
func (s ArtifactoryAPI) confirmDelete(
	ctx context.Context,
	d build.Delete,
	receipt idempotency.ReplayReceipt,
) (string, error) {
	svc, p, _, err := s.buildCaller(ctx)
	if err != nil {
		return "", err
	}
	gone, err := svc.ConfirmDelete(ctx, d, receipt.ResourceID, s.currentBuildAccess(p))
	if err != nil {
		return "", err //nolint:wrapcheck // the binding's Error maps it
	}
	return deletedText(d.Name, gone), nil
}

// promoteResource is a keyed promotion's canonical resource: the project
// with "default" resolved, and the build and number in the path. The body,
// which says how to promote, is fingerprinted.
type promoteResource struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Number  string `json:"number"`
}

func (s ArtifactoryAPI) promoteBinding() idempotency.Binding {
	return idempotency.Binding{
		Name: "promotion",
		Resource: func(ctx context.Context, request any) (any, error) {
			req, ok := request.(artgen.PromoteBuildRequestObject)
			if !ok {
				return nil, errRequestType
			}
			// The handler refuses a body that is not JSON before it looks
			// at the caller, and with an answer that is not an error, so
			// such a request goes on to the handler, which gives it.
			if promotionBody(req) != nil {
				if _, _, _, err := s.buildCaller(ctx); err != nil {
					return nil, err
				}
			}
			return promoteResource{
				Project: build.ProjectKey(httpkit.Deref(req.Params.Project)),
				Name:    req.BuildName, Number: req.BuildNumber,
			}, nil
		},
		Reload: func(ctx context.Context, request any, receipt idempotency.ReplayReceipt) (any, error) {
			req, ok := request.(artgen.PromoteBuildRequestObject)
			if !ok {
				return nil, errRequestType
			}
			svc, p, _, err := s.buildCaller(ctx)
			if err != nil {
				return nil, err
			}
			msgs, err := svc.ConfirmPromote(ctx, build.Promotion{
				ProjectKey: httpkit.Deref(req.Params.Project),
				Name:       req.BuildName, Number: req.BuildNumber,
			}, receipt.ResourceID, s.currentBuildAccess(p))
			if err != nil {
				return nil, err //nolint:wrapcheck // the binding's Error maps it
			}
			return artgen.PromoteBuild200JSONResponse(promotionReport(msgs)), nil
		},
		Error: func(ctx context.Context, request any, err error) error {
			req, _ := request.(artgen.PromoteBuildRequestObject)
			p, _ := kernel.FromContext(ctx)
			project := httpkit.Deref(req.Params.Project)
			return s.buildError(ctx, p, project, unpromoted(req), err)
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
// when a keyed build action's effect or reload runs, not the request's
// decider: a replay must not recover an action that revoked access no
// longer allows (decision 0043).
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
			return nil, fmt.Errorf("recheck build credential: %w", err)
		}
		a, err := itemkit.AccessFor(ctx, s.cfg.Access.Authorizer, current, nil)
		return buildAccess{a}, err
	}
}
