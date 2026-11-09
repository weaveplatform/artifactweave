package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
)

// TestAccMethodAndPorts: a user who is no administrator keeps the Docker
// method and every port field, and loses the host names, contexts and
// certificate paths.
func TestAccMethodAndPorts(t *testing.T) {
	method := artgen.SUBDOMAIN
	full := artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      PtrTo("nginx"),
		WebServerType:            PtrTo(artgen.NGINX),
		ArtifactoryAppContext:    PtrTo("artifactory"),
		PublicAppContext:         PtrTo("artifactory"),
		ServerName:               PtrTo("reg.example.com"),
		ServerNameExpression:     PtrTo("*.reg.example.com"),
		ArtifactoryServerName:    PtrTo("localhost"),
		SslCertificate:           PtrTo("/etc/ssl/reg.crt"),
		SslKey:                   PtrTo("/etc/ssl/reg.key"),
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          PtrTo(8081),
		HttpPort:                 PtrTo(80),
		HttpsPort:                PtrTo(443),
		SslPort:                  PtrTo(443),
		UseHttp:                  PtrTo(true),
		UseHttps:                 PtrTo(true),
	}
	want := artgen.GetReverseProxyConfiguration200JSONResponse{
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          PtrTo(8081),
		HttpPort:                 PtrTo(80),
		HttpsPort:                PtrTo(443),
		SslPort:                  PtrTo(443),
		UseHttp:                  PtrTo(true),
		UseHttps:                 PtrTo(true),
	}
	if diff := cmp.Diff(want, methodAndPorts(full)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
