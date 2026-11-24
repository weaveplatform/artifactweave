package pairing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Errors the redeem call to the issuer (Remote.Pair) and the cancel call
// to a peer (Remote.Unpair) report, which decide the 7.x message the
// administrator gets.
var (
	// ErrUnreachable reports a call that got no answer: the network, a
	// timeout or TLS.
	ErrUnreachable = errors.New("peer unreachable")
	// ErrUnverified reports an answer whose signature does not verify with
	// the key the peer presented or the one recorded for it.
	ErrUnverified = errors.New("peer answer signature does not verify")
	// ErrKeyMismatch reports an issuer whose key is not the one the pairing
	// token names.
	ErrKeyMismatch = errors.New("issuer key does not match the pairing token")
)

// StatusError is a peer's refusal: the status it answered with.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return "peer answered " + strconv.Itoa(e.Status) }

// Errors of the issuer's side of a redeem (Accept), which the peer API
// answers as its spec states.
var (
	// ErrInvalidRequest is 400: a malformed redeem, or a protocol major
	// version other than this instance's.
	ErrInvalidRequest = fmt.Errorf("invalid pairing request: %w", kernel.ErrInvalid)
	// ErrNonceRefused is 403: the nonce is unknown, expired, or was
	// redeemed by another instance or with another key.
	ErrNonceRefused = fmt.Errorf("pairing nonce refused: %w", kernel.ErrForbidden)
)

// Refusal is a trust or state error, 7.x's ServiceTrustException: the
// service_trust endpoints answer it with 400 and Reason after their message
// pattern ("Error while token pairing. %s"). Reasons that 7.161.26's
// ServiceTrustServiceImpl and ServiceTrustResource give keep its words.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(reason string) error { return &Refusal{Reason: reason} }

// 7.161.26's reasons (ServiceTrustServiceImpl.java:83-109,
// ServiceTrustResource.java:275-279), and artifactweave's own where 7.x's
// cause cannot occur here.
const (
	reasonExpired      = "Attempt to pair instances with an expired token"
	reasonItself       = "Attempt to pair instance with itself"
	reasonNetwork      = "Error exchanging token due to network failure"
	reasonStatus       = "Error exchanging token: unacceptable http status code from target server %d"
	reasonNoBaseURL    = "Base url is not provided"
	reasonNoPairing    = "Pairing does not exist for instance"
	reasonCancelFailed = "Failed calling revoke token endpoint"
	reasonMalformed    = "The pairing token is malformed"
	reasonOtherUseCase = "The pairing token was issued for use case %s"
	reasonUsed         = "The pairing token was already used"
	reasonNotIssuer    = "The issuing instance does not hold the key the pairing token names"
	reasonVersion      = "The issuing instance speaks peer protocol %s, not %d.x"
	reasonUnverified   = "The issuing instance's answer is not signed with its key"
	defaultStaleAfter  = 10 * time.Minute
	protocolMinor      = 0
)

// Request is the redeeming instance's identity, sent to the issuer with
// the token's nonce (the peer API's PairRequest).
type Request struct {
	Nonce           string
	InstanceID      string
	BaseURL         string
	PublicKey       ed25519.PublicKey
	ProtocolVersion string
}

// Answer is the issuer's identity, returned to the redeemer (the peer
// API's PairResponse).
type Answer struct {
	InstanceID      string
	BaseURL         string
	PublicKey       ed25519.PublicKey
	ProtocolVersion string
	UseCase         model.UseCase
}

// Remote makes pairing's two calls to another instance over the signed
// peer API. The peer adapter implements it with the signing client: Pair
// sends POST /artifactweave/peer/v1/pair under baseURL and returns the
// issuer's answer once its signature verifies with the key the answer
// carries; Unpair sends DELETE /pair?use_case= to the peer. Their errors
// are ErrUnreachable, ErrUnverified or a *StatusError.
type Remote interface {
	Pair(ctx context.Context, baseURL string, req Request) (Answer, error)
	Unpair(ctx context.Context, peer model.Peer, use model.UseCase) error
}

