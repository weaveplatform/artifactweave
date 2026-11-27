package retention_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	fedcleanup "github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

type cleanupSignedBarrier struct {
	peeradapter.CleanupReceiver
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (b *cleanupSignedBarrier) Downloads(ctx context.Context, p fedmodel.Peer, key string, request fedcleanup.Request) (fedcleanup.Answer, error) {
	answer, err := b.CleanupReceiver.Downloads(ctx, p, key, request)
	if err == nil && b.armed.Load() {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return fedcleanup.Answer{}, ctx.Err()
		}
	}
	return answer, err
}

type cleanupExecutionPeerServer struct {
	peergen.UnimplementedStrict
	server peeradapter.RepositoryServer
}

func (s cleanupExecutionPeerServer) ObserveRepositoryDownloads(
	ctx context.Context,
	r peergen.ObserveRepositoryDownloadsRequestObject,
) (peergen.ObserveRepositoryDownloadsResponseObject, error) {
	return s.server.ObserveRepositoryDownloads(ctx, r)
}

// Only the peer transport and the peer's independent metadata are assembled
// here; cleanup still runs through the local module's real credential restore.
func signedExecutionPeer(t *testing.T, f *cleanupExecutorFixture) *cleanupSignedBarrier {
	t.Helper()
	remote := newCleanupExecutorFixture(t, inmem.New())
	remote.now = f.now
	enableFederatedExecutor(t, f)
	enableFederatedExecutor(t, remote)
	localStore, remoteStore := f.db.(fedcleanup.Store), remote.db.(fedcleanup.Store)
	localID := storekit.Get[instance.InstanceIdentity](t)(localStore.Instance().Get(f.ctx))
	remoteID := storekit.Get[instance.InstanceIdentity](t)(remoteStore.Instance().Get(f.ctx))
	core := remote.policies.Federation.(*fedcleanup.Service)
	barrier := &cleanupSignedBarrier{CleanupReceiver: core, entered: make(chan struct{}), release: make(chan struct{})}
	signer := func(ctx context.Context) (corepeer.Signer, error) {
		id, err := remoteStore.Instance().Get(ctx)
		return corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey}, err
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use((&peeradapter.Server{Verifier: &corepeer.Verifier{Keys: remoteStore.Peers(), Nonces: remoteStore.(interface {
		RequestNonces() fedport.RequestNonceStore
	}).RequestNonces()}, Signer: signer}).Middleware)
	opts := peergen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	peergen.HandlerWithOptions(
		peergen.NewStrictHandlerWithOptions(cleanupExecutionPeerServer{server: peeradapter.RepositoryServer{Cleanup: barrier}}, nil, opts),
		peergen.ChiServerOptions{BaseURL: peeradapter.Prefix, BaseRouter: r, ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest)},
	)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	core.BaseURL = server.URL
	for _, side := range []struct {
		fixture *cleanupExecutorFixture
		peer    instance.InstanceIdentity
		base    string
	}{{f, remoteID, server.URL}, {remote, localID, "https://self.example"}} {
		st := side.fixture.db.(fedcleanup.Store)
		storekit.Must(t, st.Peers().Delete(f.ctx, "member"))
		storekit.Must(
			t,
			st.Peers().
				Put(f.ctx, fedmodel.Peer{InstanceID: side.peer.InstanceID, PublicKey: side.peer.PublicKey, BaseURL: side.base, ProtocolVersion: 1, UseCases: []fedmodel.UseCase{fedmodel.UseFederatedRepo}}),
		)
		cfg := storekit.Get[repo.Config](t)(side.fixture.db.Repos().Get(f.ctx, "execution"))
		cfg.Members[1].URL = side.base + "/artifactory/execution"
		storekit.Must(t, side.fixture.db.Repos().Update(f.ctx, cfg))
		storekit.Must(
			t,
			st.Federation().
				SetMembers(f.ctx, cfg.Key, []fedmodel.FederationMember{{PeerInstanceID: side.peer.InstanceID, BaseURL: side.base + "/artifactory", RemoteRepoKey: cfg.Key, Enabled: true}}),
		)
	}
	factory := storekit.Get[*peeradapter.Factory](t)(peeradapter.NewFactory(peeradapter.ClientOptions{Signer: func(context.Context) (corepeer.Signer, error) {
		return corepeer.Signer{KeyID: localID.InstanceID, Key: localID.PrivateKey}, nil
	}, AllowedPrivateOrigins: []string{server.URL}, Attempts: 3}))
	f.policies.Federation.(*fedcleanup.Service).Remote = peeradapter.CleanupRemote{Factory: factory}
	f.executor.Policies = f.policies
	return barrier
}

func TestAccFederatedSignedExecutionAuthoritySQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			for _, change := range []string{"credential", "stop", "lease", "recreate"} {
				t.Run(change, func(t *testing.T) {
					f := newCleanupExecutorFixture(t, newStore(t))
					f.now = time.Now()
					barrier := signedExecutionPeer(t, f)
					run, item := f.selected(t)
					before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
					barrier.armed.Store(true)
					done := make(chan error, 1)
					go func() { done <- f.executor.Execute(f.ctx, item) }()
					var release sync.Once
					unblock := func() { release.Do(func() { close(barrier.release) }) }
					defer unblock()
					select {
					case <-barrier.entered:
					case <-f.ctx.Done():
						t.Fatal("worker never reached signed response barrier")
					}
					var replacement work.WorkItem
					switch change {
					case "credential":
						user := storekit.Get[accesstest.User](t)(f.db.Users().Get(f.ctx, "admin"))
						user.PasswordHash = accesstest.HashPassword(t, "rotated-during-signed-response")
						storekit.Must(t, f.db.Users().Update(f.ctx, user))
					case "stop":
						storekit.Must(t, f.policies.Stop(f.caller, run.ID))
					case "lease":
						storekit.Must(t, f.db.WorkQueue().Release(f.ctx, item.Kind, item.Key, item.Generation, f.now))
						var taken bool
						var err error
						replacement, taken, err = f.db.WorkQueue().
							LeaseKey(f.ctx, item.Kind, item.Key, work.WorkLease{Holder: "replacement", Now: f.now, Until: f.now.Add(time.Hour)})
						storekit.Must(t, err)
						if !taken {
							t.Fatal("replacement lease was not acquired")
						}
					case "recreate":
						cfg := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
						storekit.Must(t, f.db.Repos().Delete(f.ctx, cfg.Key))
						storekit.Must(t, f.db.Repos().Create(f.ctx, cfg))
					}
					unblock()
					var err error
					select {
					case err = <-done:
					case <-f.ctx.Done():
						t.Fatal("signed worker failed to settle")
					}
					after := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
					if after.CleanedItems != 0 {
						t.Fatal("stale signed response authorized deletion", change, after)
					}
					if change == "lease" {
						if !errors.Is(err, kernel.ErrConflict) {
							t.Fatal("old worker did not lose lease authority", err)
						}
						err = f.db.Tx(f.ctx, func(ctx context.Context) error {
							_, err := f.db.WorkQueue().GuardLease(ctx, replacement.Kind, replacement.Key, replacement.Generation)
							return err
						})
						storekit.Must(t, err)
						storekit.Same(t, "old worker preserves replacement continuation", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
					} else {
						storekit.Must(t, err)
						if change == "credential" && after.Status != retapi.RunFailed || change == "stop" && after.Status != retapi.RunStopped {
							t.Fatal("changed authority had wrong terminal result", change, after.Status)
						}
					}
					f.preserved(t)
				})
			}
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}
