package retention_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	fedcleanup "github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

type candidateFederationBoundary struct {
	federationapi.Cleanup
	db                            app.RouterStore
	outcome                       string
	duringScope, duringCollection func()
	outsideScope                  atomic.Bool
}

func (c *candidateFederationBoundary) Scope(ctx context.Context, key string) (federationapi.CleanupScope, error) {
	if !c.db.InTx(ctx) {
		c.outsideScope.Store(true)
	}
	scope, err := c.Cleanup.Scope(ctx, key)
	if err != nil {
		return scope, err
	}
	if c.duringScope != nil {
		c.duringScope()
	}
	if c.outcome == "receiver" {
		scope.Receiver = true
	}
	if c.outcome == "unavailable" {
		return scope, federationapi.ErrCleanupUnavailable
	}
	return scope, nil
}

func (c *candidateFederationBoundary) Collect(
	ctx context.Context,
	scope federationapi.CleanupScope,
	versions []federationapi.CleanupVersion,
) (federationapi.CleanupEvidence, error) {
	proof, err := c.Cleanup.Collect(ctx, scope, versions)
	if err != nil {
		return proof, err
	}
	if c.duringCollection != nil {
		c.duringCollection()
	}

	return proof, nil
}

type candidateRemoteObservation func(context.Context, fedmodel.Peer, string, fedcleanup.Request) (fedcleanup.Answer, error)

func (f candidateRemoteObservation) Downloads(ctx context.Context, peer fedmodel.Peer, key string, request fedcleanup.Request) (fedcleanup.Answer, error) {
	return f(ctx, peer, key, request)
}

type candidateAfterEvaluation struct {
	artifactsapi.PackageIndex
	db     app.RouterStore
	calls  int
	change func()
}

func (c *candidateAfterEvaluation) Version(ctx context.Context, ref artifactsapi.IndexVersion, keep int) (artifactsapi.IndexVersion, error) {
	value, err := c.PackageIndex.Version(ctx, ref, keep)
	if err == nil {
		c.calls++
		if c.calls == 2 {
			c.db.AfterCommit(ctx, c.change)
		}
	}
	return value, err
}

func newFederatedSelection(t *testing.T, db app.RouterStore, outcome string) (*selectionWorld, *cleanup.Selection, artifactsapi.IndexVersion, *candidateFederationBoundary) {
	t.Helper()
	w := newSelectionWorld(t, db)
	storekit.Must(t, instance.EnsureInstance(t.Context(), db))
	w.repository("alpha-federated", "alpha", repo.NPM)
	for _, v := range []string{"1.0.0", "2.0.0"} {
		w.version("alpha-federated", repo.NPM, "widget", v, []string{npm.CanonicalPath("widget", v)})
	}
	config := storekit.Get[repo.Config](t)(db.Repos().Get(t.Context(), "alpha-federated"))
	config.Class = repo.Federated
	config.Members = []repo.Member{{URL: repo.SelfMemberURL(config.Key), Enabled: repo.Flex(true)}}
	config.Members = append(config.Members, repo.Member{URL: "https://member.example/artifactory/remote", Enabled: repo.Flex(true)})
	storekit.Must(t, db.Repos().Update(t.Context(), config))
	st := db.(fedcleanup.Store)
	storekit.Must(
		t,
		st.Peers().
			Put(t.Context(), fedmodel.Peer{InstanceID: "member", BaseURL: "https://member.example", ProtocolVersion: 1, UseCases: []fedmodel.UseCase{fedmodel.UseFederatedRepo}}),
	)
	storekit.Must(
		t,
		st.Federation().
			SetMembers(t.Context(), config.Key, []fedmodel.FederationMember{{PeerInstanceID: "member", BaseURL: "https://member.example/artifactory", RemoteRepoKey: "remote", Enabled: true}}),
	)
	core := &fedcleanup.Service{
		Store:    db.(fedcleanup.Store),
		Metadata: pkgindex.NewCleanupMetadata(db, db.Projects().Guard),
		BaseURL:  "https://self.example",
		Now:      func() time.Time { return w.now },
	}
	core.Remote = candidateRemoteObservation(func(_ context.Context, peer fedmodel.Peer, _ string, request fedcleanup.Request) (fedcleanup.Answer, error) {
		value := fedcleanup.Answer{
			Instance:    peer.InstanceID,
			Incarnation: "member-incarnation",
			Generation:  "member-config",
			Grant:       "member-grant",
			PackageType: request.PackageType,
			Challenge:   request.Challenge,
		}
		for _, version := range request.Versions {
			row := federationapi.CleanupDownload{CleanupVersion: version, ID: 1, Revision: "member-downloads", AdmissionRevision: "/", Created: w.now.Add(-48 * time.Hour)}
			if outcome == "recent" {
				row.LastDownloaded = w.now
			}
			value.Versions = append(value.Versions, row)
		}
		return value, nil
	})
	boundary := &candidateFederationBoundary{Cleanup: core, db: db, outcome: outcome}
	w.service.Federation = boundary
	input := selectionInput(repo.NPM)
	_, err := w.service.Create(t.Context(), input.Key, input)
	storekit.Must(t, err)
	selection := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), input.Key))
	repositories, _, err := selection.Repositories(t.Context(), kernel.Page{Limit: 1})
	storekit.Must(t, err)
	packages, _, err := selection.Packages(t.Context(), repositories[0], kernel.Page{Limit: 1})
	storekit.Must(t, err)
	versions, _, err := selection.Versions(t.Context(), packages[0], kernel.Page{Limit: 1})
	storekit.Must(t, err)
	return w, selection, versions[0], boundary
}

