package retention_test

import (
	"bytes"
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	fedapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	fedcleanup "github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retcleanup "github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

type cleanupCollectedBarrier struct {
	fedapi.CleanupCoordination
	entered, release chan struct{}
}

func (b cleanupCollectedBarrier) Collect(ctx context.Context, scope fedapi.CleanupScope, versions []fedapi.CleanupVersion) (fedapi.CleanupEvidence, error) {
	proof, err := b.CleanupCoordination.Collect(ctx, scope, versions)
	if err != nil {
		return proof, err
	}
	// This explicit post-collection check must pass before the test admits
	// the SQL writer. The negative control retains it and omits only later
	// checks, so it tests placement after waiting rather than mere presence.
	if err := b.CleanupCoordination.Fresh(ctx, proof); err != nil {
		return fedapi.CleanupEvidence{}, err
	}
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return fedapi.CleanupEvidence{}, ctx.Err()
	}
	return proof, nil
}

type cleanupPublicationSQL struct {
	store *appsql.Store
	pool  *sql.DB
}

// A real transaction holds the sole backend connection and administration row
// after signed collection. WaitCount proves publication queued for that SQL
// connection before the clock advances or the writer commits. This covers a
// pre-BEGIN resource wait on both drivers; it does not claim SQL-row arrival.
func TestAccFederatedCleanupPublicationWaitSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) cleanupPublicationSQL {
			return cleanupPublicationSQL{appsql.New(s.DB(), s.Dialect(), keys), s.DB()}
		},
		func(t *testing.T, newStore func(*testing.T) cleanupPublicationSQL) {
			for _, change := range []string{"expiry", "grant", "download", "credential"} {
				t.Run(change, func(t *testing.T) {
					database := newStore(t)
					f := newCleanupExecutorFixture(t, database.store)
					f.now = time.Now()
					signedExecutionPeer(t, f)
					revokedHash := accesstest.HashPassword(t, "revoked-behind-publication-row")
					var advance atomic.Int64
					service := f.policies.Federation.(*fedcleanup.Service)
					service.Now = func() time.Time { return f.now.Add(time.Duration(advance.Load())) }
					run, item := f.selected(t)
					before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
					database.pool.SetMaxOpenConns(1)
					barrier := cleanupCollectedBarrier{CleanupCoordination: service, entered: make(chan struct{}), release: make(chan struct{})}
					f.executor.Policies.Federation = barrier
					done := make(chan error, 1)
					go func() {
						_, err := f.executor.Step(f.ctx, run.ID, func(ctx context.Context) error {
							_, err := f.db.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
							return err
						})
						done <- err
					}()
					var wireOnce sync.Once
					releaseWire := func() { wireOnce.Do(func() { close(barrier.release) }) }
					defer releaseWire()
					select {
					case <-barrier.entered:
					case <-f.ctx.Done():
						t.Fatal("worker did not collect the signed response")
					}
					locked, releaseSQL := make(chan struct{}), make(chan struct{})
					sqlDone := make(chan error, 1)
					go func() {
						sqlDone <- f.db.Tx(f.ctx, func(ctx context.Context) error {
							if err := f.db.Projects().Guard(ctx); err != nil {
								return err
							}
							close(locked)
							select {
							case <-releaseSQL:
							case <-ctx.Done():
								return ctx.Err()
							}
							switch change {
							case "grant":
								st := f.db.(fedcleanup.Store)
								peers, err := st.Peers().List(ctx, fedport.PeerFilter{})
								if err != nil {
									return err
								}
								return st.Peers().Put(ctx, peers[0])
							case "credential":
								user, err := f.db.Users().Get(ctx, "admin")
								if err != nil {
									return err
								}
								user.PasswordHash = revokedHash
								return f.db.Users().Update(ctx, user)
							case "download":
								return f.db.Items().RecordDownload(ctx, f.ids[0], artport.Download{By: "reader-after-observation", At: f.now})
							}
							return nil
						})
					}()
					var sqlOnce sync.Once
					unblockSQL := func() { sqlOnce.Do(func() { close(releaseSQL) }) }
					defer unblockSQL()
					select {
					case <-locked:
					case err := <-sqlDone:
						t.Fatal("writer could not acquire authority row", err)
					case <-f.ctx.Done():
						t.Fatal("SQL writer did not start")
					}
					waits := database.pool.Stats().WaitCount
					releaseWire()
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for database.pool.Stats().WaitCount == waits {
						select {
						case err := <-done:
							t.Fatal("publication never queued for the held backend connection", err)
						case <-f.ctx.Done():
							t.Fatal("publication did not reach the SQL connection wait")
						case <-ticker.C:
						}
					}
					if change == "expiry" {
						advance.Store(int64(fedcleanup.Window))
					}
					unblockSQL()
					storekit.Must(t, <-sqlDone)
					select {
					case err := <-done:
						storekit.Must(t, err)
					case <-f.ctx.Done():
						t.Fatal("publication did not settle")
					}
					f.preserved(t)
					current := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
					if change == "credential" {
						if current.Phase != model.CleanupFinished || current.Reason != "authority_revoked" {
							t.Fatal("SQL wait retained revoked credential", current.Phase, current.Reason)
						}
					} else if current.Phase != model.CleanupSkipping || current.Reason != string(retcleanup.ReasonChanged) {
						t.Fatal("SQL wait did not refuse changed evidence", change, current.Phase, current.Reason)
					}
					storekit.Same(t, "refusal preserves original version identity", before.Version, current.Version)
					after := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
					if after.CleanedItems != 0 {
						t.Fatal("invalid proof published an effect", after)
					}
				})
			}
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}
