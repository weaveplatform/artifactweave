package peer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// Prefix is the peer API's path prefix, api/src/peer's servers[0].url.
const Prefix = "/artifactweave/peer/v1"

// DefaultMaxRequestBody bounds a peer request's body, which the middleware
// reads whole to check its digest before any handler runs. An event batch
// holds at most 1000 events, which this leaves room for.
const DefaultMaxRequestBody = 64 << 20

// Server authenticates every request under Prefix and signs every answer to
// a verified one (decision 0021, Transport).
type Server struct {
	// Verifier checks signatures, times, keys and nonces.
	Verifier *corepeer.Verifier
	// Signer returns this instance's signer; it is called per answer, so
	// the composition root can load the instance key once, lazily.
	Signer func(context.Context) (corepeer.Signer, error)
	// MaxRequestBody bounds a request body; 0 is DefaultMaxRequestBody.
	MaxRequestBody int64
}

// Middleware refuses, before next runs, every request that is not signed by
// a paired peer: 401 for a missing, malformed or failing signature, an
// unknown key, a body that does not match Content-Digest, a time more than
// five minutes off or a replayed nonce; 400 for a protocol major version
// other than this build's, recorded on a verified caller's record. A
// verified request reaches next with the caller in its context
// (corepeer.Caller), and its answer is signed. POST /pair is verified with
// the key its body presents, since its caller is not yet a peer.
func (s *Server) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := s.MaxRequestBody
		if limit <= 0 {
			limit = DefaultMaxRequestBody
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				refuse(w, r, http.StatusRequestEntityTooLarge, "The request body exceeds "+strconv.FormatInt(limit, 10)+" bytes")
				return
			}
			refuse(w, r, http.StatusBadRequest, "The request body could not be read")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		version, err := corepeer.ParseVersion(r.Header.Get(corepeer.HeaderProtocol))
		if err != nil {
			refuse(w, r, http.StatusBadRequest, "Artifactweave-Peer-Protocol must be <major>.<minor>")
			return
		}
		in := corepeer.Request{Method: r.Method, TargetURI: TargetURI(r), Header: r.Header, Body: body}
		if pairRedeem(r) {
			var problem string
			if in.Bootstrap, problem = bootstrapCaller(body); problem != "" {
				refuse(w, r, http.StatusBadRequest, problem)
				return
			}
		}
		caller, verr := s.Verifier.VerifyRequest(r.Context(), in)
		if verr != nil && !refusal(verr) {
			httpkit.NoteCause(r.Context(), verr)
			refuse(w, r, http.StatusInternalServerError, "The peer signature could not be checked")
			return
		}
		verified := verr == nil
		if verified && in.Bootstrap == nil {
			// Recorded whether or not the version is this build's, so a peer
			// that comes back to a compatible version shows available again.
			if err := s.Verifier.ObserveVersion(r.Context(), caller, version); err != nil {
				httpkit.NoteCause(r.Context(), err)
				refuse(w, r, http.StatusInternalServerError, "The peer protocol version could not be recorded")
				return
			}
		}
		// A major mismatch answers 400 even when the signature does not
		// verify: a peer of another major version may sign differently, and
		// 400 tells its operator what is wrong. Only a verified caller's
		// record changes.
		if !version.Compatible() {
			msg := fmt.Sprintf("Peer protocol %s is not compatible with this instance's %s",
				version, corepeer.CurrentVersion)
			if !verified {
				refuse(w, r, http.StatusBadRequest, msg)
				return
			}
			s.serveSigned(w, r, in, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refuse(w, r, http.StatusBadRequest, msg)
			}))
			return
		}
		if verr != nil {
			refuse(w, r, http.StatusUnauthorized, refusalMessage(verr))
			return
		}
		s.serveSigned(w, r.WithContext(corepeer.WithCaller(r.Context(), caller)), in, next)
	})
}