// Store is what pairing reads and writes: the peer records and nonces, the
// outbox cursors a removed peer leaves, and this instance's identity, in
// one unit of work.
type Store interface {
	tx.Runner
	Peers() port.PeerStore
	Outbox() outbox.OutboxStore
	Instance() instance.InstanceStore
}

// Service pairs this instance with others through the service_trust
// endpoints and records each as the other's peer (decision 0021).
type Service struct {
	Store  Store
	Remote Remote
	// Authorize refuses a caller who is not currently an administrator;
	// every administrator operation calls it first.
	Authorize func(ctx context.Context) error
	// Now and Rand default to the clock and crypto/rand.
	Now  func() time.Time
	Rand io.Reader
	// StaleAfter is how long after it was last seen a peer is OFFLINE;
	// zero means ten minutes, two of PEER-05's status reads.
	StaleAfter time.Duration
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) staleAfter() time.Duration {
	if s.StaleAfter > 0 {
		return s.StaleAfter
	}
	return defaultStaleAfter
}

func (s Service) authorize(ctx context.Context) error {
	if s.Authorize == nil {
		return fmt.Errorf("pairing authority unavailable: %w", kernel.ErrStore)
	}
	return s.Authorize(ctx)
}

func (s Service) identity(ctx context.Context) (instance.InstanceIdentity, error) {
	id, err := s.Store.Instance().Get(ctx)
	if err != nil {
		return id, fmt.Errorf("pairing identity: %w", err)
	}
	return id, nil
}

// Version is a protocol version as the peer API writes it, <major>.<minor>.
func Version(major int) string { return strconv.Itoa(major) + "." + strconv.Itoa(protocolMinor) }

// Major parses the major of a <major>.<minor> protocol version.
func Major(version string) (int, bool) {
	major, minor, ok := strings.Cut(version, ".")
	if !ok {
		return 0, false
	}
	m, err := strconv.Atoi(major)
	if _, errMinor := strconv.Atoi(minor); err != nil || errMinor != nil || m < 0 {
		return 0, false
	}
	return m, true
}

// PeerBaseURL is the base URL an instance is reached at by peers: base
// without a trailing slash or /artifactory, as 7.x gives a federated
// repository pairing token's federatedRepoUrlBase
// (ServiceTrustResource.java:218-227).
func PeerBaseURL(base string) string {
	base = strings.TrimRight(base, "/")
	return strings.TrimSuffix(base, "/artifactory")
}

// Issue creates a pairing token for use on this instance, which base names
// (POST service_trust/pairing/{usecase}). Only the nonce's hash is stored;
// the token itself is returned once and never kept.
func (s Service) Issue(ctx context.Context, use model.UseCase, base string) (string, error) {
	if err := s.authorize(ctx); err != nil {
		return "", err
	}
	id, err := s.identity(ctx)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceBytes)
	r := s.Rand
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, nonce); err != nil {
		return "", fmt.Errorf("pairing nonce: %w", err)
	}
	now := s.now()
	t := Token{
		BaseURL: PeerBaseURL(base), InstanceID: id.InstanceID, Fingerprint: Fingerprint(id.PublicKey),
		UseCase: use, Nonce: base64.RawURLEncoding.EncodeToString(nonce),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(model.PairingTokenTTL).Unix(),
	}
	raw, err := t.Sign(id.PrivateKey)
	if err != nil {
		return "", err
	}
	if err := s.Store.Peers().AddNonce(ctx, model.PairingNonce{
		NonceHash: NonceHash(nonce), UseCase: use, ExpiresAt: t.Expires(),
	}); err != nil {
		return "", fmt.Errorf("store pairing nonce: %w", err)
	}
	return raw, nil
}

