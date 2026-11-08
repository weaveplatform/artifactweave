package remote

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrRegistryDenied is what a registry client returns when the upstream
// registry still refuses a request after the one authentication retry Do
// allows, or answers it with a challenge the client may not follow: one from
// another origin, or Basic after the configured credentials were sent. The
// Docker remote rules answer it as an upstream 401.
var ErrRegistryDenied = errors.New("remote: upstream registry authentication denied")

// RegistryClient sends Distribution requests to one upstream registry for one
// resolved repository revision, and authenticates them itself (decision 0007,
// docs/implementation/remote-registry-authentication.md). *Registry
// implements it.
type RegistryClient interface {
	// Image returns the name the upstream knows the image name by, with the
	// repository's project prefix and Docker Hub's library namespace, or
	// ErrRequest for a name outside the Distribution grammar or over 255
	// characters.
	Image(name string) (string, error)
	// Do sends r for image; r.Path is relative to the image, such as
	// "manifests/latest". The caller owns the response body.
	// ErrRegistryDenied reports a request the registry refused.
	Do(ctx context.Context, image string, r Request) (*http.Response, error)
	// Base returns a copy of the registry's base URL, under which a
	// listing's next-page link must stay.
	Base() *url.URL
}

// Registries opens registry clients for the Docker remote rules, so that the
// rules hold neither the client pool nor the credential resolver, which live
// as long as the application. RegistryClients implements it.
type Registries interface {
	// Open returns the pooled client, or source.ErrSuperseded when the resolved
	// credential revision differs from revision. On any error the client is
	// a nil interface.
	Open(
		ctx context.Context,
		c repo.Config,
		id types.SourceIdentity,
		revision string,
	) (RegistryClient, error)
	// Foreign sends r once through an unpooled client built from opts, for
	// a foreign layer, which a manifest names outside the registry.
	Foreign(ctx context.Context, c repo.Config, opts Options, r Request) (*http.Response, error)
}
