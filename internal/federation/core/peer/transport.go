package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// This file is the peer protocol's policy over RFC 9421 (decision 0021,
// Transport and Wire contract): what a peer signs, and what a receiver
// requires before it acts on a request or a caller on an answer.

// Skew is the clock difference a signature's created time and Date may
// show: decision 0021's five minutes. A nonce is remembered for as long.
const Skew = 5 * time.Minute

// Alg is the only signature algorithm peers use.
const Alg = "ed25519"

// label names the one signature a peer message carries. Receivers accept
// any label; this is only what this build sends.
const label = "peer"

// maxNonce bounds a nonce's length; only its hash is stored, so this keeps
// a hostile sender from making the hashing itself expensive.
const maxNonce = 256

// Refusals a verified exchange can end in, besides ErrUnsigned,
// ErrBadSignature and ErrDigest. None carries a key, a signature or a
// nonce.
var (
	// ErrUnknownKey: the keyid names no paired peer, or not the peer the
	// caller expected to answer.
	ErrUnknownKey = errors.New("the signing key belongs to no paired peer")
	// ErrCoverage: the signature leaves out a component or parameter the
	// peer protocol requires, or names another algorithm.
	ErrCoverage = errors.New("the signature does not cover what the peer protocol requires")
	// ErrSkew: created, expires or Date is more than Skew from this
	// instance's clock.
	ErrSkew = errors.New("the message time is more than five minutes from this instance's clock")
	// ErrReplay: the nonce was accepted before, within Skew.
	ErrReplay = errors.New("the request nonce was already used")
)

// RequestComponents are what a peer request's signature covers: the
// method, the target URI and Date, and Content-Digest exactly when the
// request has a body (decision 0021, Wire contract).
func RequestComponents(hasBody bool) []Component {
	c := []Component{{Name: "@method"}, {Name: "@target-uri"}, {Name: "date"}}
	if hasBody {
		c = append(c, Component{Name: "content-digest"})
	}
	return c
}

// ResponseComponents are what an answer's signature covers: its status,
// Date and Content-Digest, and the request's method, target URI and
// Signature-Input. The last binds the answer to the request's nonce, so an
// answer to one request cannot be passed off as the answer to another.
// Signature-Input rather than Signature is covered: a signature value can
// be forged to collide across keys (RFC 9421, section 7.3.7), while the
// input names the requester's key and nonce outright.
func ResponseComponents() []Component {
	return []Component{
		{Name: "@status"},
		{Name: "date"},
		{Name: "content-digest"},
		{Name: "@method", Req: true},
		{Name: "@target-uri", Req: true},
		{Name: "signature-input", Req: true},
	}
}

// Signer signs this instance's peer requests and answers with its Ed25519
// key, its instance id as keyid.
type Signer struct {
	KeyID string
	Key   ed25519.PrivateKey
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Rand supplies nonces; nil is crypto/rand.
	Rand io.Reader
}