// Redeem completes a pairing from the redeeming side (PUT
// service_trust/pairing/{usecase}): it calls the issuer's POST /pair with
// the token's nonce and this instance's identity, checks the issuer's
// answer against the token, and records the issuer as a peer for use.
// self is this instance's base URL.
//
// The token's nonce is claimed here first, so a reused token, or one of two
// concurrent redemptions, is refused before the issuer is called; the claim
// is released when the pairing fails, so a token refused for a passing
// reason, such as an unreachable issuer, can be tried again while it lasts.
func (s Service) Redeem(ctx context.Context, use model.UseCase, raw, self string) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	tok, err := Parse(raw)
	if err != nil {
		return refuse(reasonMalformed)
	}
	if tok.UseCase != use {
		return refuse(fmt.Sprintf(reasonOtherUseCase, tok.UseCase))
	}
	now := s.now()
	if !now.Before(tok.Expires()) {
		return refuse(reasonExpired)
	}
	id, err := s.identity(ctx)
	if err != nil {
		return err
	}
	if tok.InstanceID == id.InstanceID {
		return refuse(reasonItself)
	}
	nonce, _ := tok.NonceBytes() // Parse checked it
	hash := NonceHash(nonce)
	claim := model.PairingNonce{
		NonceHash: hash, UseCase: use, ExpiresAt: tok.Expires(),
		RedeemedAt: now, RedeemedBy: id.InstanceID,
	}
	if err := s.Store.Peers().AddNonce(ctx, claim); errors.Is(err, kernel.ErrConflict) {
		return refuse(reasonUsed)
	} else if err != nil {
		return fmt.Errorf("claim pairing token: %w", err)
	}
	if err := s.redeem(ctx, tok, id, self, now); err != nil {
		// The claim goes with a failed pairing; failing to release it only
		// makes this token unusable, which its expiry ends anyway.
		_ = s.Store.Peers().DeleteNonce(context.WithoutCancel(ctx), hash)
		return err
	}
	return nil
}

func (s Service) redeem(
	ctx context.Context,
	tok Parsed,
	id instance.InstanceIdentity,
	self string,
	now time.Time,
) error {
	if s.Remote == nil {
		return fmt.Errorf("pairing transport unavailable: %w", kernel.ErrStore)
	}
	ans, err := s.Remote.Pair(ctx, tok.BaseURL, Request{
		Nonce: tok.Nonce, InstanceID: id.InstanceID, BaseURL: PeerBaseURL(self),
		PublicKey: id.PublicKey, ProtocolVersion: Version(id.ProtocolVersion),
	})
	var status *StatusError
	switch {
	case errors.Is(err, ErrUnreachable):
		return refuse(reasonNetwork)
	case errors.Is(err, ErrUnverified):
		return refuse(reasonUnverified)
	case errors.As(err, &status):
		return refuse(fmt.Sprintf(reasonStatus, status.Status))
	case err != nil:
		return fmt.Errorf("redeem pairing token: %w", err)
	}
	issuer := model.Peer{
		InstanceID: ans.InstanceID, BaseURL: tok.BaseURL, PublicKey: ans.PublicKey,
	}
	if err := s.checkAnswer(tok, ans, id.ProtocolVersion); err != nil {
		// The issuer has recorded this instance by now; tell whoever answered
		// to drop it, so a refused token leaves no peer record on either
		// side. The issuer's nonce stays spent: a token whose claims did not
		// hold is one nobody should redeem again.
		_ = s.Remote.Unpair(context.WithoutCancel(ctx), issuer, tok.UseCase)
		return err
	}
	issuer.ProtocolVersion, _ = Major(ans.ProtocolVersion)
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // fn wraps its own errors
		return s.grant(ctx, issuer, tok.UseCase, now)
	})
}

// checkAnswer holds the issuer's answer to the token: the key the token
// names, the signature it carries, the instance and use case it was issued
// by and for, and a protocol major this instance speaks.
func (s Service) checkAnswer(tok Parsed, ans Answer, major int) error {
	switch err := tok.Verify(ans.PublicKey); {
	case errors.Is(err, ErrKeyMismatch):
		return refuse(reasonNotIssuer)
	case err != nil:
		return refuse(reasonMalformed)
	}
	if ans.InstanceID != tok.InstanceID || ans.UseCase != tok.UseCase {
		return refuse(reasonNotIssuer)
	}
	if m, ok := Major(ans.ProtocolVersion); !ok || m != major {
		return refuse(fmt.Sprintf(reasonVersion, ans.ProtocolVersion, major))
	}
	return nil
}

