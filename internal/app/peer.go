package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
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
// (decision 0021, Transport). Requests elsewhere pass untouched. With no
// store there are no peers, and every peer request is refused.
func peerGuard(st PeerStore, now func() time.Time) func(http.Handler) http.Handler {
	if st == nil {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !peerRequest(r) {
					next.ServeHTTP(w, r)
					return
				}
				httpkit.WriteErrProfile(w, r, apierror.ProfileGrid,
					apierror.New(http.StatusUnauthorized, "The signing key belongs to no paired peer"))
			})
		}
	}
	server := &peeradapter.Server{
		Verifier: &corepeer.Verifier{Keys: st.Peers(), Nonces: st.RequestNonces(), Now: now},
		Signer:   instanceSigner(st, now),
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
