package configurationhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// urlBaseBodyLimit bounds PUT baseUrl's plain-text body. A URL longer than
// this would not fit the request lines of the links built on it.
const urlBaseBodyLimit = 8 << 10

// GetReverseProxyConfiguration answers administrators with the reverse
// proxy settings, and any other signed-in user with the Docker method and
// the ports (getreverseproxyconfiguration.md: "Non-admin users will get the
// proxy method and ports"). Stored settings are returned as posted. Without
// them it describes artifactweave itself, which is what the Docker access
// method needs (decision 0003): SUBDOMAIN when docker.subdomain_match is on
// and REPOPATHPREFIX otherwise, with serverName the configured base URL's
// host and, for subdomains, its wildcard. Whether the caller is an
// administrator is rechecked when the stored settings are read.
func (s SystemAPI) GetReverseProxyConfiguration(
	ctx context.Context,
	_ artgen.GetReverseProxyConfigurationRequestObject,
) (artgen.GetReverseProxyConfigurationResponseObject, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("reverse proxy: %w", err)
	}
	admin := p.Admin
	body := s.reverseProxy(ctx)
	if ws := s.deps.Configuration.WebServer; ws != nil {
		view, err := ws.ReverseProxy(ctx)
		var typed *apierror.Error
		switch {
		case errors.As(err, &typed):
			return nil, err //nolint:wrapcheck // the authority refusal's own status
		case err != nil:
			return nil, s.internalError(ctx, "read reverse proxy configuration", err)
		}
		admin = view.Admin
		if view.Stored {
			body = storedReverseProxy(view.Settings)
		}
	}
	if !admin {
		return methodAndPorts(body), nil
	}
	return body, nil
}

// methodAndPorts is what a signed-in user who is not an administrator may
// read of the reverse proxy settings: how Docker clients address
// repositories and where the proxy listens, without the host names,
// contexts or certificate paths.
func methodAndPorts(
	c artgen.GetReverseProxyConfiguration200JSONResponse,
) artgen.GetReverseProxyConfiguration200JSONResponse {
	return artgen.GetReverseProxyConfiguration200JSONResponse{
		DockerReverseProxyMethod: c.DockerReverseProxyMethod,
		ArtifactoryPort:          c.ArtifactoryPort,
		HttpPort:                 c.HttpPort,
		HttpsPort:                c.HttpsPort,
		SslPort:                  c.SslPort,
		UseHttp:                  c.UseHttp,
		UseHttps:                 c.UseHttps,
	}
}

// storedReverseProxy is the JSON of stored settings. Every field is
// present, empty strings included: the platform_reverse_proxy resource reads
// an empty sslKey or sslCertificate as "not configured", and both port
// spellings carry sslPort because the resource reads httpsPort where the
// reference page documents sslPort.
func storedReverseProxy(rp configapi.ReverseProxy) artgen.GetReverseProxyConfiguration200JSONResponse {
	method := artgen.ReverseProxyConfigDockerReverseProxyMethod(rp.DockerReverseProxyMethod)
	kind := artgen.ReverseProxyConfigWebServerType(rp.WebServerType)
	return artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      &rp.Key,
		WebServerType:            &kind,
		ArtifactoryAppContext:    &rp.ArtifactoryAppContext,
		PublicAppContext:         &rp.PublicAppContext,
		ServerName:               &rp.ServerName,
		ServerNameExpression:     &rp.ServerNameExpression,
		ArtifactoryServerName:    &rp.ArtifactoryServerName,
		ArtifactoryPort:          &rp.ArtifactoryPort,
		SslCertificate:           &rp.SSLCertificate,
		SslKey:                   &rp.SSLKey,
		DockerReverseProxyMethod: &method,
		UseHttps:                 &rp.UseHTTPS,
		UseHttp:                  &rp.UseHTTP,
		SslPort:                  &rp.SSLPort,
		HttpsPort:                &rp.SSLPort,
		HttpPort:                 &rp.HTTPPort,
	}
}

// configuredBase is the effective configured base URL: the stored urlBase,
// else base_url. A router without the web server binding has only base_url.
func (s SystemAPI) configuredBase(ctx context.Context) string {
	if ws := s.deps.Configuration.WebServer; ws != nil {
		return ws.BaseURL(ctx)
	}
	return s.deps.BaseURL
}

// reverseProxy describes this server as the reverse proxy settings.
func (s SystemAPI) reverseProxy(
	ctx context.Context,
) artgen.GetReverseProxyConfiguration200JSONResponse {
	base := s.configuredBase(ctx)
	if base == "" {
		base = baseurl.From(ctx)
	}
	host := ""
	if u, err := url.Parse(base); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	method := artgen.REPOPATHPREFIX
	body := artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      httpkit.PtrTo("nginx"),
		WebServerType:            httpkit.PtrTo(artgen.NGINX),
		ArtifactoryAppContext:    httpkit.PtrTo("artifactory"),
		PublicAppContext:         httpkit.PtrTo("artifactory"),
		ServerName:               httpkit.PtrTo(host),
		DockerReverseProxyMethod: &method,
	}
	if s.deps.Settings.DockerSubdomainMatch {
		method = artgen.SUBDOMAIN
		body.ServerNameExpression = httpkit.PtrTo("*." + host)
	}
	return body
}