// grant records p as a peer holding use, keeping the use cases it already
// holds, as last seen at now. It runs inside the caller's transaction.
func (s Service) grant(ctx context.Context, p model.Peer, use model.UseCase, now time.Time) error {
	if prev, err := s.Store.Peers().Get(ctx, p.InstanceID); err == nil {
		p.UseCases = prev.UseCases
		p.DisplayName, p.CityName, p.CountryCode, p.EdgeRole =
			prev.DisplayName, prev.CityName, prev.CountryCode, prev.EdgeRole
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("read peer %s: %w", p.InstanceID, err)
	}
	p.UseCases = append(p.UseCases, use)
	p.LastSeen = now
	p.Status = p.DeriveStatus(now, s.staleAfter(), false, 0)
	if err := s.Store.Peers().Put(ctx, p); err != nil {
		return fmt.Errorf("record peer %s: %w", p.InstanceID, err)
	}
	return nil
}

// Accept is the issuer's side of a redeem (the peer API's POST /pair): it
// redeems req's nonce for the caller and records the caller as a peer for
// the nonce's use case, in one transaction, and answers with this
// instance's identity. self is this instance's base URL. The caller's
// signature with req.PublicKey is checked before Accept is called. A
// repeat by the same instance with the same key answers again until the
// token expires, so a redeemer whose answer was lost can retry.
func (s Service) Accept(ctx context.Context, req Request, self string) (Answer, error) {
	id, err := s.identity(ctx)
	if err != nil {
		return Answer{}, err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(req.Nonce)
	switch m, ok := Major(req.ProtocolVersion); {
	case err != nil || len(nonce) != nonceBytes,
		instance.ValidateInstanceID(req.InstanceID) != nil,
		len(req.PublicKey) != ed25519.PublicKeySize,
		!validBaseURL(req.BaseURL):
		return Answer{}, ErrInvalidRequest
	case !ok || m != id.ProtocolVersion:
		return Answer{}, fmt.Errorf("%w: protocol %s", ErrInvalidRequest, req.ProtocolVersion)
	case req.InstanceID == id.InstanceID:
		return Answer{}, ErrNonceRefused
	}
	now := s.now()
	redeemer := req.InstanceID + " " + Fingerprint(req.PublicKey)
	var use model.UseCase
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		n, err := s.Store.Peers().RedeemNonce(ctx, NonceHash(nonce), redeemer, now)
		if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
			return ErrNonceRefused
		}
		if err != nil {
			return fmt.Errorf("redeem pairing nonce: %w", err)
		}
		use = n.UseCase
		return s.grant(ctx, model.Peer{
			InstanceID: req.InstanceID, BaseURL: PeerBaseURL(req.BaseURL),
			PublicKey: req.PublicKey, ProtocolVersion: id.ProtocolVersion,
		}, use, now)
	})
	if err != nil {
		return Answer{}, err //nolint:wrapcheck // the transaction returns its function's wrapped errors
	}
	return Answer{
		InstanceID: id.InstanceID, BaseURL: PeerBaseURL(self), PublicKey: id.PublicKey,
		ProtocolVersion: Version(id.ProtocolVersion), UseCase: use,
	}, nil
}

func validBaseURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

// Pairings returns the base URLs of the peers holding use (GET
// service_trust/pairings/{usecase}), ordered by instance id.
func (s Service) Pairings(ctx context.Context, use model.UseCase) ([]string, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	peers, err := s.Store.Peers().List(ctx, port.PeerFilter{UseCase: use})
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.BaseURL)
	}
	return out, nil
}

