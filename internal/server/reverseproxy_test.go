package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
)

// TestMethodAndPorts: a user who is no administrator keeps the Docker
// method and every port field, and loses the host names, contexts and
// certificate paths.
func TestMethodAndPorts(t *testing.T) {
	method := artgen.SUBDOMAIN
	full := artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      ptrTo("nginx"),
		WebServerType:            ptrTo(artgen.NGINX),
		ArtifactoryAppContext:    ptrTo("artifactory"),
		PublicAppContext:         ptrTo("artifactory"),
		ServerName:               ptrTo("reg.example.com"),
		ServerNameExpression:     ptrTo("*.reg.example.com"),
		ArtifactoryServerName:    ptrTo("localhost"),
		SslCertificate:           ptrTo("/etc/ssl/reg.crt"),
		SslKey:                   ptrTo("/etc/ssl/reg.key"),
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          ptrTo(8081),
		HttpPort:                 ptrTo(80),
		HttpsPort:                ptrTo(443),
		SslPort:                  ptrTo(443),
		UseHttp:                  ptrTo(true),
		UseHttps:                 ptrTo(true),
	}
	want := artgen.GetReverseProxyConfiguration200JSONResponse{
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          ptrTo(8081),
		HttpPort:                 ptrTo(80),
		HttpsPort:                ptrTo(443),
		SslPort:                  ptrTo(443),
		UseHttp:                  ptrTo(true),
		UseHttps:                 ptrTo(true),
	}
	if diff := cmp.Diff(want, methodAndPorts(full)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