// serveSigned runs next and signs its answer to in.
func (s *Server) serveSigned(w http.ResponseWriter, r *http.Request, in corepeer.Request, next http.Handler) {
	signer, err := s.Signer(r.Context())
	if err != nil {
		httpkit.NoteCause(r.Context(), err)
		refuse(w, r, http.StatusInternalServerError, "The instance key is unavailable")
		return
	}
	req := corepeer.Message{Method: in.Method, TargetURI: in.TargetURI, Header: in.Header}
	sw := &signingWriter{w: w, sign: func(status int, h http.Header) error {
		return signer.SignResponse(status, h, req)
	}}
	next.ServeHTTP(sw, r)
	if err := sw.finish(); err != nil {
		httpkit.NoteCause(r.Context(), err)
	}
}

// refuse writes the flat envelope the peer API's errors use. It is never
// signed: an answer to an unverified request has no one to bind to.
func refuse(w http.ResponseWriter, r *http.Request, status int, message string) {
	httpkit.WriteErrProfile(w, r, apierror.ProfileGrid, apierror.New(status, message))
}

// refusal reports whether err is a verdict on the request rather than a
// failure to reach one.
func refusal(err error) bool {
	for _, e := range []error{
		corepeer.ErrUnsigned, corepeer.ErrBadSignature, corepeer.ErrDigest, corepeer.ErrUnknownKey,
		corepeer.ErrCoverage, corepeer.ErrSkew, corepeer.ErrReplay,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// refusalMessage is the 401 message for a refusal. Each is fixed text: no
// header value, key, signature or nonce reaches the answer or a log.
func refusalMessage(err error) string {
	switch {
	case errors.Is(err, corepeer.ErrUnknownKey):
		return "The signing key belongs to no paired peer"
	case errors.Is(err, corepeer.ErrDigest):
		return "Content-Digest does not match the request body"
	case errors.Is(err, corepeer.ErrSkew):
		return "The request time is more than five minutes from this instance's clock"
	case errors.Is(err, corepeer.ErrReplay):
		return "The request nonce was already used"
	case errors.Is(err, corepeer.ErrCoverage):
		return "The signature must cover @method, @target-uri, date and, with a body, content-digest, " +
			"with keyid, created and nonce"
	case errors.Is(err, corepeer.ErrBadSignature):
		return "The peer signature does not verify"
	default:
		return "The request carries no valid peer signature"
	}
}

// pairRedeem reports POST /pair, the one request whose caller is not yet a
// peer.
func pairRedeem(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.TrimSuffix(r.URL.Path, "/") == Prefix+"/pair"
}

// bootstrapCaller is the identity a POST /pair body presents: its signature
// must verify with public_key and name instance_id as keyid (api/src/peer,
// redeemPairing). A body that presents none answers 400 with the returned
// message, as the operation's own validation would.
func bootstrapCaller(body []byte) (*model.Peer, string) {
	var b struct {
		InstanceID      string `json:"instance_id"`
		BaseURL         string `json:"base_url"`
		PublicKey       string `json:"public_key"`
		ProtocolVersion string `json:"protocol_version"`
	}
	if err := json.Unmarshal(body, &b); err != nil || b.InstanceID == "" {
		return nil, "The body must present instance_id and public_key"
	}
	key, err := base64.StdEncoding.DecodeString(b.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, "public_key must be 32 bytes of base64"
	}
	p := &model.Peer{InstanceID: b.InstanceID, BaseURL: b.BaseURL, PublicKey: key}
	if v, err := corepeer.ParseVersion(b.ProtocolVersion); err == nil {
		p.ProtocolVersion = v.Major
	}
	return p, ""
}

// TargetURI reconstructs the absolute URI the sender addressed, which its
// signature covers as @target-uri: the scheme and host a proxy forwarded
// (X-Forwarded-Proto, -Host and -Port) or the connection's own, and the
// request target exactly as received. A peer's recorded base URL must
// therefore reach this listener with its host intact, or through a proxy
// that forwards it; a proxy that rewrites the path breaks the signature.
// X-JFrog-Override-Base-Url is ignored: it names the base URL links use,
// which need not be the one the peer called.
func TargetURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fwd := baseurl.ForwardedHost(r); fwd != "" {
		host = fwd
		if p := firstEntry(r.Header.Get("X-Forwarded-Proto")); p == "http" || p == "https" {
			scheme = p
		}
		if port := firstEntry(r.Header.Get("X-Forwarded-Port")); port != "" &&
			strings.Trim(port, "0123456789") == "" && !hasPort(host) &&
			(scheme == "https" && port != "443" || scheme == "http" && port != "80") {
			host = net.JoinHostPort(strings.Trim(host, "[]"), port)
		}
	}
	target := r.RequestURI
	if !strings.HasPrefix(target, "/") {
		target = r.URL.RequestURI()
	}
	return scheme + "://" + host + target
}

func firstEntry(v string) string {
	entry, _, _ := strings.Cut(v, ",")
	return strings.ToLower(strings.TrimSpace(entry))
}

func hasPort(host string) bool {
	_, port, err := net.SplitHostPort(host)
	return err == nil && port != ""
}

// signingWriter holds an answer until it can be signed. A handler that sets
// Content-Digest before its status (a blob, whose SHA-256 it knows) is
// signed at once and streamed; any other answer is buffered, digested and
// signed when the handler returns.
type signingWriter struct {
	w         http.ResponseWriter
	sign      func(status int, h http.Header) error
	status    int
	buf       bytes.Buffer
	streaming bool
	failed    bool
}

func (s *signingWriter) Header() http.Header { return s.w.Header() }

func (s *signingWriter) WriteHeader(status int) {
	if s.status != 0 {
		return
	}
	s.status = status
	if s.w.Header().Get(corepeer.HeaderContentDigest) == "" {
		return
	}
	s.streaming = true
	if err := s.sign(status, s.w.Header()); err != nil {
		s.failed = true
		s.w.Header().Del(corepeer.HeaderContentDigest)
		refuseRaw(s.w)
		return
	}
	s.w.WriteHeader(status)
}

func (s *signingWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.WriteHeader(http.StatusOK)
	}
	switch {
	case s.failed:
		return 0, errSigning
	case s.streaming:
		return s.w.Write(b) //nolint:wrapcheck // the writer's own error, as net/http gives it
	default:
		return s.buf.Write(b) //nolint:wrapcheck // bytes.Buffer only panics
	}
}

