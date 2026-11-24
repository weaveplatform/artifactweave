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
	FullSync(ctx context.Context, key, mirror string, forced bool) error
	Recovery(ctx context.Context, key string, force bool) error
	Reconnect(ctx context.Context) error
	RemoveMember(ctx context.Context, raw string) (int, error)
	Prioritize(ctx context.Context, key, level string) error
	SetPropagateCreatedTime(ctx context.Context, enabled bool) error
	Administrator(ctx context.Context) error
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

// FED-04's answers. reconnect's `true` is research 40 §7's; the others'
// bodies are closed source and no client reads them, so their wording is
// artifactweave's [I]. fullSync's 200 is the documented object with
// neither of its optional fields: no message is known, and resultPaths
// names comparison files, which only the refused isDry writes.
const (
	msgRecoveryAll    = "Recovery started for every federated repository"
	msgRecoveryOne    = "Recovery started for repository '%s'"
	msgReconnected    = "true"
	msgMemberRemoved  = "Member '%s' was removed from %d federated repositories"
	msgPrioritized    = "Repository '%s' priority is %s"
	msgIsDryDeviation = "isDry is not supported: artifactweave runs no full-sync comparison"
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
	case errors.Is(err, federated.ErrNoStreams):
		return apierror.ErrNotImplemented
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

// FederatedRepositoryFullSync runs a full sync with every member, or the
// one ?mirror names (POST federation/fullSync/{repoKey}). ?isDry=true is a
// registered deviation: 400 naming the parameter, as the comparison report
// it would write is RTFS's format.
func (a FederatedAPI) FederatedRepositoryFullSync(
	ctx context.Context,
	req artgen.FederatedRepositoryFullSyncRequestObject,
) (artgen.FederatedRepositoryFullSyncResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if req.Params.IsDry != nil && *req.Params.IsDry {
		// After the administrator check, so the parameter tells no one else
		// anything.
		if err := ops.Administrator(ctx); err != nil {
			return nil, federatedError(ctx, req.RepoKey, err)
		}
		return nil, apierror.New(http.StatusBadRequest, msgIsDryDeviation)
	}
	if err := ops.FullSync(ctx, req.RepoKey, deref(req.Params.Mirror), false); err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.FederatedRepositoryFullSync200JSONResponse{}, nil
}

// FederatedRepositoryForceFullSync replaces every member's copy, or the
// one ?mirror names, with this instance's (POST
// federation/forceFullSync/{repoKey}).
func (a FederatedAPI) FederatedRepositoryForceFullSync(
	ctx context.Context,
	req artgen.FederatedRepositoryForceFullSyncRequestObject,
) (artgen.FederatedRepositoryForceFullSyncResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.FullSync(ctx, req.RepoKey, deref(req.Params.Mirror), true); err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.FederatedRepositoryForceFullSync200JSONResponse{}, nil
}

// FederationRecovery retries at once the delivery that is backing off, for
// one repository or every federated one (POST federation/recovery).
func (a FederatedAPI) FederationRecovery(
	ctx context.Context,
	req artgen.FederationRecoveryRequestObject,
) (artgen.FederationRecoveryResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	key, force := deref(req.Params.RepoKey), req.Params.Force != nil && *req.Params.Force
	if err := ops.Recovery(ctx, key, force); err != nil {
		return nil, federatedError(ctx, key, err)
	}
	if key == "" {
		return artgen.FederationRecovery200JSONResponse(msgRecoveryAll), nil
	}
	return artgen.FederationRecovery200JSONResponse(fmt.Sprintf(msgRecoveryOne, key)), nil
}

// ReconnectToFederationClient re-establishes delivery to every member
// (POST federation/reconnect).
func (a FederatedAPI) ReconnectToFederationClient(
	ctx context.Context,
	_ artgen.ReconnectToFederationClientRequestObject,
) (artgen.ReconnectToFederationClientResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.Reconnect(ctx); err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.ReconnectToFederationClient200TextResponse(msgReconnected), nil
}

// RemoveFederationMember removes the member the body names, by its base
// URL or member URL, from every federated repository (POST
// federation/removeMember).
func (a FederatedAPI) RemoveFederationMember(
	ctx context.Context,
	req artgen.RemoveFederationMemberRequestObject,
) (artgen.RemoveFederationMemberResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(deref(req.Body))
	n, err := ops.RemoveMember(ctx, raw)
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.RemoveFederationMember200JSONResponse(fmt.Sprintf(msgMemberRemoved, raw, n)), nil
}

// PrioritizeFederatedRepository sets a repository's dispatch priority
// (PUT federation/prioritization/{repoKey}).
func (a FederatedAPI) PrioritizeFederatedRepository(
	ctx context.Context,
	req artgen.PrioritizeFederatedRepositoryRequestObject,
) (artgen.PrioritizeFederatedRepositoryResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	level := string(req.Params.PriorityLevel)
	if err := ops.Prioritize(ctx, req.RepoKey, level); err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	return artgen.PrioritizeFederatedRepository200TextResponse(
		fmt.Sprintf(msgPrioritized, req.RepoKey, strings.ToUpper(level))), nil
}

// PropagateCreationTimeMetadataDuringFullSync stores whether full sync
// carries original creation times (PUT federation/propagateCreatedTime).
// The 200 object's fields are closed source; it echoes the setting [I].
func (a FederatedAPI) PropagateCreationTimeMetadataDuringFullSync(
	ctx context.Context,
	req artgen.PropagateCreationTimeMetadataDuringFullSyncRequestObject,
) (artgen.PropagateCreationTimeMetadataDuringFullSyncResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.SetPropagateCreatedTime(ctx, req.Params.Enabled); err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.PropagateCreationTimeMetadataDuringFullSync200JSONResponse{"enabled": req.Params.Enabled}, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