func TestAccFederatedCandidateScopeSQL(t *testing.T) {
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), nil) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			for _, outcome := range []string{"selected", "unavailable", "receiver", "recent"} {
				t.Run(outcome, func(t *testing.T) {
					for _, change := range []string{"guarded_scope", "transfer_before_scope", "recreate_before_scope", "revoke_during_scope", "revoke_during_collection"} {
						if change == "revoke_during_collection" && (outcome == "receiver" || outcome == "unavailable") {
							continue
						}
						t.Run(change, func(t *testing.T) {
							w, _, _, boundary := newFederatedSelection(t, newStore(t), outcome)
							if change == "transfer_before_scope" || change == "recreate_before_scope" {
								w.service.PackageIndex = &candidateAfterEvaluation{PackageIndex: w.service.PackageIndex, db: w.db, change: func() {
									config := storekit.Get[repo.Config](t)(w.db.Repos().Get(t.Context(), "alpha-federated"))
									if change == "transfer_before_scope" {
										config.ProjectKey = "beta"
										storekit.Must(t, w.db.Repos().Update(t.Context(), config))
									} else {
										storekit.Must(t, w.db.Repos().Delete(t.Context(), config.Key))
										storekit.Must(t, w.db.Repos().Create(t.Context(), config))
										w.version(config.Key, repo.NPM, "widget", "1.0.0", []string{npm.CanonicalPath("widget", "1.0.0")})
									}
								}}
							}
							if change == "revoke_during_scope" {
								boundary.duringScope = func() { w.authority = "beta" }
							}
							if change == "revoke_during_collection" {
								boundary.duringCollection = func() { w.authority = "beta" }
							}
							selection := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), "selection-policy"))
							repositories, _, err := selection.Repositories(t.Context(), kernel.Page{Limit: 1})
							storekit.Must(t, err)
							packages, _, err := selection.Packages(t.Context(), repositories[0], kernel.Page{Limit: 1})
							storekit.Must(t, err)
							versions, _, err := selection.Versions(t.Context(), packages[0], kernel.Page{Limit: 1})
							storekit.Must(t, err)
							page, err := selection.Candidates(t.Context(), versions[0], kernel.Page{Limit: 1})
							if change == "guarded_scope" {
								storekit.Must(t, err)
								if boundary.outsideScope.Load() {
									t.Fatal("federation scope was acquired outside the shared authority transaction")
								}
								want := map[string]cleanup.Reason{"selected": cleanup.ReasonSelected, "unavailable": cleanup.ReasonFederationUnknown, "receiver": cleanup.ReasonFederationReceiver, "recent": cleanup.ReasonDownloaded}[outcome]
								if page.Reason != want {
									t.Fatalf("reason=%s, want %s", page.Reason, want)
								}
								return
							}
							if change == "recreate_before_scope" && err == nil && page.Reason == cleanup.ReasonChanged && len(page.Items) == 0 {
								return
							}
							if err == nil {
								t.Fatalf("changed authority/context returned a peer-derived outcome: reason=%s items=%d", page.Reason, len(page.Items))
							}
						})
					}
				})
			}
		},
		sqltest.WithPostgres(),
	)
}

