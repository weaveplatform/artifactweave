package server

import (
	"context"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
)

// GetReverseProxyConfiguration answers administrators with the reverse
// proxy settings. Until they can be stored (P12) it describes artifactweave
// itself, which is what the Docker access method needs (decision 0003):
// SUBDOMAIN when docker.subdomain_match is on and REPOPATHPREFIX otherwise,
// with serverName the base URL's host and, for subdomains, its wildcard.
func (s artifactoryService) GetReverseProxyConfiguration(
	ctx context.Context,
	_ artgen.GetReverseProxyConfigurationRequestObject,
) (artgen.GetReverseProxyConfigurationResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
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
	return body, nil
}
