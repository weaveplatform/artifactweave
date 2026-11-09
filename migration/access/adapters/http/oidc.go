package accesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/service/oidc"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	accessgen "github.com/weaveplatform/artifactweave/migration/platform/gen/access"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// The OIDC integration (/access/api/v1/oidc, issue #83, research 21):
// providers and identity mappings are admin only; the token exchange needs
// no credentials, because the subject token is the credential.

// oidcTokenRoute is the exchange, which the middleware lets through without
// credentials.
const oidcTokenRoute = "POST /access/api/v1/oidc/token"

// oidcService checks the caller is an administrator and returns the service.
func (s AccessService) oidcService(ctx context.Context) (oidc.Service, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return oidc.Service{}, err
	}
	return s.oidc()
}

func (s AccessService) oidc() (oidc.Service, error) {
	st, err := s.content()
	if err != nil {
		return oidc.Service{}, err
	}
	return oidc.Service{Store: st, Tokens: s.deps.Tokens, Keys: s.deps.IDTokens}, nil
}

// oidcError maps the service's errors to statuses. A provider whose keys
// cannot be fetched still answers 401, the status the operation declares for
// a token that does not verify, and is logged so an operator sees why.
func (s AccessService) oidcError(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, kernel.ErrAPIRestricted):
		return kernel.ErrAPIRestricted
	case errors.Is(err, oidc.ErrNotFound):
		return apierror.New(http.StatusNotFound, err.Error())
	case errors.Is(err, oidc.ErrInvalid), errors.Is(err, oidc.ErrNoMatch):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, oidc.ErrUnverified):
		if errors.Is(err, idtoken.ErrKeys) {
			s.log().WarnContext(ctx, op,
				"request_id", httpkit.RequestIDFrom(ctx), "error", err)
		}
		return apierror.New(http.StatusUnauthorized, err.Error())
	default:
		return s.internalError(ctx, op, err)
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
		return oidc.Provider{}, httpkit.BadRequestf("a body with the OIDC provider is required")
	}
	return recode[oidc.Provider](b)
}

func mappingBody(b *accessgen.OidcIdentityMapping) (oidc.Mapping, error) {
	if b == nil {
		return oidc.Mapping{}, httpkit.BadRequestf("a body with the identity mapping is required")
	}
	return recode[oidc.Mapping](b)
}

func (s AccessService) GetAllOidcConfigurations(
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
func (s AccessService) CreateOidcConfiguration(
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

func (s AccessService) GetOidcConfiguration(
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

func (s AccessService) UpdateOidcConfiguration(
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

func (s AccessService) DeleteOidcConfiguration(
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

func (s AccessService) GetOidcIdentityMappings(
	ctx context.Context,
	req accessgen.GetOidcIdentityMappingsRequestObject,
) (accessgen.GetOidcIdentityMappingsResponseObject, error) {
	svc, err := s.oidcService(ctx)
	if err != nil {
		return nil, err
	}
	all, err := svc.Mappings(ctx, req.ProviderName, httpkit.Deref(req.Params.ProjectKey))
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

func (s AccessService) CreateOidcIdentityMapping(
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

func (s AccessService) GetOidcIdentityMappingByName(
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

func (s AccessService) UpdateOidcIdentityMapping(
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

func (s AccessService) DeleteOidcIdentityMapping(
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
func (s AccessService) OidcTokenExchange(
	ctx context.Context,
	req accessgen.OidcTokenExchangeRequestObject,
) (accessgen.OidcTokenExchangeResponseObject, error) {
	if s.deps.Tokens == nil {
		return nil, apierror.ErrNotImplemented
	}
	svc, err := s.oidc()
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil {
		return nil, httpkit.BadRequestf("a body with the token exchange is required")
	}
	got, err := svc.Exchange(ctx, oidc.ExchangeRequest{
		GrantType:        httpkit.Deref(b.GrantType),
		SubjectTokenType: httpkit.Deref(b.SubjectTokenType),
		SubjectToken:     httpkit.Deref(b.SubjectToken),
		ProviderName:     httpkit.Deref(b.ProviderName),
		ProjectKey:       httpkit.Deref(b.ProjectKey),
		MappingName:      httpkit.Deref(b.IdentityMappingName),
		IncludeReference: httpkit.Deref(b.IncludeReferenceToken),
		JobID: httpkit.Deref(
			b.JobId,
		),
		RunID:    httpkit.Deref(b.RunId),
		Repo:     httpkit.Deref(b.Repo),
		Revision: httpkit.Deref(b.Revision),
		Branch:   httpkit.Deref(b.Branch),
	})
	if err != nil {
		return nil, s.oidcError(ctx, "OIDC token exchange", err)
	}
	expires := int(got.ExpiresIn)
	out := accessgen.OidcTokenExchange200JSONResponse{
		AccessToken:     &got.AccessToken,
		TokenType:       httpkit.PtrTo("Bearer"),
		ExpiresIn:       &expires,
		IssuedTokenType: httpkit.PtrTo(oidc.IssuedAccessToken),
		Username:        &got.Username,
		Scope:           httpkit.PtrTo(got.Record.Scope.String()),
	}
	if got.ReferenceToken != "" {
		out.ReferenceToken = &got.ReferenceToken
	}
	return out, nil
}
