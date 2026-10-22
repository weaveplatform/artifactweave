package remote

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func (p *Policy) fetchResponse(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	request Request,
) (*http.Response, error) {
	if p.Fetch != nil {
		return p.Fetch(ctx, q, permit, opts, request)
	}
	client, err := p.Pool.Client(q.Repo, opts)
	if err != nil {
		return nil, err
	}
	return client.Fetch(ctx, request)
}
