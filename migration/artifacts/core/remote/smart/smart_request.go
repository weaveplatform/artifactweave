package smart

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

// Peer management requests reuse resolved security and pooled transports, but
// never follow redirects. A storage API reply from another repository/authority
// cannot replace the peer selected by discovery, even with allowAnyHostAuth.
// PUT is confined to this private statistics call; Client.Do remains read-only.
func (s *Smart) request(
	ctx context.Context, q source.Request, permit source.Permit, opts remote.Options,
	method, target string, body []byte,
) (*http.Response, error) {
	if method != http.MethodGet && method != http.MethodPut && method != http.MethodPost ||
		len(body) > 64<<10 {
		return nil, remote.ErrRequest
	}
	peer, err := remote.ParseSmartPeer(q.Repo)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	u, err := remote.CheckedURL(target)
	base, baseErr := remote.CheckedURL(peer.BaseURL)
	if err != nil || baseErr != nil || remote.Origin(u) != remote.Origin(base) {
		return nil, remote.ErrSecurity
	}
	if method == http.MethodPost && !validSmartPOST(q, peer, target, body) {
		return nil, remote.ErrRequest
	}
	if method == http.MethodPut {
		remotePeer := peer
		remotePeer.Class = repo.Remote
		if target != statisticsURL(peer) && target != statisticsURL(remotePeer) {
			return nil, remote.ErrSecurity
		}
	}
	if err = s.policy.Check(ctx, q, permit, opts.Revision); err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	state, err := s.policy.State(ctx, q, opts.Revision)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	if err = s.policy.Cooldown(state); err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	conn, err := s.pool.Connection(q.Repo, opts)
	if err != nil || !conn.Guarded() {
		return nil, source.ErrUnavailable
	}
	stateContext := ctx
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	metadata := q
	metadata.Resource.Kind = source.ResourceKindMetadata
	ctx, metadataCancel := remote.MetadataContext(ctx, metadata)
	outerCancel := cancel
	cancel = func() { metadataCancel(); outerCancel() }
	if err = conn.CheckURL(ctx, u); err != nil {
		cancel()
		return nil, remote.PolicyError(err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, remote.ErrRequest
	}
	// No automatic replay can be safe after a statistics submission may have
	// reached its peer. The durable counter was claimed before entering here.
	if method == http.MethodPut {
		req.GetBody = nil
	}
	req.Header.Set("Accept", "application/json")
	if err = conn.Prepare(req); err != nil {
		cancel()
		return nil, remote.PolicyError(err)
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "text/plain")
		req.GetBody = nil
	}
	smartHeaders(req)
	res, err := conn.Send(req)
	if err != nil {
		cancel()
		failure := remote.PolicyError(err)
		if stateErr := s.policy.RecordFailure(
			stateContext,
			q,
			permit,
			opts.Revision,
			failure,
		); stateErr != nil {
			//nolint:wrapcheck // preserves upstream error identity
			return nil, stateErr
		}
		return nil, failure
	}
	if err = s.policy.RecordResponse(ctx, q, permit, opts.Revision, res); err != nil {
		_ = res.Body.Close()
		cancel()
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	res.Body = &cancelBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

func validSmartPOST(q source.Request, peer remote.SmartPeer, target string, body []byte) bool {
	return q.Resource.SmartAQL != "" && target == peer.BaseURL+"/api/search/aql" &&
		string(body) == q.Resource.SmartAQL
}
