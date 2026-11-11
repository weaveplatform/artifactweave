package configurationhttp

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// TestAccMethodAndPorts: a user who is no administrator keeps the Docker
// method and every port field, and loses the host names, contexts and
// certificate paths.
func TestAccMethodAndPorts(t *testing.T) {
	method := artgen.SUBDOMAIN
	full := artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      httpkit.PtrTo("nginx"),
		WebServerType:            httpkit.PtrTo(artgen.NGINX),
		ArtifactoryAppContext:    httpkit.PtrTo("artifactory"),
		PublicAppContext:         httpkit.PtrTo("artifactory"),
		ServerName:               httpkit.PtrTo("reg.example.com"),
		ServerNameExpression:     httpkit.PtrTo("*.reg.example.com"),
		ArtifactoryServerName:    httpkit.PtrTo("localhost"),
		SslCertificate:           httpkit.PtrTo("/etc/ssl/reg.crt"),
		SslKey:                   httpkit.PtrTo("/etc/ssl/reg.key"),
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          httpkit.PtrTo(8081),
		HttpPort:                 httpkit.PtrTo(80),
		HttpsPort:                httpkit.PtrTo(443),
		SslPort:                  httpkit.PtrTo(443),
		UseHttp:                  httpkit.PtrTo(true),
		UseHttps:                 httpkit.PtrTo(true),
	}
	want := artgen.GetReverseProxyConfiguration200JSONResponse{
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          httpkit.PtrTo(8081),
		HttpPort:                 httpkit.PtrTo(80),
		HttpsPort:                httpkit.PtrTo(443),
		SslPort:                  httpkit.PtrTo(443),
		UseHttp:                  httpkit.PtrTo(true),
		UseHttps:                 httpkit.PtrTo(true),
	}
	if diff := cmp.Diff(want, methodAndPorts(full)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
