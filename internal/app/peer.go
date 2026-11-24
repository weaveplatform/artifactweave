package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/adapters/pushhttp"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

// peerBase is the prefix of the peer API (decision 0021), its spec's
// servers[0].url.
const peerBase = peeradapter.Prefix

// peerRequest reports a request to the peer API, which user authentication
// skips. A peer proves who it is with an HTTP message signature, which
// peerGuard verifies, and carries no user credential: user authentication
// would answer every peer 401 while anonymous access is off, and run it as
// the anonymous user while it is on.
func peerRequest(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, peerBase+"/") }

// PeerStore is what the peer transport needs of the metadata store: the
// peer records whose keys verify requests, the replay cache, and this
// instance's identity, whose key signs answers.
type PeerStore interface {
	instance.Store
	Peers() fedport.PeerStore
	RequestNonces() fedport.RequestNonceStore
}

// peerGuard verifies every request under the peer prefix before routing,
// so no peer operation, generated or hand-written, nor any later
// middleware's effect, is reached without a paired peer's signature
// (decision 0021, Transport). Requests elsewhere pass untouched. A router
// with no store has no peers, so it refuses every peer request the same
// way a store with none would.
func peerGuard(st PeerStore, now func() time.Time) func(http.Handler) http.Handler {
	server := &peeradapter.Server{
		Verifier: &corepeer.Verifier{Keys: noPeers{}, Nonces: noPeers{}, Now: now},
		Signer: func(context.Context) (corepeer.Signer, error) {
			return corepeer.Signer{}, fmt.Errorf("peer signer: %w", kernel.ErrNotFound)
		},
	}
	if st != nil {
		server.Verifier = &corepeer.Verifier{Keys: st.Peers(), Nonces: st.RequestNonces(), Now: now}
		server.Signer = instanceSigner(st, now)
	}
	return func(next http.Handler) http.Handler {
		guarded := server.Middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if peerRequest(r) {
				guarded.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// noPeers is the peer records and replay cache of a router without a
// store: it knows no peer and keeps no nonce.
type noPeers struct{}

func (noPeers) Get(_ context.Context, id string) (model.Peer, error) {
	return model.Peer{}, fmt.Errorf("peer %s: %w", id, kernel.ErrNotFound)
}

func (noPeers) SetProtocolVersion(_ context.Context, id string, _ int) error {
	return fmt.Errorf("peer %s: %w", id, kernel.ErrNotFound)
}

func (noPeers) Remember(context.Context, []byte, time.Time, time.Time) error {
	return fmt.Errorf("peer request nonces: %w", kernel.ErrStore)
}

func (noPeers) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

// instanceSigner signs with this instance's identity, read once: the key
// is created at first start and nothing rotates it while the process runs.
// A failed read is retried on the next call rather than remembered.
func instanceSigner(st instance.Store, now func() time.Time) func(context.Context) (corepeer.Signer, error) {
	var (
		mu     sync.Mutex
		signer *corepeer.Signer
	)
	return func(ctx context.Context) (corepeer.Signer, error) {
		mu.Lock()
		defer mu.Unlock()
		if signer != nil {
			return *signer, nil
		}
		if err := instance.EnsureInstance(ctx, st); err != nil {
			return corepeer.Signer{}, fmt.Errorf("peer signer: %w", err)
		}
		id, err := st.Instance().Get(ctx)
		if err != nil {
			return corepeer.Signer{}, fmt.Errorf("peer signer: %w", err)
		}
		signer = &corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey, Now: now}
		return *signer, nil
	}
}

// PeerClientStore is what the peer client needs of the metadata store: this
// instance's identity, whose key signs requests, and the proxy
// configurations it takes the platform default from.
type PeerClientStore interface {
	instance.Store
	Configs() configdoc.ConfigStore
}

// newPeerClients is the signing peer client of decision 0021 over st: it
// signs with this instance's key, goes through the platform default proxy
// as single push does, verifies TLS against the system roots, and reaches
// private addresses only where allowedPrivate grants them, as the remote
// and push clients' remote.allowed_private_origins does.
func newPeerClients(st PeerClientStore, allowedPrivate []string, now func() time.Time) (*peeradapter.Factory, error) {
	f, err := peeradapter.NewFactory(peeradapter.ClientOptions{
		Signer: instanceSigner(st, now),
		Proxy: func(ctx context.Context) (*url.URL, *outbound.NTLMProxy, error) {
			return pushhttp.PlatformProxy(ctx, st.Configs())
		},
		AllowedPrivateOrigins: allowedPrivate,
		Now:                   now,
	})
	if err != nil {
		return nil, fmt.Errorf("peer clients: %w", err)
	}
	return f, nil
}
