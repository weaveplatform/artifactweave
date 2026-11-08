package app

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
)

// The format slices' tests install the hooks on their own fixtures, so only
// this composition shows a hook the application stopped installing. A policy
// without it still serves every request, only differently: a NuGet v2 204
// would no longer be an empty feed.
func TestAccRemoteWiringInstallsFormatHooks(t *testing.T) {
	w := newRemoteWorld(t)
	policies := map[string]*remote.Policy{
		"remote":   w.rt.remoteSources.HTTP.(*maven.MavenBackend).Policy,
		"registry": w.rt.registrySources.HTTP.(*remote.RegistryContent).Policy,
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
}
