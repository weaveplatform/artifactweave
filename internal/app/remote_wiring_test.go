package app

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
)

// The format slices' tests install the hooks on their own fixtures, so only
// this composition shows a hook the application stopped installing. A policy
// without it still serves every request, only differently: a NuGet v2 204
// would no longer be an empty feed, and without the registry path parser
// Smart Remote would stop synchronising registry properties.
func TestAccRemoteWiringInstallsFormatHooks(t *testing.T) {
	w := newRemoteWorld(t)
	content := w.rt.registrySources.HTTP.(*docker.RegistryContent)
	policies := map[string]*remote.Policy{
		"remote":   w.rt.remoteSources.HTTP.(*maven.MavenBackend).Policy,
		"registry": content.Policy,
	}
	for name, p := range policies {
		res := &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     http.Header{},
			Body:       http.NoBody,
		}
		q := source.Request{Resource: source.Resource{NuGetEmptyFeed: true}}
		if p.ProjectResponse == nil || p.ProjectResponse(q, res) != nil ||
			res.StatusCode != http.StatusOK {
			t.Fatalf("%s policy does not project the NuGet empty feed", name)
		}
	}
	clients, ok := content.Registries.(*upstream.RegistryClients)
	if !ok || clients.Pool != &w.rt.registryHTTP || clients.Options == nil {
		t.Fatal("registry content does not open the runtime's registry clients")
	}
	if w.rt.registryPath == nil {
		t.Fatal("no registry path parser for Smart Remote")
	}
	image, operation, reference, err := w.rt.registryPath("library/alpine/manifests/latest")
	if err != nil || image != "library/alpine" || operation != "manifests" ||
		reference != "latest" {
		t.Fatalf("registry path: %q %q %q %v", image, operation, reference, err)
	}
}
