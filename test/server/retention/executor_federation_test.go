package retention_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	fedmodule "github.com/weaveplatform/artifactweave/internal/federation"
	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	fedcleanup "github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type cleanupRemoteState struct {
	err                          error
	revision, incarnation, grant string
	downloaded, created          time.Time
	calls                        int
	intents                      fedport.CleanupIntentStore
}

func (r *cleanupRemoteState) Downloads(ctx context.Context, peer fedmodel.Peer, _ string, request fedcleanup.Request) (fedcleanup.Answer, error) {
	if err := ctx.Err(); err != nil {
		return fedcleanup.Answer{}, err
	}
	// This fixture uses one remote; the production collector's parallel batch
	// contract is exercised separately at maximum membership.
	r.calls++
	if r.err != nil {
		return fedcleanup.Answer{}, r.err
	}
	out := fedcleanup.Answer{
		Instance:    peer.InstanceID,
		Incarnation: r.incarnation,
		Generation:  "config-1",
		Grant:       r.grant,
		Challenge:   request.Challenge,
		PackageType: request.PackageType,
	}
	for _, version := range request.Versions {
		out.Versions = append(
			out.Versions,
			federationapi.CleanupDownload{CleanupVersion: version, ID: 1, Revision: r.revision, AdmissionRevision: "/", Created: r.created, LastDownloaded: r.downloaded},
		)
	}
	return out, nil
}

func enableFederatedExecutor(t *testing.T, f *cleanupExecutorFixture) *cleanupRemoteState {
	t.Helper()
	db := f.db.(fedcleanup.Store)
	cfg := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
	cfg.Class = repo.Federated
	cfg.Members = []repo.Member{{URL: repo.SelfMemberURL(cfg.Key), Enabled: repo.Flex(true)}, {URL: "https://member.example/artifactory/execution", Enabled: repo.Flex(true)}}
	storekit.Must(t, f.db.Repos().Update(f.ctx, cfg))
	storekit.Must(
		t,
		db.Peers().Put(f.ctx, fedmodel.Peer{InstanceID: "member", BaseURL: "https://member.example", ProtocolVersion: 1, UseCases: []fedmodel.UseCase{fedmodel.UseFederatedRepo}}),
	)
	storekit.Must(
		t,
		db.Federation().
			SetMembers(f.ctx, cfg.Key, []fedmodel.FederationMember{{PeerInstanceID: "member", BaseURL: "https://member.example/artifactory", RemoteRepoKey: cfg.Key, Enabled: true}}),
	)
	remote := &cleanupRemoteState{intents: db.(interface {
		CleanupIntents() fedport.CleanupIntentStore
	}).CleanupIntents(), revision: "downloads-1", incarnation: "remote-lifetime", grant: "remote-grant", created: f.now.Add(-48 * time.Hour)}
	f.policies.Federation = &fedcleanup.Service{
		Store:    db,
		Metadata: pkgindex.NewCleanupMetadata(f.db),
		Exclusions: f.db.(interface {
			CleanupExclusions() artifactsapi.CleanupExclusions
		}).CleanupExclusions(),
		BaseURL: "https://self.example",
		Remote:  remote,
		Now:     func() time.Time { return f.now },
	}
	// Attach the real FED02 native producer even when delivery is dormant.
	module := storekit.Get[*fedmodule.Module](
		t,
	)(
		fedmodule.New(
			fedmodule.Deps{
				Store:        f.db.(fedmodule.Store),
				Access:       accesstest.Over(t, f.db).API(),
				Repositories: f.artifacts.Repositories,
				Lifetimes:    f.artifacts.Lifetimes,
				BaseURL:      "https://self.example",
			},
		),
	)
	feed := f.db.(interface{ SetItemFeed(artifactsapi.ItemFeed) })
	t.Cleanup(func() { module.Close(); feed.SetItemFeed(nil) })
	_, err := module.OpenStreams(fedmodule.StreamOptions{Blobs: f.blobs, Remote: cleanupDormantRemote{}, Queue: f.queue, Items: f.artifacts.FederatedItems, Feed: feed})
	storekit.Must(t, err)
	f.executor.Policies = f.policies
	return remote
}

func TestAccFederatedCleanupExecutorReference(t *testing.T) {
	federatedExecutorContract(t, func(*testing.T) harness.Store { return inmem.New() })
}

func TestAccFederatedCleanupExecutorSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			federatedExecutorContract(t, func(t *testing.T) harness.Store { return newStore(t) })
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func federatedExecutorContract(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, name := range []string{"ordinary_removal", "remote_download", "remote_incarnation", "remote_grant", "local_grant_replacement", "member_ABA", "offline", "expiry_after_lease_wait", "acknowledgement_rollback", "resume_65_files"} {
		t.Run(name, func(t *testing.T) {
			var f *cleanupExecutorFixture
			if name == "resume_65_files" {
				f = cleanupLargeVersion(t, newStore(t))
			} else {
				f = newCleanupExecutorFixture(t, newStore(t))
			}
			remote := enableFederatedExecutor(t, f)
			run, item := f.selected(t)
			progress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
			if progress.Federation == nil || len(progress.Federation.Pins) != 2 {
				t.Fatal("selected federation candidate lost its private current-member evidence")
			}
			fence := func(ctx context.Context) error {
				_, err := f.db.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
				return err
			}
			switch name {
			case "remote_download":
				remote.revision = "download-2"
				remote.downloaded = f.now
			case "remote_incarnation":
				remote.incarnation = "recreated-remote"
			case "remote_grant":
				remote.grant = "remote-repaired"
			case "local_grant_replacement":
				st := f.db.(fedcleanup.Store)
				peer := storekit.Get[fedmodel.Peer](t)(st.Peers().Get(f.ctx, "member"))
				storekit.Must(t, st.Peers().Put(f.ctx, peer))
			case "member_ABA":
				cfg := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
				cfg.Members[1].Enabled = repo.Flex(false)
				storekit.Must(t, f.db.Repos().Update(f.ctx, cfg))
				cfg.Members[1].Enabled = repo.Flex(true)
				storekit.Must(t, f.db.Repos().Update(f.ctx, cfg))
			case "offline":
				remote.err = errors.New("member disconnected")
			case "expiry_after_lease_wait":
				expire := func(ctx context.Context) error {
					if err := fence(ctx); err != nil {
						return err
					}
					f.now = f.now.Add(fedcleanup.Window)
					return nil
				}
				_, err := f.executor.Step(f.ctx, run.ID, expire)
				storekit.WantErr(t, err, kernel.ErrConflict)
				f.preserved(t)
				federatedRollbackContinuation(t, progress, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
				return
			case "acknowledgement_rollback":
				armed := true
				faulty := f.executor
				faulty.Store = cleanupFaultStore{f.store, &armed}
				_, err := faulty.Step(f.ctx, run.ID, fence)
				storekit.WantErr(t, err, errCleanupEffectAck)
				f.preserved(t)
				federatedRollbackContinuation(t, progress, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
			}
			if name == "resume_65_files" {
				_, err := f.executor.Step(f.ctx, run.ID, fence)
				storekit.Must(t, err)
				resumed := storekit.Get[cleanup.Executor](t)(f.module.Executor())
				resumed.Policies = f.policies
				f.executor = resumed
			}
			storekit.Must(t, f.executor.Execute(f.ctx, item))
			finished := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
			want := int64(0)
			if name == "ordinary_removal" || name == "acknowledgement_rollback" {
				want = 2
			}
			if name == "resume_65_files" {
				want = 65
			}
			if finished.Status != retapi.RunCompleted || finished.CleanedItems != want {
				t.Fatalf("federation cleanup status=%s cleaned=%d want=%d", finished.Status, finished.CleanedItems, want)
			}
			if want == 0 {
				f.preserved(t)
			}
			report := support.Do(
				t,
				f.handler,
				http.MethodGet,
				cleanupReports+"summaryReports?policyKey=execution-policy&triggerId="+strconv.FormatInt(run.ID, 10),
				support.Basic("admin", "pw"),
				"",
			)
			if report.Code != http.StatusOK {
				t.Fatal("federated cleanup report changed its public answer", report.Code)
			}
			for _, private := range []string{"member.example", remote.incarnation, remote.grant, remote.revision} {
				if strings.Contains(report.Body.String(), private) {
					t.Fatal("public cleanup report exposed private member evidence")
				}
			}

			if remote.calls < 1 {
				t.Fatal("federation selection never obtained a member observation")
			}
			storekit.Must(t, f.db.WorkQueue().Complete(f.ctx, item.Kind, strconv.FormatInt(run.ID, 10), item.Generation, f.now))
		})
	}
}

// Legacy executor mutation tests isolate local authorization/effect behavior.
// Actual remote barrier and signed-outcome contracts have independent owners in
// TestAccCleanupCoordinatedExclusionSQL and the process-level native scenarios.
func (r *cleanupRemoteState) Coordinate(ctx context.Context, _ fedmodel.Peer, _ string, request fedcleanup.CoordinationRequest) (fedcleanup.CoordinationAnswer, error) {
	if r.err != nil {
		return fedcleanup.CoordinationAnswer{}, r.err
	}
	if request.Action == "prepare" {
		return fedcleanup.CoordinationAnswer{State: "prepared", Exclusion: request.Exclusion}, nil
	}
	intent, err := r.intents.Read(ctx, request.Exclusion.ID)
	if err != nil {
		return fedcleanup.CoordinationAnswer{}, err
	}
	return fedcleanup.CoordinationAnswer{State: string(intent.State), Watermark: intent.Watermark, Exclusion: request.Exclusion}, nil
}

func federatedRollbackContinuation(t *testing.T, before, after model.CleanupProgress) {
	t.Helper()
	if after.CleanupIntentID == "" || after.Revision != before.Revision+1 {
		t.Fatal("failed native effect lost its durably prepared recovery reference", after.CleanupIntentID, after.Revision)
	}
	after.CleanupIntentID = ""
	after.Revision = before.Revision
	storekit.Same(t, "rollback preserves every original traversal/effect fact", before, after)
}