// UpdateReverseProxyConfiguration replaces the stored settings. Omitted
// fields take their descriptor defaults (configapi.ReverseProxyDefaults); sslPort
// and httpsPort are one setting, so a body that sends both must agree.
func (s SystemAPI) UpdateReverseProxyConfiguration(
	ctx context.Context,
	req artgen.UpdateReverseProxyConfigurationRequestObject,
) (artgen.UpdateReverseProxyConfigurationResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err //nolint:wrapcheck // the authority refusal's own status
	}
	ws := s.deps.Configuration.WebServer
	if ws == nil {
		return nil, apierror.ErrNotImplemented
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("the reverse proxy configuration is missing")
	}
	b := req.Body
	if b.SslPort != nil && b.HttpsPort != nil && *b.SslPort != *b.HttpsPort {
		return nil, httpkit.BadRequestf("sslPort and httpsPort must agree")
	}
	rp := configapi.ReverseProxyDefaults()
	for dst, src := range map[*string]*string{
		&rp.Key: b.Key, &rp.ArtifactoryAppContext: b.ArtifactoryAppContext,
		&rp.PublicAppContext: b.PublicAppContext, &rp.ServerName: b.ServerName,
		&rp.ServerNameExpression: b.ServerNameExpression, &rp.ArtifactoryServerName: b.ArtifactoryServerName,
		&rp.SSLCertificate: b.SslCertificate, &rp.SSLKey: b.SslKey,
	} {
		if src != nil {
			*dst = *src
		}
	}
	for dst, src := range map[*int]*int{
		&rp.ArtifactoryPort: b.ArtifactoryPort, &rp.HTTPPort: b.HttpPort, &rp.SSLPort: b.SslPort,
	} {
		if src != nil {
			*dst = *src
		}
	}
	if b.HttpsPort != nil {
		rp.SSLPort = *b.HttpsPort
	}
	if b.UseHttp != nil {
		rp.UseHTTP = *b.UseHttp
	}
	if b.UseHttps != nil {
		rp.UseHTTPS = *b.UseHttps
	}
	if b.WebServerType != nil {
		rp.WebServerType = string(*b.WebServerType)
	}
	if b.DockerReverseProxyMethod != nil {
		rp.DockerReverseProxyMethod = string(*b.DockerReverseProxyMethod)
	}
	if err := ws.SetReverseProxy(ctx, rp); err != nil {
		return nil, s.writeError(ctx, "store reverse proxy configuration", err)
	}
	return artgen.UpdateReverseProxyConfiguration200Response{}, nil
}

// GetReverseProxySnippet renders the stored NGINX settings.
func (s SystemAPI) GetReverseProxySnippet(
	ctx context.Context,
	_ artgen.GetReverseProxySnippetRequestObject,
) (artgen.GetReverseProxySnippetResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err //nolint:wrapcheck // the authority refusal's own status
	}
	ws := s.deps.Configuration.WebServer
	if ws == nil {
		return nil, apierror.ErrNotImplemented
	}
	snippet, err := ws.NginxSnippet(ctx)
	if err != nil {
		return nil, s.writeError(ctx, "render reverse proxy snippet", err)
	}
	return artgen.GetReverseProxySnippet200TextResponse(snippet), nil
}

// PutBaseURL is PUT /api/system/configuration/baseUrl. It is hand-written
// because its body is plain text. The answer is the reference page's
// example sentence; 6.20's ConfigResource also ends it with a newline,
// which the 7.x example does not show.
func (s SystemAPI) PutBaseURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := httpkit.AdminOnly(ctx); err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	ws := s.deps.Configuration.WebServer
	if ws == nil {
		httpkit.WriteErr(w, r, apierror.ErrNotImplemented)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, urlBaseBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpkit.WriteErr(w, r, apierror.New(http.StatusRequestEntityTooLarge, "request body is too large"))
			return
		}
		httpkit.WriteErr(w, r, httpkit.BadRequestf("Cannot read the request body"))
		return
	}
	stored, err := ws.SetURLBase(ctx, string(body))
	if err != nil {
		httpkit.WriteErr(w, r, s.writeError(ctx, "store URL base", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "URL base has been successfully updated to \"%s\".", stored)
}

// writeError answers a refusal with 400 and its reason, an authority
// refusal with its own status, and anything else with a 500 that names
// nothing internal.
func (s SystemAPI) writeError(ctx context.Context, op string, err error) error {
	var typed *apierror.Error
	switch {
	case errors.Is(err, configapi.ErrInvalid):
		reason := strings.TrimPrefix(err.Error(), configapi.ErrInvalid.Error()+": ")
		return httpkit.BadRequestf("%s", reason)
	case errors.As(err, &typed):
		return err //nolint:wrapcheck // the refusal's own status
	default:
		return s.internalError(ctx, op, err)
	}
}