func (s Signer) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s Signer) nonce() (string, error) {
	r := s.Rand
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 24)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("peer signature nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SignRequest signs a request to targetURI, the absolute URI it is sent to,
// with body as its content (nil for none). It sets the protocol version,
// Date, Content-Digest when there is a body, Signature-Input and Signature
// on h, replacing any values there.
func (s Signer) SignRequest(method, targetURI string, h http.Header, body []byte) error {
	now := s.now()
	h.Set(HeaderProtocol, CurrentVersion.String())
	h.Set(HeaderDate, now.UTC().Format(http.TimeFormat))
	h.Del(HeaderContentDigest)
	if len(body) > 0 {
		h.Set(HeaderContentDigest, ContentDigest(body))
	}
	return s.sign(Message{Method: method, TargetURI: targetURI, Header: h}, RequestComponents(len(body) > 0), now)
}

// SignResponse signs an answer with status to req. h must already carry the
// answer's Content-Digest (ContentDigest of the body, or DigestFromSHA256
// for a body streamed after the headers); it sets the protocol version,
// Date, Signature-Input and Signature.
func (s Signer) SignResponse(status int, h http.Header, req Message) error {
	if h.Get(HeaderContentDigest) == "" {
		return fmt.Errorf("sign peer answer: %w: no Content-Digest", ErrDigest)
	}
	now := s.now()
	h.Set(HeaderProtocol, CurrentVersion.String())
	h.Set(HeaderDate, now.UTC().Format(http.TimeFormat))
	return s.sign(Message{Status: status, Header: h, Request: &req}, ResponseComponents(), now)
}

func (s Signer) sign(m Message, covered []Component, now time.Time) error {
	if len(s.Key) != ed25519.PrivateKeySize || s.KeyID == "" {
		return fmt.Errorf("sign peer message: %w: the instance key is not set", ErrSigning)
	}
	nonce, err := s.nonce()
	if err != nil {
		return err
	}
	in, sig, err := Sign(m, label, covered, Params{Created: now, KeyID: s.KeyID, Alg: Alg, Nonce: nonce}, s.Key)
	if err != nil {
		return fmt.Errorf("sign peer message: %w", err)
	}
	m.Header.Set(HeaderSignatureInput, in)
	m.Header.Set(HeaderSignature, sig)
	return nil
}

// PeerKeys is what Verifier needs of the peer records: the key a keyid
// names and a place to record the protocol version a peer speaks.
// port.PeerStore satisfies it.
type PeerKeys interface {
	Get(ctx context.Context, instanceID string) (model.Peer, error)
	SetProtocolVersion(ctx context.Context, instanceID string, major int) error
}

// Verifier checks signed peer requests.
type Verifier struct {
	Keys   PeerKeys
	Nonces port.RequestNonceStore
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	// pruned is when expired nonces were last pruned, in Unix nanoseconds.
	pruned atomic.Int64
}

func (v *Verifier) now() time.Time {
	if v.Now == nil {
		return time.Now()
	}
	return v.Now()
}

// Request is a received peer request: its method, the absolute target URI
// the receiver reconstructs, its fields and its whole body.
type Request struct {
	Method    string
	TargetURI string
	Header    http.Header
	Body      []byte
	// Bootstrap, when set, is the caller's identity taken from the request
	// itself rather than from a peer record: POST /pair, where the caller
	// is not yet a peer and proves it holds the key it presents.
	Bootstrap *model.Peer
}

// VerifyRequest authenticates r and returns the calling peer. It checks, in
// order: the signature's form and coverage, its time and Date against the
// clock, the key its keyid names, the signature itself, Content-Digest
// against the body, and last the nonce, so a request that fails any other
// check never takes a place in the replay cache.
func (v *Verifier) VerifyRequest(ctx context.Context, r Request) (model.Peer, error) {
	now := v.now()
	sig, err := ParseSignature(r.Header)
	if err != nil {
		return model.Peer{}, err
	}
	hasBody := len(r.Body) > 0 || r.Header.Get(HeaderContentDigest) != ""
	if err := requireCoverage(sig, RequestComponents(hasBody)); err != nil {
		return model.Peer{}, err
	}
	if sig.Params.Nonce == "" || len(sig.Params.Nonce) > maxNonce {
		return model.Peer{}, fmt.Errorf("%w: a nonce", ErrCoverage)
	}
	if err := checkTimes(sig, r.Header, now); err != nil {
		return model.Peer{}, err
	}
	caller, err := v.caller(ctx, sig.Params.KeyID, r.Bootstrap)
	if err != nil {
		return model.Peer{}, err
	}
	m := Message{Method: r.Method, TargetURI: r.TargetURI, Header: r.Header}
	if err := sig.Verify(m, caller.PublicKey); err != nil {
		return model.Peer{}, err
	}
	if hasBody {
		if err := CheckContentDigest(r.Header.Get(HeaderContentDigest), r.Body); err != nil {
			return model.Peer{}, err
		}
	}
	if err := v.remember(ctx, sig, now); err != nil {
		return model.Peer{}, err
	}
	return caller, nil
}

func (v *Verifier) caller(ctx context.Context, keyID string, bootstrap *model.Peer) (model.Peer, error) {
	if bootstrap != nil {
		if keyID != bootstrap.InstanceID {
			return model.Peer{}, fmt.Errorf("%w: keyid is not the presented instance", ErrUnknownKey)
		}
		return *bootstrap, nil
	}
	p, err := v.Keys.Get(ctx, keyID)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.Peer{}, ErrUnknownKey
	}
	if err != nil {
		return model.Peer{}, fmt.Errorf("peer signing key: %w", err)
	}
	return p, nil
}

// remember takes the nonce's place in the replay cache until the request
// could no longer pass the skew check, then prunes expired places at most
// once per Skew.
func (v *Verifier) remember(ctx context.Context, sig Signature, now time.Time) error {
	h := sha256.New()
	// The keyid is hashed with the nonce so that two peers choosing the same
	// nonce do not refuse each other.
	_, _ = io.WriteString(h, sig.Params.KeyID)
	h.Write([]byte{0})
	_, _ = io.WriteString(h, sig.Params.Nonce)
	err := v.Nonces.Remember(ctx, h.Sum(nil), sig.Params.Created.Add(Skew), now)
	if errors.Is(err, kernel.ErrConflict) {
		return ErrReplay
	}
	if err != nil {
		return fmt.Errorf("peer request nonce: %w", err)
	}
	last := v.pruned.Load()
	if now.UnixNano()-last >= int64(Skew) && v.pruned.CompareAndSwap(last, now.UnixNano()) {
		// Pruning is housekeeping: a failure leaves expired entries, which
		// Remember replaces anyway, and must not refuse a verified request.
		_, _ = v.Nonces.Prune(ctx, now.Add(-time.Second))
	}
	return nil
}

