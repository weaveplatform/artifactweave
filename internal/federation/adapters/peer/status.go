package peer

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// StatusService answers a verified peer's status read (pairing.Service).
type StatusService interface {
	Describe(ctx context.Context, caller model.Peer, version string) (pairing.PeerStatus, error)
}

// StatusServer serves GET /artifactweave/peer/v1/status (#1949): the
// signed liveness and version read behind a peer's status and clock skew
// (decision 0021, Wire contract). Server.Middleware has verified the
// caller and signs the answer.
type StatusServer struct {
	Status StatusService
	// Version is this instance's release version.
	Version string
}

// GetPeerStatus answers this instance's identity, versions and clock, and
// the use cases the caller holds here.
func (s StatusServer) GetPeerStatus(
	ctx context.Context,
	_ peergen.GetPeerStatusRequestObject,
) (peergen.GetPeerStatusResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	st, err := s.Status.Describe(ctx, caller, s.Version)
	if err != nil {
		return nil, err //nolint:wrapcheck // the strict error handler answers 500 and logs the cause
	}
	uses := make([]peergen.UseCase, 0, len(st.UseCases))
	for _, u := range st.UseCases {
		uses = append(uses, peergen.UseCase(u))
	}
	return peergen.GetPeerStatus200JSONResponse{
		InstanceId: st.InstanceID, ProtocolVersion: st.ProtocolVersion, Version: st.Version,
		Time: st.Time, UseCases: uses,
	}, nil
}

// StatusRemote is the pairing service's status read (pairing.StatusRemote):
// the signed peer client calling a peer's GET /status, whose answer is
// verified with the key recorded for the peer.
type StatusRemote struct{ Factory *Factory }

var _ pairing.StatusRemote = StatusRemote{}

// Status reads peer's status. An answer that carries a signature the
// recorded key does not verify is pairing.ErrUnverified; so is the peer's
// 401, which it cannot sign because it did not verify this instance: in
// either direction the trust no longer holds. Any other answer without a
// usable signature, an intermediary's 502 say, is only its status.
func (r StatusRemote) Status(ctx context.Context, peer model.Peer) (pairing.PeerStatus, error) {
	c, err := r.Factory.Client(ctx, peer)
	if err != nil {
		return pairing.PeerStatus{}, fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
	defer c.Close()
	api, err := c.API()
	if err != nil {
		return pairing.PeerStatus{}, fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
	resp, err := api.GetPeerStatusWithResponse(ctx, &peerclient.GetPeerStatusParams{})
	var unverified *UnverifiedAnswerError
	switch {
	case errors.As(err, &unverified) && errors.Is(err, corepeer.ErrUnsigned) &&
		unverified.Status != http.StatusUnauthorized:
		return pairing.PeerStatus{}, &pairing.StatusError{Status: unverified.Status}
	case err != nil:
		return pairing.PeerStatus{}, callError(err, false)
	case resp.StatusCode() != http.StatusOK || resp.JSON200 == nil:
		return pairing.PeerStatus{}, &pairing.StatusError{Status: resp.StatusCode()}
	}
	uses := make([]model.UseCase, 0, len(resp.JSON200.UseCases))
	for _, u := range resp.JSON200.UseCases {
		uses = append(uses, model.UseCase(u))
	}
	return pairing.PeerStatus{
		InstanceID: resp.JSON200.InstanceId, ProtocolVersion: resp.JSON200.ProtocolVersion,
		Version: resp.JSON200.Version, Time: resp.JSON200.Time, UseCases: uses,
	}, nil
}