// Local, empty federation and explicit self-only bidirectional repositories
// share selection semantics. Receiver direction must remain a refusal even
// without a remote, and current /r relationship direction overrides stale mode.
func TestAccFederatedCandidateDirectionSQL(t *testing.T) {
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), nil) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			for _, mode := range []string{"local", "empty", "self_bidirectional", "self_receiver", "grant_receiver", "two_way_precedence"} {
				t.Run(mode, func(t *testing.T) {
					w, _, _, _ := newFederatedSelection(t, newStore(t), "selected")
					cfg := storekit.Get[repo.Config](t)(w.db.Repos().Get(t.Context(), "alpha-federated"))
					st := w.db.(fedcleanup.Store)
					switch mode {
					case "local":
						cfg.Class = repo.Local
						cfg.Members = nil
					case "empty":
						cfg.Members = nil
					case "self_bidirectional":
						cfg.Members = cfg.Members[:1]
					case "self_receiver":
						cfg.Members = cfg.Members[:1]
						cfg.Members[0].Mode = repo.ModeReceiver
					case "grant_receiver", "two_way_precedence":
						peer := storekit.Get[fedmodel.Peer](t)(st.Peers().Get(t.Context(), "member"))
						peer.UseCases = []fedmodel.UseCase{fedmodel.UseFederatedRepoRead}
						peer.ReceivesFederationOnly = true
						if mode == "two_way_precedence" {
							peer.UseCases = append(peer.UseCases, fedmodel.UseFederatedRepo)
						}
						storekit.Must(t, st.Peers().Put(t.Context(), peer))
					}
					storekit.Must(t, w.db.Repos().Update(t.Context(), cfg))
					if mode != "grant_receiver" && mode != "two_way_precedence" {
						storekit.Must(t, st.Federation().SetMembers(t.Context(), cfg.Key, nil))
					}
					selection := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), "selection-policy"))
					repositories, _, err := selection.Repositories(t.Context(), kernel.Page{Limit: 1})
					storekit.Must(t, err)
					packages, _, err := selection.Packages(t.Context(), repositories[0], kernel.Page{Limit: 1})
					storekit.Must(t, err)
					versions, _, err := selection.Versions(t.Context(), packages[0], kernel.Page{Limit: 1})
					storekit.Must(t, err)
					page, err := selection.Candidates(t.Context(), versions[0], kernel.Page{Limit: 1})
					storekit.Must(t, err)
					if mode == "self_receiver" || mode == "grant_receiver" {
						if page.Reason != cleanup.ReasonFederationReceiver || len(page.Items) != 0 || page.Federation != nil {
							t.Fatal("receiver obtained cleanup authority", page)
						}
					} else if page.Reason != cleanup.ReasonSelected || len(page.Items) != 1 {
						t.Fatal("ordinary/self-only selection diverged", mode, page)
					}
				})
			}
		},
		sqltest.WithPostgres(),
	)
}