// ObserveVersion records the major protocol version a verified caller sent
// when it differs from the one its record holds. Recording a mismatch is
// what makes status show the peer unavailable; recording a match again
// clears it once the peer speaks this build's version (decision 0021,
// Versioning). A bootstrap caller has no record to update.
func (v *Verifier) ObserveVersion(ctx context.Context, caller model.Peer, sent Version) error {
	if caller.ProtocolVersion == sent.Major {
		return nil
	}
	if err := v.Keys.SetProtocolVersion(ctx, caller.InstanceID, sent.Major); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("record peer protocol version: %w", err)
	}
	return nil
}

// VerifyResponse authenticates an answer from the peer keyID with key to
// the request it answers (resp.Request), with body as the answer's whole
// content. A caller must not act on an answer, or read its body, before
// this succeeds.
func VerifyResponse(resp Message, body []byte, keyID string, key ed25519.PublicKey, now time.Time) error {
	if err := VerifyResponseFields(resp, keyID, key, now); err != nil {
		return err
	}
	return CheckContentDigest(resp.Header.Get(HeaderContentDigest), body)
}

// VerifyResponseFields is VerifyResponse without the body: it
// authenticates the answer's status and fields, Content-Digest among them,
// so an answer too large to hold, a blob, can be streamed once its
// Content-Digest is known to be the peer's. The caller must then check
// the content against that digest (DigestSHA256) before using it.
func VerifyResponseFields(resp Message, keyID string, key ed25519.PublicKey, now time.Time) error {
	sig, err := ParseSignature(resp.Header)
	if err != nil {
		return err
	}
	if err := requireCoverage(sig, ResponseComponents()); err != nil {
		return err
	}
	if err := checkTimes(sig, resp.Header, now); err != nil {
		return err
	}
	if sig.Params.KeyID != keyID {
		return fmt.Errorf("%w: the answer is not signed by the peer called", ErrUnknownKey)
	}
	return sig.Verify(resp, key)
}

// requireCoverage checks that sig covers every component in want, carries a
// keyid and created time, and names no algorithm other than Ed25519.
func requireCoverage(sig Signature, want []Component) error {
	for _, c := range want {
		if !sig.Covers(c) {
			return fmt.Errorf("%w: %s", ErrCoverage, c.Name)
		}
	}
	switch {
	case sig.Params.KeyID == "":
		return fmt.Errorf("%w: a keyid", ErrCoverage)
	case sig.Params.Created.IsZero():
		return fmt.Errorf("%w: a created time", ErrCoverage)
	case sig.Params.Alg != "" && sig.Params.Alg != Alg:
		return fmt.Errorf("%w: algorithm %s", ErrCoverage, Alg)
	}
	return nil
}

// checkTimes refuses a created time or Date more than Skew from now, and an
// expires time already past.
func checkTimes(sig Signature, h http.Header, now time.Time) error {
	if h.Get(HeaderDate) == "" {
		return fmt.Errorf("%w: Date is missing", ErrUnsigned)
	}
	date, err := http.ParseTime(h.Get(HeaderDate))
	if err != nil {
		return fmt.Errorf("%w: Date is not an HTTP date", ErrSkew)
	}
	for _, t := range []time.Time{sig.Params.Created, date} {
		if d := now.Sub(t); d > Skew || d < -Skew {
			return ErrSkew
		}
	}
	if !sig.Params.Expires.IsZero() && !now.Before(sig.Params.Expires) {
		return ErrSkew
	}
	return nil
}

// callerKey keys the verified caller in a request context.
type callerKey struct{}

// WithCaller returns ctx carrying the verified calling peer.
func WithCaller(ctx context.Context, p model.Peer) context.Context {
	return context.WithValue(ctx, callerKey{}, p)
}

// Caller returns the peer whose signature the request carrying ctx was
// verified with, and false when there is none. Every peer operation
// authorizes its own use case against it.
func Caller(ctx context.Context) (model.Peer, bool) {
	p, ok := ctx.Value(callerKey{}).(model.Peer)
	return p, ok
}
