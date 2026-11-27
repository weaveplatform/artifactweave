package peer

import (
	"context"
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// ConfigurationReceiver applies a configuration a member sends
// (federated.Receiver).
type ConfigurationReceiver interface {
	Apply(ctx context.Context, caller model.Peer, repoKey string, cfg map[string]any) error
}

// RepositoryServer serves PUT /artifactweave/peer/v1/repositories/{repoKey},
// configSync's receiving side (decision 0022). Server.Middleware has
// verified the caller's signature before it gets here.
type RepositoryServer struct {
	Receiver ConfigurationReceiver
	Reader   interface {
		Configuration(context.Context, model.Peer, string) (map[string]any, error)
	}
	Cleanup CleanupReceiver
}

// PutRepositoryConfiguration creates or updates this instance's copy of the
// federated repository the caller sends.
func (s RepositoryServer) PutRepositoryConfiguration(
	ctx context.Context,
	req peergen.PutRepositoryConfigurationRequestObject,
) (peergen.PutRepositoryConfigurationResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	if req.Body == nil || req.Body.Configuration == nil {
		return nil, apierror.New(http.StatusBadRequest, "The body must carry the configuration")
	}
	err := s.Receiver.Apply(ctx, caller, req.RepoKey, req.Body.Configuration)
	var refusal *federated.RefusalError
	switch {
	case err == nil:
		return peergen.PutRepositoryConfiguration204Response{}, nil
	case errors.As(err, &refusal):
		return nil, apierror.New(http.StatusBadRequest, refusal.Message)
	case errors.Is(err, kernel.ErrForbidden):
		return nil, apierror.New(http.StatusForbidden,
			"The caller holds no federated-repo/* trust with this instance")
	case errors.Is(err, kernel.ErrConflict):
		return nil, apierror.New(http.StatusConflict,
			"A repository with this key exists here and is not a federated repository of the same package type")
	default:
		return nil, err //nolint:wrapcheck // the strict error handler answers 500 and logs the cause
	}
}

// GetRepositoryConfiguration reads only configurations the current peer's
// membership permits; the signed response never carries unmasked secrets.
func (s RepositoryServer) GetRepositoryConfiguration(
	ctx context.Context,
	req peergen.GetRepositoryConfigurationRequestObject,
) (peergen.GetRepositoryConfigurationResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	if s.Reader == nil {
		return nil, apierror.ErrNotImplemented
	}
	cfg, err := s.Reader.Configuration(ctx, caller, req.RepoKey)
	if errors.Is(err, kernel.ErrForbidden) {
		return nil, apierror.New(http.StatusForbidden, "The caller is not a current federation member")
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // the strict handler logs the private cause
	}
	return peergen.GetRepositoryConfiguration200JSONResponse{Stream: model.RepositoryStream(req.RepoKey), Configuration: cfg}, nil
}
