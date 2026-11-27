package federation_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type signedCleanupServer struct {
	peergen.UnimplementedStrict
	cleanup peeradapter.RepositoryServer
}

func (s signedCleanupServer) ObserveRepositoryDownloads(
	ctx context.Context,
	req peergen.ObserveRepositoryDownloadsRequestObject,
) (peergen.ObserveRepositoryDownloadsResponseObject, error) {
	return s.cleanup.ObserveRepositoryDownloads(ctx, req)
}

type signedCleanupNode struct {
	*world
	key    string
	server *httptest.Server
	signer func(context.Context) (corepeer.Signer, error)
}

func newSignedCleanupNode(t *testing.T, key string, wrap func(peeradapter.CleanupReceiver) peeradapter.CleanupReceiver) *signedCleanupNode {
	t.Helper()
	return signedCleanupNodeOver(t, key, newWorldWithKey(t, key, 0, 1), wrap)
}

func signedCleanupNodeOver(t *testing.T, key string, w *world, wrap func(peeradapter.CleanupReceiver) peeradapter.CleanupReceiver) *signedCleanupNode {
	t.Helper()
	n := &signedCleanupNode{world: w, key: key}
	n.signer = func(ctx context.Context) (corepeer.Signer, error) {
		id, err := n.store.Instance().Get(ctx)
		return corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey}, err
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use((&peeradapter.Server{Verifier: &corepeer.Verifier{Keys: n.store.Peers(), Nonces: n.store.RequestNonces()}, Signer: n.signer}).Middleware)
	var receiver peeradapter.CleanupReceiver = n.service
	if wrap != nil {
		receiver = wrap(receiver)
	}
	opts := peergen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	peergen.HandlerWithOptions(
		peergen.NewStrictHandlerWithOptions(signedCleanupServer{cleanup: peeradapter.RepositoryServer{Cleanup: receiver}}, nil, opts),
		peergen.ChiServerOptions{BaseURL: peeradapter.Prefix, BaseRouter: r, ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest)},
	)
	n.server = httptest.NewServer(r)
	t.Cleanup(n.server.Close)
	n.service.BaseURL = n.server.URL
	return n
}

func connectCleanupNodes(t *testing.T, a, b *signedCleanupNode) {
	t.Helper()
	for _, pair := range [][2]*signedCleanupNode{{a, b}, {b, a}} {
		local, remote := pair[0], pair[1]
		id := storekit.Get[instance.InstanceIdentity](t)(remote.store.Instance().Get(t.Context()))
		peer := model.Peer{InstanceID: id.InstanceID, PublicKey: id.PublicKey, BaseURL: remote.server.URL, ProtocolVersion: 1, UseCases: []model.UseCase{model.UseFederatedRepo}}
		storekit.Must(t, local.store.Peers().Put(t.Context(), peer))
		cfg := storekit.Get[repo.Config](t)(local.artifacts.Repos().Get(t.Context(), local.key))
		cfg.Members = append(cfg.Members, repo.Member{URL: remote.server.URL + "/artifactory/" + remote.key, Enabled: repo.Flex(true)})
		storekit.Must(t, local.artifacts.Repos().Update(t.Context(), cfg))
		storekit.Must(
			t,
			local.store.Federation().
				SetMembers(t.Context(), local.key, []model.FederationMember{{PeerInstanceID: id.InstanceID, BaseURL: remote.server.URL + "/artifactory", RemoteRepoKey: remote.key, Enabled: true}}),
		)
		factory := storekit.Get[*peeradapter.Factory](
			t,
		)(
			peeradapter.NewFactory(peeradapter.ClientOptions{Signer: local.signer, AllowedPrivateOrigins: []string{remote.server.URL}, Attempts: 3}),
		)
		local.service.Remote = peeradapter.CleanupRemote{Factory: factory}
	}
}

// Full signed RPCs are assembled with both owners' actual metadata readers.
// Pairing/signature mechanics retain their existing adapter contract tests.
func TestAccCleanupSignedRepositoryBinding(t *testing.T) {
	for _, name := range []string{"different_keys_same_kind", "different_kind_collision", "wrong_reciprocal_target", "fresh_challenge", "deadline"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var previous cleanup.Answer
			wrap := func(receiver peeradapter.CleanupReceiver) peeradapter.CleanupReceiver {
				return remoteFunc(func(ctx context.Context, p model.Peer, key string, r cleanup.Request) (cleanup.Answer, error) {
					call := calls.Add(1)
					if name == "deadline" {
						<-ctx.Done()
						return cleanup.Answer{}, ctx.Err()
					}
					mu.Lock()
					defer mu.Unlock()
					if name == "fresh_challenge" && call > 1 {
						return previous, nil
					}
					out, err := receiver.Downloads(ctx, p, key, r)
					previous = out
					return out, err
				})
			}
			a, b := newSignedCleanupNode(t, "source-npm", nil), newSignedCleanupNode(t, "target-npm", wrap)
			connectCleanupNodes(t, a, b)
			if name == "different_kind_collision" {
				cfg := storekit.Get[repo.Config](t)(b.artifacts.Repos().Get(t.Context(), b.key))
				cfg.PackageType = repo.Maven
				storekit.Must(t, b.artifacts.Repos().Update(t.Context(), cfg))
			}
			if name == "wrong_reciprocal_target" {
				cfg := storekit.Get[repo.Config](t)(b.artifacts.Repos().Get(t.Context(), b.key))
				cfg.Members[1].URL = a.server.URL + "/artifactory/other-source"
				storekit.Must(t, b.artifacts.Repos().Update(t.Context(), cfg))
				id := storekit.Get[instance.InstanceIdentity](t)(a.store.Instance().Get(t.Context()))
				storekit.Must(
					t,
					b.store.Federation().
						SetMembers(t.Context(), b.key, []model.FederationMember{{PeerInstanceID: id.InstanceID, BaseURL: a.server.URL + "/artifactory", RemoteRepoKey: "other-source", Enabled: true}}),
				)
			}
			scope := storekit.Get[api.CleanupScope](t)(a.service.Scope(t.Context(), a.key))
			started := time.Now()
			proof, err := a.service.Collect(t.Context(), scope, a.versions)
			if name == "different_keys_same_kind" || name == "fresh_challenge" {
				storekit.Must(t, err)
				if proof.Scope.PackageType != "npm" || len(proof.Pins) != 2 {
					t.Fatal("signed same-kind observation lost its identity", proof)
				}
				if name == "fresh_challenge" {
					_, err = a.service.Collect(t.Context(), scope, a.versions)
					if !errors.Is(err, cleanup.ErrUnavailable) {
						t.Fatal("a previous answer was reused for a new challenge", err)
					}
					if calls.Load() != 2 {
						t.Fatal("unexpected observation attempts", calls.Load())
					}
				}
			} else {
				if !errors.Is(err, cleanup.ErrUnavailable) {
					t.Fatal("unsafe remote observation became evidence", name, err)
				}
				if calls.Load() != 1 {
					t.Fatal("failed signed observation was retried", calls.Load())
				}
				if name == "deadline" && time.Since(started) > 2*cleanup.AttemptTimeout {
					t.Fatal("silent member exceeded its observation bound", time.Since(started))
				}
			}
		})
	}
}

func (s signedCleanupServer) CoordinateRepositoryCleanup(
	ctx context.Context,
	request peergen.CoordinateRepositoryCleanupRequestObject,
) (peergen.CoordinateRepositoryCleanupResponseObject, error) {
	return s.cleanup.CoordinateRepositoryCleanup(ctx, request)
}