// Flush passes through only once the answer is streaming; flushing a
// buffered answer would send it unsigned.
func (s *signingWriter) Flush() {
	if f, ok := s.w.(http.Flusher); ok && s.streaming && !s.failed {
		f.Flush()
	}
}

var errSigning = errors.New("the peer answer could not be signed")

// finish signs and sends a buffered answer.
func (s *signingWriter) finish() error {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if s.streaming || s.failed {
		if s.failed {
			return errSigning
		}
		return nil
	}
	h := s.w.Header()
	h.Set(corepeer.HeaderContentDigest, corepeer.ContentDigest(s.buf.Bytes()))
	if err := s.sign(s.status, h); err != nil {
		h.Del(corepeer.HeaderContentDigest)
		refuseRaw(s.w)
		return fmt.Errorf("%w: %w", errSigning, err)
	}
	h.Set("Content-Length", strconv.Itoa(s.buf.Len()))
	s.w.WriteHeader(s.status)
	_, err := s.w.Write(s.buf.Bytes())
	return err //nolint:wrapcheck // the writer's own error
}

// refuseRaw answers 500 when an answer cannot be signed. The peer reads an
// unsigned answer as a failure, which is what it is.
func refuseRaw(w http.ResponseWriter) {
	h := w.Header()
	for k := range h {
		delete(h, k)
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, `{"code":"INTERNAL_SERVER_ERROR","message":"The answer could not be signed"}`+"\n")
}
