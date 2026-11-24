package peer

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// ConfigRemote is configSync's transport (federated.Remote): the signed
// peer client calling a member's PUT /repositories/{repoKey}.
type ConfigRemote struct{ Factory *Factory }

var _ federated.Remote = ConfigRemote{}

// PutConfiguration sends cfg for the member's repository remoteKey. Its
// answer is verified with the key recorded for peer. No verified answer,
// whether the network failed or an answer did not verify, is
// federated.ErrUnavailable; a verified status other than 204 is the
// member's refusal.
func (r ConfigRemote) PutConfiguration(
	ctx context.Context,
	peer model.Peer,
	remoteKey, stream string,
	cfg map[string]any,
) error {
	c, err := r.Factory.Client(ctx, peer)
	if err != nil {
		return fmt.Errorf("%w: %w", federated.ErrUnavailable, err)
	}
	defer c.Close()
	api, err := c.API()
	if err != nil {
		return fmt.Errorf("%w: %w", federated.ErrUnavailable, err)
	}
	resp, err := api.PutRepositoryConfigurationWithResponse(ctx, remoteKey, &peerclient.PutRepositoryConfigurationParams{},
		peerclient.RepositoryConfiguration{Stream: stream, Configuration: cfg})
	if err != nil {
		if errors.Is(err, outbound.ErrConfiguration) || errors.Is(err, ErrTooLarge) {
			return fmt.Errorf("peer call: %w", err)
		}
		return fmt.Errorf("%w: %w", federated.ErrUnavailable, err)
	}
	if resp.StatusCode() == http.StatusNoContent {
		return nil
	}
	refusal := &federated.RemoteRefusal{Status: resp.StatusCode()}
	for _, e := range []*peerclient.ErrorResponse{resp.JSON400, resp.JSON403, resp.JSON409} {
		if e != nil {
			refusal.Message = e.Message
		}
	}
	return refusal
}

// RequestResync asks the member to resynchronise stream from this
// instance with a full sync, forced or not (POST /streams/{stream}/resync).
// No verified answer is federated.ErrUnavailable, as PutConfiguration's;
// a verified status other than 204 is the member's refusal.
func (r ConfigRemote) RequestResync(ctx context.Context, peer model.Peer, stream string, forced bool) error {
	c, err := r.Factory.Client(ctx, peer)
	if err != nil {
		return fmt.Errorf("%w: %w", federated.ErrUnavailable, err)
	}
	defer c.Close()
	api, err := c.API()
	if err != nil {
		return fmt.Errorf("%w: %w", federated.ErrUnavailable, err)
	}
	resp, err := api.RequestResyncWithResponse(ctx, stream, &peerclient.RequestResyncParams{},
		peerclient.ResyncRequest{Forced: forced})
	if err != nil {
		if errors.Is(err, outbound.ErrConfiguration) || errors.Is(err, ErrTooLarge) {
			return fmt.Errorf("peer call: %w", err)
		}
		return fmt.Errorf("%w: %w", federated.ErrUnavailable, err)
	}
	if resp.StatusCode() == http.StatusNoContent {
		return nil
	}
	refusal := &federated.RemoteRefusal{Status: resp.StatusCode()}
	for _, e := range []*peerclient.ErrorResponse{resp.JSON400, resp.JSON403, resp.JSON404} {
		if e != nil {
			refusal.Message = e.Message
		}
	}
	return refusal
}
