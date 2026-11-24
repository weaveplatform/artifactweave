package federationhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// FederatedOperations is what the federated repository operations call
// (federated.Operations).
type FederatedOperations interface {
	ConfigSync(ctx context.Context, key string) error
	Migrate(ctx context.Context, key string) error
	ConvertToLocal(ctx context.Context, keys []string) ([]string, map[string]string, error)
	ReplaceURL(ctx context.Context, oldBase, newBase string) error
}

// Answers of the federated repository operations. configSync's and the URL
// rewrite's are 7.161.26's (FederatedRepoResource.java:424,
// FederatedReplaceUrlResource.java:137-140); migrate's and convertToLocal's
// bodies are closed source, and jfrog-client-go's ConvertLocalToFederated
// reads only the status, so theirs are artifactweave's [I].
const (
	msgConfigSync = "Configuration sync executed successfully"
	msgMigrated   = "Repository '%s' was converted to a federated repository"
	msgConverted  = "Converted to local repositories: %s"
)

// FederatedAPI serves configSync, migrate, convertToLocal, replaceUrl and
// updateMemberUrl under /artifactory/api/federation (FED-01). Without
// Operations each answers 501, as its stub did.
type FederatedAPI struct {
	Operations FederatedOperations
}

func (a FederatedAPI) ops() (FederatedOperations, error) {
	if a.Operations == nil {
		return nil, apierror.ErrNotImplemented
	}
	return a.Operations, nil
}

// federatedError turns the operations' errors into answers: a refusal is
// 400 with its message, a missing repository 404, authority errors pass to
// the strict error handler, and anything else is 500 with its cause logged.
func federatedError(ctx context.Context, key string, err error) error {
	var refusal *federated.RefusalError
	var response *apierror.Error
	switch {
	case errors.As(err, &refusal):
		return apierror.New(http.StatusBadRequest, refusal.Message)
	case errors.Is(err, federated.ErrNoRepository):
		return apierror.New(http.StatusNotFound, fmt.Sprintf("Repository '%s' does not exist", key))
	case errors.As(err, &response),
		errors.Is(err, kernel.ErrUnauthenticated),
		errors.Is(err, kernel.ErrBadCredentials),
		errors.Is(err, kernel.ErrForbidden),
		errors.Is(err, kernel.ErrPasswordExpired),
		errors.Is(err, kernel.ErrAPIRestricted):
		return err
	}
	httpkit.NoteCause(ctx, err)
	return apierror.New(http.StatusInternalServerError, "internal error")
}

// SynchronizeFederatedMemberConfiguration queues the repository's
// configuration to every member and answers 7.x's confirmation.
func (a FederatedAPI) SynchronizeFederatedMemberConfiguration(
	ctx context.Context,
	req artgen.SynchronizeFederatedMemberConfigurationRequestObject,
) (artgen.SynchronizeFederatedMemberConfigurationResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.ConfigSync(ctx, req.RepoKey); err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.SynchronizeFederatedMemberConfiguration200TextResponse(msgConfigSync), nil
}

// ConvertLocalRepositoryToFederated turns a local repository into a
// federated one (POST federation/migrate/{repoKey}).
func (a FederatedAPI) ConvertLocalRepositoryToFederated(
	ctx context.Context,
	req artgen.ConvertLocalRepositoryToFederatedRequestObject,
) (artgen.ConvertLocalRepositoryToFederatedResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.Migrate(ctx, req.RepoKey); err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.ConvertLocalRepositoryToFederated200TextResponse(fmt.Sprintf(msgMigrated, req.RepoKey)), nil
}

// ConvertFederatedRepositoryToLocal converts each listed federated
// repository back to local. Every one converted answers 200 with a
// confirmation; some converted answers 200 with the others' reasons; none
// converted answers 400 with the reasons.
func (a FederatedAPI) ConvertFederatedRepositoryToLocal(
	ctx context.Context,
	req artgen.ConvertFederatedRepositoryToLocalRequestObject,
) (artgen.ConvertFederatedRepositoryToLocalResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	var keys []string
	if req.Body != nil {
		keys = *req.Body
	}
	converted, failed, err := ops.ConvertToLocal(ctx, keys)
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	var body artgen.ConvertFederatedRepositoryToLocal200JSONResponseBody
	switch {
	case len(failed) == 0:
		err = body.FromConvertFederatedRepositoryToLocal200JSONResponseBody0(
			fmt.Sprintf(msgConverted, strings.Join(converted, ", ")))
	case len(converted) == 0:
		return artgen.ConvertFederatedRepositoryToLocal400JSONResponse{FailedConversionRepositories: &failed}, nil
	default:
		err = body.FromConvertFederatedRepositoryToLocal200JSONResponseBody1(
			artgen.ConvertFederatedRepositoryToLocal200JSONResponseBody1{FailedConversionRepositories: &failed})
	}
	if err != nil {
		return nil, fmt.Errorf("encode convertToLocal answer: %w", err)
	}
	return artgen.ConvertFederatedRepositoryToLocal200JSONResponse(body), nil
}

// ReplaceFederatedMemberBaseUrl rewrites member base URLs on every
// federated repository (POST federation/replaceUrl).
func (a FederatedAPI) ReplaceFederatedMemberBaseUrl( //nolint:revive // the generated operation name
	ctx context.Context,
	req artgen.ReplaceFederatedMemberBaseUrlRequestObject,
) (artgen.ReplaceFederatedMemberBaseUrlResponseObject, error) {
	msg, err := a.replace(ctx, req.Body)
	if err != nil {
		return nil, err
	}
	return artgen.ReplaceFederatedMemberBaseUrl200TextResponse(msg), nil
}

// UpdateFederatedMemberUrl is replaceUrl under its other path (POST
// federation/updateMemberUrl), which 7.161.26 serves with the same body.
func (a FederatedAPI) UpdateFederatedMemberUrl( //nolint:revive // the generated operation name
	ctx context.Context,
	req artgen.UpdateFederatedMemberUrlRequestObject,
) (artgen.UpdateFederatedMemberUrlResponseObject, error) {
	msg, err := a.replace(ctx, req.Body)
	if err != nil {
		return nil, err
	}
	return artgen.UpdateFederatedMemberUrl200TextResponse(msg), nil
}

func (a FederatedAPI) replace(ctx context.Context, body *artgen.FederatedReplaceUrl) (string, error) {
	ops, err := a.ops()
	if err != nil {
		return "", err
	}
	if body == nil {
		body = &artgen.FederatedReplaceUrl{}
	}
	if err := ops.ReplaceURL(ctx, body.OldBaseUrl, body.NewBaseUrl); err != nil {
		return "", federatedError(ctx, "", err)
	}
	return fmt.Sprintf(federated.MsgReplaceURL, body.OldBaseUrl, body.NewBaseUrl), nil
}
