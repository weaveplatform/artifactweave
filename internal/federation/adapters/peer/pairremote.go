package peer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// PairRemote is the pairing service's transport (pairing.Remote): the
// signed peer client calling another instance's POST and DELETE /pair.
type PairRemote struct{ Factory *Factory }

var _ pairing.Remote = PairRemote{}

// Pair redeems req's nonce at the issuer's base URL. The issuer is not a
// peer yet, so its answer is verified with the key the answer presents
// (PairAnswerKey); the pairing service then holds that key to the token's
// fingerprint, which is what ties the answer to the token's issuer.
func (r PairRemote) Pair(ctx context.Context, baseURL string, req pairing.Request) (pairing.Answer, error) {
	c, err := r.Factory.Client(ctx, model.Peer{BaseURL: baseURL})
	if err != nil {
		// A base URL the client cannot call is one no answer comes from,
		// 7.x's network failure.
		return pairing.Answer{}, fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
	defer c.Close()
	api, err := c.WithAnswerKey(PairAnswerKey).API()
	if err != nil {
		return pairing.Answer{}, fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
	resp, err := api.RedeemPairingWithResponse(ctx, &peerclient.RedeemPairingParams{}, peerclient.PairRequest{
		Nonce: req.Nonce, InstanceId: req.InstanceID, BaseUrl: req.BaseURL,
		PublicKey:       base64.StdEncoding.EncodeToString(req.PublicKey),
		ProtocolVersion: req.ProtocolVersion,
	})
	if err != nil {
		return pairing.Answer{}, callError(err, true)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return pairing.Answer{}, &pairing.StatusError{Status: resp.StatusCode()}
	}
	// PairAnswerKey has decoded this key already to verify the answer.
	key, _ := base64.StdEncoding.DecodeString(resp.JSON200.PublicKey)
	return pairing.Answer{
		InstanceID: resp.JSON200.InstanceId, BaseURL: resp.JSON200.BaseUrl,
		PublicKey: ed25519.PublicKey(key), ProtocolVersion: resp.JSON200.ProtocolVersion,
		UseCase: model.UseCase(resp.JSON200.UseCase),
	}, nil
}

// Unpair tells peer to drop use from its record of this instance. Its
// answer is verified with the key recorded for peer.
func (r PairRemote) Unpair(ctx context.Context, peer model.Peer, use model.UseCase) error {
	c, err := r.Factory.Client(ctx, peer)
	if err != nil {
		return fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
	defer c.Close()
	api, err := c.API()
	if err != nil {
		return fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
	resp, err := api.CancelPairingWithResponse(ctx, &peerclient.CancelPairingParams{UseCase: peerclient.UseCase(use)})
	if err != nil {
		return callError(err, false)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return &pairing.StatusError{Status: resp.StatusCode()}
	}
	return nil
}

// callError sorts a failed call into the pairing service's errors. An
// answer that does not verify is a refusal when it is one: an issuer's
// 403 or 400 to a redeem carries no identity to verify it with, so only
// its status is known (pairing answers a status 7.x's way); a 2xx that
// does not verify is a forged or tampered answer. A call that got no
// answer at all is unreachable.
func callError(err error, redeem bool) error {
	var unverified *UnverifiedAnswerError
	switch {
	case errors.As(err, &unverified):
		if redeem && (unverified.Status < 200 || unverified.Status > 299) {
			return &pairing.StatusError{Status: unverified.Status}
		}
		return fmt.Errorf("%w: %w", pairing.ErrUnverified, err)
	case errors.Is(err, outbound.ErrConfiguration), errors.Is(err, ErrTooLarge):
		return fmt.Errorf("peer call: %w", err)
	default:
		return fmt.Errorf("%w: %w", pairing.ErrUnreachable, err)
	}
}
