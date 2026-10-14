package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	"github.com/weaveplatform/artifactweave/internal/service/oidc"
)

// The OIDC integration (/access/api/v1/oidc, issue #83, research 21):
// providers and identity mappings are admin only; the token exchange needs
// no credentials, because the subject token is the credential.

// oidcTokenRoute is the exchange, which the middleware lets through without
// credentials.
const oidcTokenRoute = "POST /access/api/v1/oidc/token"

// oidcService checks the caller is an administrator and returns the service.
func (s accessService) oidcService(ctx context.Context) (oidc.Service, error) {
	if err := adminOnly(ctx); err != nil {
		return oidc.Service{}, err
	}
	return s.oidc()
}

func (s accessService) oidc() (oidc.Service, error) {
	st, err := artifactoryService{cfg: s.cfg}.content()
	if err != nil {
		return oidc.Service{}, err
	}
	return oidc.Service{Store: st, Tokens: s.cfg.Tokens, Keys: s.idTokens}, nil
}

// oidcError maps the service's errors to statuses. A provider whose keys
// cannot be fetched still answers 401, the status the operation declares for
// a token that does not verify, and is logged so an operator sees why.
func (s accessService) oidcError(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, oidc.ErrNotFound):
		return apierror.New(http.StatusNotFound, err.Error())
	case errors.Is(err, oidc.ErrInvalid), errors.Is(err, oidc.ErrNoMatch):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, oidc.ErrUnverified):
		if errors.Is(err, idtoken.ErrKeys) {
			artifactoryService{cfg: s.cfg}.log().WarnContext(ctx, op,
				"request_id", requestIDFrom(ctx), "error", err)
		}
		return apierror.New(http.StatusUnauthorized, err.Error())
	default:
		return artifactoryService{cfg: s.cfg}.internalError(ctx, op, err)
	}
}

// recode converts between the generated wire types and the service's, which
// share their JSON form.
func recode[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("oidc: encode: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("oidc: decode: %w", err)
	}
	return out, nil
}

// provider decodes a request body; a missing one is a bad request.
func providerBody(b *accessgen.OidcConfiguration) (oidc.Provider, error) {
	if b == nil {
		return oidc.Provider{}, badRequest("a body with the OIDC provider is required")
	}
	return recode[oidc.Provider](b)
}

func mappingBody(b *accessgen.OidcIdentityMapping) (oidc.Mapping, error) {
	if b == nil {
		return oidc.Mapping{}, badRequest("a body with the identity mapping is required")
	}
	return recode[oidc.Mapping](b)
}

func (s accessService) GetAllOidcConfigurations(
	ctx context.Context,
	_ accessgen.GetAllOidcConfigurationsRequestObject,
) (accessgen.GetAllOidcConfigurationsResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	all, err := svc.Providers(ctx)
	if err != nil {
		return nil, s.oidcError(ctx, "list OIDC providers", err)
	}
	out, err := recode[accessgen.GetAllOidcConfigurations200JSONResponse](all)
	if err != nil {
		return nil, s.oidcError(ctx, "list OIDC providers", err)
	}
	if out == nil {
		out = accessgen.GetAllOidcConfigurations200JSONResponse{}
	}
	return out, nil
}

// CreateOidcConfiguration answers 200, not 201: the jfrog/platform provider
// expects exactly 200.
func (s accessService) CreateOidcConfiguration(
	ctx context.Context,
	req accessgen.CreateOidcConfigurationRequestObject,
) (accessgen.CreateOidcConfigurationResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	p, err := providerBody(req.Body)
	if err != nil {
		return nil, err
	}
	if p, err = svc.CreateProvider(ctx, p); err != nil {
		return nil, s.oidcError(ctx, "create OIDC provider", err)
	}
	out, err := recode[accessgen.CreateOidcConfiguration200JSONResponse](p)
	if err != nil {
		return nil, s.oidcError(ctx, "create OIDC provider", err)
	}
	return out, nil
}

func (s accessService) GetOidcConfiguration(
	ctx context.Context,
	req accessgen.GetOidcConfigurationRequestObject,
) (accessgen.GetOidcConfigurationResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	p, err := svc.Provider(ctx, req.Name)
	if err != nil {
		return nil, s.oidcError(ctx, "get OIDC provider", err)
	}
	out, err := recode[accessgen.GetOidcConfiguration200JSONResponse](p)
	if err != nil {
		return nil, s.oidcError(ctx, "get OIDC provider", err)
	}
	return out, nil
}

func (s accessService) UpdateOidcConfiguration(
	ctx context.Context,
	req accessgen.UpdateOidcConfigurationRequestObject,
) (accessgen.UpdateOidcConfigurationResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	p, err := providerBody(req.Body)
	if err != nil {
		return nil, err
	}
	if p, err = svc.UpdateProvider(ctx, req.Name, p); err != nil {
		return nil, s.oidcError(ctx, "update OIDC provider", err)
	}
	out, err := recode[accessgen.UpdateOidcConfiguration200JSONResponse](p)
	if err != nil {
		return nil, s.oidcError(ctx, "update OIDC provider", err)
	}
	return out, nil
}

