package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
)

// GetReverseProxyConfiguration answers administrators with the reverse
// proxy settings, and any other signed-in user with the Docker method and
// the ports (getreverseproxyconfiguration.md: "Non-admin users will get the
// proxy method and ports"). Until the settings can be stored (P12) it
// describes artifactweave itself, which is what the Docker access method
// needs (decision 0003): SUBDOMAIN when docker.subdomain_match is on and
// REPOPATHPREFIX otherwise, with serverName the base URL's host and, for
// subdomains, its wildcard.
func (s artifactoryService) GetReverseProxyConfiguration(
	ctx context.Context,
	_ artgen.GetReverseProxyConfigurationRequestObject,
) (artgen.GetReverseProxyConfigurationResponseObject, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("reverse proxy: %w", err)
	}
	body := s.reverseProxy(ctx)
	if !p.Admin {
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

// reverseProxy describes this server as the reverse proxy settings.
func (s artifactoryService) reverseProxy(
	ctx context.Context,
) artgen.GetReverseProxyConfiguration200JSONResponse {
	base := s.cfg.BaseURL
	if base == "" {
		base = baseurl.From(ctx)
	}
	host := ""
	if u, err := url.Parse(base); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	method := artgen.REPOPATHPREFIX
	body := artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      ptrTo("nginx"),
		WebServerType:            ptrTo(artgen.NGINX),
		ArtifactoryAppContext:    ptrTo("artifactory"),
		PublicAppContext:         ptrTo("artifactory"),
		ServerName:               ptrTo(host),
		DockerReverseProxyMethod: &method,
	}
	if s.cfg.DockerSubdomainMatch {
		method = artgen.SUBDOMAIN
		body.ServerNameExpression = ptrTo("*." + host)
	}
	return body
}
