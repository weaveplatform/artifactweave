package remote

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

func (p *Policy) fetchResponse(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	request Request,
) (*http.Response, error) {
	if q.Resource.CocoaPodsGit != nil {
		if p.GitFetch == nil {
			return nil, source.ErrUnsupported
		}
		return p.GitFetch(ctx, p, q, permit, opts)
	}
	if p.Fetch != nil {
		return p.Fetch(ctx, q, permit, opts, request)
	}
	conn, err := p.Pool.Connection(q.Repo, opts)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	//nolint:wrapcheck // preserves upstream error identity
	return conn.Fetch(ctx, request)
}