func (s accessService) DeleteOidcConfiguration(
	ctx context.Context,
	req accessgen.DeleteOidcConfigurationRequestObject,
) (accessgen.DeleteOidcConfigurationResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.DeleteProvider(ctx, req.Name); err != nil {
		return nil, s.oidcError(ctx, "delete OIDC provider", err)
	}
	return accessgen.DeleteOidcConfiguration204Response{}, nil
}

func (s accessService) GetOidcIdentityMappings(
	ctx context.Context,
	req accessgen.GetOidcIdentityMappingsRequestObject,
) (accessgen.GetOidcIdentityMappingsResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	all, err := svc.Mappings(ctx, req.ProviderName, deref(req.Params.ProjectKey))
	if err != nil {
		return nil, s.oidcError(ctx, "list identity mappings", err)
	}
	out, err := recode[accessgen.GetOidcIdentityMappings200JSONResponse](all)
	if err != nil {
		return nil, s.oidcError(ctx, "list identity mappings", err)
	}
	if out == nil {
		out = accessgen.GetOidcIdentityMappings200JSONResponse{}
	}
	return out, nil
}

func (s accessService) CreateOidcIdentityMapping(
	ctx context.Context,
	req accessgen.CreateOidcIdentityMappingRequestObject,
) (accessgen.CreateOidcIdentityMappingResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	m, err := mappingBody(req.Body)
	if err != nil {
		return nil, err
	}
	if m, err = svc.CreateMapping(ctx, req.ProviderName, m); err != nil {
		return nil, s.oidcError(ctx, "create identity mapping", err)
	}
	out, err := recode[accessgen.CreateOidcIdentityMapping200JSONResponse](m)
	if err != nil {
		return nil, s.oidcError(ctx, "create identity mapping", err)
	}
	return out, nil
}

func (s accessService) GetOidcIdentityMappingByName(
	ctx context.Context,
	req accessgen.GetOidcIdentityMappingByNameRequestObject,
) (accessgen.GetOidcIdentityMappingByNameResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	m, err := svc.Mapping(ctx, req.ProviderName, req.MappingName)
	if err != nil {
		return nil, s.oidcError(ctx, "get identity mapping", err)
	}
	out, err := recode[accessgen.GetOidcIdentityMappingByName200JSONResponse](m)
	if err != nil {
		return nil, s.oidcError(ctx, "get identity mapping", err)
	}
	return out, nil
}

func (s accessService) UpdateOidcIdentityMapping(
	ctx context.Context,
	req accessgen.UpdateOidcIdentityMappingRequestObject,
) (accessgen.UpdateOidcIdentityMappingResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	m, err := mappingBody(req.Body)
	if err != nil {
		return nil, err
	}
	if m, err = svc.UpdateMapping(ctx, req.ProviderName, req.MappingName, m); err != nil {
		return nil, s.oidcError(ctx, "update identity mapping", err)
	}
	out, err := recode[accessgen.UpdateOidcIdentityMapping200JSONResponse](m)
	if err != nil {
		return nil, s.oidcError(ctx, "update identity mapping", err)
	}
	return out, nil
}

func (s accessService) DeleteOidcIdentityMapping(
	ctx context.Context,
	req accessgen.DeleteOidcIdentityMappingRequestObject,
) (accessgen.DeleteOidcIdentityMappingResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.DeleteMapping(ctx, req.ProviderName, req.MappingName); err != nil {
		return nil, s.oidcError(ctx, "delete identity mapping", err)
	}
	return accessgen.DeleteOidcIdentityMapping204Response{}, nil
}

// OidcTokenExchange issues an access token for a provider's ID token
// (RFC 8693). It answers without credentials; the token service must be
// configured, as for the token API.
func (s accessService) OidcTokenExchange(
	ctx context.Context,
	req accessgen.OidcTokenExchangeRequestObject,
) (accessgen.OidcTokenExchangeResponseObject, error) {
	if s.cfg.Tokens == nil {
		return nil, apierror.ErrNotImplemented
	}
	svc, err := s.oidc()
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("a body with the token exchange is required")
	}
	got, err := svc.Exchange(ctx, oidc.ExchangeRequest{
		GrantType: deref(b.GrantType), SubjectTokenType: deref(b.SubjectTokenType),
		SubjectToken: deref(b.SubjectToken), ProviderName: deref(b.ProviderName),
		ProjectKey: deref(b.ProjectKey), MappingName: deref(b.IdentityMappingName),
		IncludeReference: deref(b.IncludeReferenceToken),
		JobID:            deref(b.JobId), RunID: deref(b.RunId), Repo: deref(b.Repo),
		Revision: deref(b.Revision), Branch: deref(b.Branch),
	})
	if err != nil {
		return nil, s.oidcError(ctx, "OIDC token exchange", err)
	}
	expires := int(got.ExpiresIn)
	out := accessgen.OidcTokenExchange200JSONResponse{
		AccessToken:     &got.AccessToken,
		TokenType:       ptrTo("Bearer"),
		ExpiresIn:       &expires,
		IssuedTokenType: ptrTo(oidc.IssuedAccessToken),
		Username:        &got.Username,
		Scope:           ptrTo(got.Record.Scope.String()),
	}
	if got.ReferenceToken != "" {
		out.ReferenceToken = &got.ReferenceToken
	}
	return out, nil
}
