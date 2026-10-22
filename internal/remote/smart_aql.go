package remote

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// query deliberately has no persistent response/cache entry: search may disclose
// upstream-only files but must never manufacture items or cache-alias downloads.
func (s *SmartRemote) query(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	peer SmartPeer,
) (source.Result, error) {
	text := q.Resource.SmartAQL
	parsed, err := aql.Parse(text)
	if err != nil || parsed.Action != aql.Find || parsed.Transitive {
		return source.Result{}, source.ErrInvalidRequest
	}
	key, err := parsed.TransitiveTarget()
	if err != nil || key != peer.Repository || !parsed.HasLimit || parsed.Limit > 10001 ||
		peer.Class != repo.Local ||
		q.Resource.ExternalURL != peer.BaseURL+"/api/search/aql" {
		return source.Result{}, source.ErrInvalidRequest
	}
	opts, err := s.policy.Options(ctx, q.Repo)
	if err != nil || opts.Revision != peer.revision {
		return source.Result{}, source.ErrSuperseded
	}
	release, err := s.policy.capacity.acquire(ctx, q.Repo.Key)
	if err != nil {
		return source.Result{}, policyError(err)
	}
	defer release()
	res, err := s.request(
		ctx,
		q,
		permit,
		opts,
		http.MethodPost,
		q.Resource.ExternalURL,
		[]byte(text),
	)
	if err != nil {
		return source.Result{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return source.Result{}, responseError(res, s.policy.now())
	}
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return source.Result{}, source.ErrInvalidResult
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		return source.Result{}, source.ErrInvalidResult
	}
	metadata := source.MetadataResponse{BaseURL: q.Resource.ExternalURL}
	if err = q.Resource.ValidateMetadata(
		ctx,
		bytes.NewReader(body),
		int64(len(body)),
		metadata,
	); err != nil {
		return source.Result{}, source.ErrInvalidResult
	}
	if err = s.policy.check(ctx, q, permit, opts.Revision); err != nil {
		return source.Result{}, err
	}
	return source.Result{
		Metadata: metadata,
		Stream: &source.Stream{
			Body:   io.NopCloser(bytes.NewReader(body)),
			Status: http.StatusOK,
			Representation: source.Representation{
				MediaType: "application/json",
				Size:      int64(len(body)),
			},
		},
		Revalidate: func(current context.Context) error { return s.policy.check(current, q, permit, opts.Revision) },
	}, nil
}