// Unpair ends the trust for use with the peer at base (DELETE
// service_trust/pairing/{usecase}): it tells the peer, then drops the use
// case here. force drops it even when the peer cannot be told.
func (s Service) Unpair(ctx context.Context, use model.UseCase, base string, force bool) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if base == "" {
		return refuse(reasonNoBaseURL)
	}
	peers, err := s.Store.Peers().List(ctx, port.PeerFilter{UseCase: use})
	if err != nil {
		return fmt.Errorf("list peers: %w", err)
	}
	i := slices.IndexFunc(peers, func(p model.Peer) bool { return p.BaseURL == PeerBaseURL(base) })
	if i < 0 {
		return refuse(reasonNoPairing)
	}
	peer := peers[i]
	if s.Remote == nil {
		return fmt.Errorf("pairing transport unavailable: %w", kernel.ErrStore)
	}
	if err := s.Remote.Unpair(ctx, peer, use); err != nil && !force {
		if errors.Is(err, ErrUnverified) {
			_ = s.Observe(context.WithoutCancel(ctx), peer.InstanceID, true)
		}
		return refuse(reasonCancelFailed)
	}
	return s.Drop(ctx, peer.InstanceID, use)
}

// Drop removes use from the peer's record; a peer left with no use case
// is removed with its outbox cursors. A peer that does not hold use, or
// is not recorded, is no error: the peer API's DELETE /pair answers 204
// either way.
func (s Service) Drop(ctx context.Context, instanceID string, use model.UseCase) error {
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // fn wraps its own errors
		p, err := s.Store.Peers().Get(ctx, instanceID)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read peer %s: %w", instanceID, err)
		}
		if !p.Has(use) {
			// Nothing to drop; a peer recorded for nothing else keeps its
			// record, which other use cases' later pairing fills.
			return nil
		}
		p.UseCases = slices.DeleteFunc(p.UseCases, func(u model.UseCase) bool { return u == use })
		if len(p.UseCases) > 0 {
			if err := s.Store.Peers().Put(ctx, p); err != nil {
				return fmt.Errorf("record peer %s: %w", instanceID, err)
			}
			return nil
		}
		if err := s.Store.Peers().Delete(ctx, instanceID); err != nil {
			return fmt.Errorf("remove peer %s: %w", instanceID, err)
		}
		if err := s.Store.Outbox().DeleteCursors(ctx, instanceID); err != nil {
			return fmt.Errorf("remove peer %s cursors: %w", instanceID, err)
		}
		return nil
	})
}

// Observe records an exchange with a peer: rejected when its signature did
// not verify, which shows it UNAUTHORIZED, and otherwise as seen now. The
// status is DeriveStatus's, so a peer not seen within StaleAfter reads
// OFFLINE (Status).
func (s Service) Observe(ctx context.Context, instanceID string, rejected bool) error {
	now := s.now()
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // fn wraps its own errors
		p, err := s.Store.Peers().Get(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("read peer %s: %w", instanceID, err)
		}
		if !rejected {
			p.LastSeen = now
		}
		p.Status = p.DeriveStatus(now, s.staleAfter(), rejected, 0)
		if err := s.Store.Peers().Put(ctx, p); err != nil {
			return fmt.Errorf("record peer %s: %w", instanceID, err)
		}
		return nil
	})
}

// Status is a peer's status now: UNAUTHORIZED while its last exchange was
// rejected, OFFLINE once it has not been seen within StaleAfter, PARTIAL
// while failing use cases are reported, and ONLINE otherwise.
func (s Service) Status(p model.Peer, failingUseCases int) model.PeerStatus {
	return p.DeriveStatus(s.now(), s.staleAfter(), p.Status == model.PeerUnauthorized, failingUseCases)
}

// Prune removes the nonces that expired before now and returns how many:
// unredeemed tokens are never cancelled, they expire, and this clears what
// they leave.
func (s Service) Prune(ctx context.Context) (int64, error) {
	n, err := s.Store.Peers().PruneNonces(ctx, s.now())
	if err != nil {
		return 0, fmt.Errorf("prune pairing nonces: %w", err)
	}
	return n, nil
}
