package remote

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var ErrRegistryDenied = errors.New("remote: upstream registry authentication denied")

// RegistryClient sends Distribution requests to one upstream registry.
type RegistryClient interface {
	Image(name string) (string, error)
	Do(ctx context.Context, image string, r Request) (*http.Response, error)
	Base() *url.URL
}

// Registries opens registry clients for the Docker remote rules.
type Registries interface {
	// Open returns the pooled client, or source.ErrSuperseded when the resolved
	// credential revision differs from revision.
	Open(
		ctx context.Context,
		c repo.Config,
		id types.SourceIdentity,
		revision string,
	) (RegistryClient, error)
	// Foreign sends r once through an unpooled client built from opts.
	Foreign(ctx context.Context, c repo.Config, opts Options, r Request) (*http.Response, error)
}
