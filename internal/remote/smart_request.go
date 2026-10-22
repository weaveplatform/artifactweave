package remote

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Peer management requests reuse resolved security and pooled transports, but
// never follow redirects. A storage API reply from another repository/authority
// cannot replace the peer selected by discovery, even with allowAnyHostAuth.
// PUT is confined to this private statistics call; Client.Do remains read-only.
func (s *SmartRemote) request(
	ctx context.Context, q source.Request, permit source.Permit, opts Options,
	method, target string, body []byte,
) (*http.Response, error) {
	if method != http.MethodGet && method != http.MethodPut && method != http.MethodPost ||
		len(body) > 64<<10 {
		return nil, ErrRequest
	}
	peer, err := ParseSmartPeer(q.Repo)
	if err != nil {
		return nil, err
	}
	u, err := checkedURL(target)
	base, baseErr := checkedURL(peer.BaseURL)
	if err != nil || baseErr != nil || origin(u) != origin(base) {
		return nil, ErrSecurity
	}
	if method == http.MethodPost && !validSmartPOST(q, peer, target, body) {
		return nil, ErrRequest
	}
	if method == http.MethodPut {
		remotePeer := peer
		remotePeer.Class = repo.Remote
		if target != peer.statisticsURL() && target != remotePeer.statisticsURL() {
			return nil, ErrSecurity
		}
	}
	if err = s.policy.check(ctx, q, permit, opts.Revision); err != nil {
		return nil, err
	}
	state, err := s.policy.state(ctx, q, opts.Revision)
	if err != nil {
		return nil, err
	}
	if err = s.policy.cooldown(state); err != nil {
		return nil, err
	}
	client, err := s.pool.Client(q.Repo, opts)
	if err != nil || client.checkURL == nil {
		return nil, source.ErrUnavailable
	}
	stateContext := ctx
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	metadata := q
	metadata.Resource.Kind = source.Metadata
	ctx, metadataCancel := metadataContext(ctx, metadata)
	outerCancel := cancel
	cancel = func() { metadataCancel(); outerCancel() }
	if err = client.checkURL(ctx, u); err != nil {
		cancel()
		return nil, policyError(err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, ErrRequest
	}
	// No automatic replay can be safe after a statistics submission may have
	// reached its peer. The durable counter was claimed before entering here.
	if method == http.MethodPut {
		req.GetBody = nil
	}
	req.Header.Set("Accept", "application/json")
	if client.prepare != nil {
		if err = client.prepare(req); err != nil {
			cancel()
			return nil, policyError(err)
		}
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "text/plain")
		req.GetBody = nil
	}
	smartHeaders(req)
	transport := *client.http
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrRedirect }
	res, err := transport.Do(req)
	if err != nil {
		cancel()
		failure := policyError(err)
		if stateErr := s.policy.recordFailure(
			stateContext,
			q,
			permit,
			opts.Revision,
			failure,
		); stateErr != nil {
			return nil, stateErr
		}
		return nil, failure
	}
	if err = s.policy.recordResponse(ctx, q, permit, opts.Revision, res); err != nil {
		_ = res.Body.Close()
		cancel()
		return nil, err
	}
	res.Body = &cancelBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

func validSmartPOST(q source.Request, peer SmartPeer, target string, body []byte) bool {
	return q.Resource.SmartAQL != "" && target == peer.BaseURL+"/api/search/aql" &&
		string(body) == q.Resource.SmartAQL
}
