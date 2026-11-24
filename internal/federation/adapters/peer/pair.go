package peer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// PairService is the issuing side of pairing that the peer API's /pair
// operations call (pairing.Service).
type PairService interface {
	Accept(ctx context.Context, req pairing.Request, self string) (pairing.Answer, error)
	Drop(ctx context.Context, instanceID string, use model.UseCase) error
}

// PairServer serves POST and DELETE /artifactweave/peer/v1/pair (decision
// 0021, "Wire contract"). Server.Middleware checks each request's
// signature before it gets here, a redeem's with the key its body carries
// and a cancel's with the key recorded for its keyid, and puts the signer
// in the context (corepeer.Caller).
type PairServer struct {
	Pairing PairService
	// FederatedBase returns federatedRepoUrlBase, "" when it is not set;
	// when set, the issuer answers with it as its base URL rather than the
	// request's, as the token it issued carries it. Nil uses the request's.
	FederatedBase func(ctx context.Context) (string, error)
}

const notSigned = "The request is not signed by the instance it names"

// RedeemPairing redeems a pairing token's nonce for the calling instance.
func (s PairServer) RedeemPairing(
	ctx context.Context,
	req peergen.RedeemPairingRequestObject,
) (peergen.RedeemPairingResponseObject, error) {
	if req.Body == nil {
		return nil, apierror.New(http.StatusBadRequest, "The body is required")
	}
	key, err := base64.StdEncoding.DecodeString(req.Body.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, apierror.New(http.StatusBadRequest, "public_key must be 32 bytes of base64")
	}
	// The middleware verified the signature with the identity this body
	// presents; a request that reached here any other way is not signed by
	// the instance it would record.
	caller, ok := corepeer.Caller(ctx)
	if !ok || caller.InstanceID != req.Body.InstanceId || !ed25519.PublicKey(key).Equal(caller.PublicKey) {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	self := baseurl.From(ctx)
	if s.FederatedBase != nil {
		base, err := s.FederatedBase(ctx)
		if err != nil {
			return nil, err //nolint:wrapcheck // the strict error handler answers 500 and logs the cause
		}
		if base != "" {
			self = base
		}
	}
	ans, err := s.Pairing.Accept(ctx, pairing.Request{
		Nonce: req.Body.Nonce, InstanceID: req.Body.InstanceId, BaseURL: req.Body.BaseUrl,
		PublicKey: key, ProtocolVersion: req.Body.ProtocolVersion,
	}, self)
	switch {
	case errors.Is(err, pairing.ErrInvalidRequest):
		return nil, apierror.New(http.StatusBadRequest,
			"The redeem request is malformed or speaks another protocol major version")
	case errors.Is(err, pairing.ErrNonceRefused):
		// One answer for an unknown, expired and spent nonce, so a caller
		// learns nothing about nonces it does not hold.
		return nil, apierror.New(http.StatusForbidden, "The pairing nonce is not valid")
	case err != nil:
		return nil, err //nolint:wrapcheck // the strict error handler answers 500 and logs the cause
	}
	return peergen.RedeemPairing200JSONResponse{
		InstanceId: ans.InstanceID, BaseUrl: ans.BaseURL,
		PublicKey:       base64.StdEncoding.EncodeToString(ans.PublicKey),
		ProtocolVersion: ans.ProtocolVersion, UseCase: peergen.UseCase(ans.UseCase),
	}, nil
}

// CancelPairing drops the use case from the calling peer's record. It
// answers 204 whether or not the caller held it.
func (s PairServer) CancelPairing(
	ctx context.Context,
	req peergen.CancelPairingRequestObject,
) (peergen.CancelPairingResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	use := model.UseCase(req.Params.UseCase)
	if model.PairingFor(string(use)) != model.PairingServed {
		return nil, apierror.New(http.StatusBadRequest, "use_case must be federated-repo/* or federated-repo/r")
	}
	if err := s.Pairing.Drop(ctx, caller.InstanceID, use); err != nil {
		return nil, err //nolint:wrapcheck // the strict error handler answers 500 and logs the cause
	}
	return peergen.CancelPairing204Response{}, nil
}
