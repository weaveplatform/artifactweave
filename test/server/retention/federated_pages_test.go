package retention_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	fedcleanup "github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	fedport "github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Candidate files, not observation requests, cross this real 64/65 page
// boundary. Each candidate carries actual signed configured-member evidence;
// an old proof never grants a changed lifetime/relationship's deletion rights.
func TestAccFederatedCandidatePageInvalidationSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			for _, change := range []string{"unchanged", "membership", "grant_replacement", "incarnation", "ownership", "credential"} {
				t.Run(change, func(t *testing.T) {
					f := cleanupLargeVersion(t, newStore(t))
					f.now = time.Now()
					signedExecutionPeer(t, f)
					selection := storekit.Get[*cleanup.Selection](t)(f.policies.Select(f.caller, "execution-policy"))
					repositories, _, err := selection.Repositories(f.caller, kernel.Page{Limit: 1})
					storekit.Must(t, err)
					packages, _, err := selection.Packages(f.caller, repositories[0], kernel.Page{Limit: 1})
					storekit.Must(t, err)
					versions, _, err := selection.Versions(f.caller, packages[0], kernel.Page{Limit: 1})
					storekit.Must(t, err)
					first := storekit.Get[cleanup.CandidatePage](t)(selection.Candidates(f.caller, versions[0], kernel.Page{Limit: 64}))
					if first.Reason != cleanup.ReasonSelected || len(first.Items) != 64 || first.Next == "" || first.Federation == nil || len(first.Federation.Pins) != 2 {
						t.Fatal("missing actual configured-member first candidate page", first.Reason, len(first.Items), first.Next)
					}
					proof := *first.Federation
					store := f.db.(fedcleanup.Store)
					switch change {
					case "membership":
						cfg := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
						cfg.Members[1].Enabled = repo.Flex(false)
						storekit.Must(t, f.db.Repos().Update(f.ctx, cfg))
					case "grant_replacement":
						peer := storekit.Get[fedmodel.Peer](t)(store.Peers().Get(f.ctx, proof.Scope.Members[0].Instance))
						storekit.Must(t, store.Peers().Put(f.ctx, peer))
					case "incarnation":
						cfg := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
						_, _, err := f.artifacts.Repositories.Remove(f.caller, cfg.Key, "admin")
						storekit.Must(t, err)
						storekit.Must(t, f.artifacts.Repositories.Create(f.caller, cfg))
						response := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/package/1/one.bin", support.Basic("admin", "pw"), "replacement-incarnation")
						if response.Code != http.StatusCreated {
							t.Fatal("recreate native file", response.Code)
						}
						replacement := storekit.Get[artmodel.Item](t)(f.db.Items().Get(f.ctx, "execution", false, "package/1/one.bin"))
						storekit.Must(
							t,
							pkgindex.Link(f.ctx, f.db, pkgindex.Version{RepoKey: "execution", PackageType: repo.Generic, Package: "package", Version: "1"}, replacement.ID, f.now),
						)
						current := storekit.Get[artmodel.Package](t)(f.db.Packages().Find(f.ctx, "execution", "package"))
						v := storekit.Get[artmodel.PackageVersion](t)(f.db.Packages().Version(f.ctx, current.ID, "1"))
						if v.ID == first.Version.ID {
							t.Fatal("recreated repository reused the old version identity")
						}
					case "ownership":
						cfg := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
						cfg.ProjectKey = "foreign"
						storekit.Must(t, f.db.Repos().Update(f.ctx, cfg))
					case "credential":
						user := storekit.Get[accesstest.User](t)(f.db.Users().Get(f.ctx, "admin"))
						user.PasswordHash = accesstest.HashPassword(t, "changed-between-candidate-pages")
						storekit.Must(t, f.db.Users().Update(f.ctx, user))
					}
					// Guard is a metadata/context fence, not authentication. Credential
					// mutation must instead fail the next public page's original-caller check.
					if change != "unchanged" && change != "credential" {
						err := f.db.Tx(f.ctx, func(ctx context.Context) error { return f.policies.Federation.Guard(ctx, proof) })
						if err == nil {
							t.Fatal("previous page's evidence adopted changed context", change)
						}
					}
					second, err := selection.Candidates(f.caller, first.Version, kernel.Page{Limit: 64, After: first.Next})
					switch change {
					case "unchanged", "grant_replacement":
						storekit.Must(t, err)
						if second.Reason != cleanup.ReasonSelected || len(second.Items) != 1 || second.Next != "" || second.Federation == nil {
							t.Fatal("fresh continuation lost its real final page", change, second.Reason, len(second.Items), second.Next)
						}
						for _, file := range first.Items {
							if file.ID == second.Items[0].ID {
								t.Fatal("candidate continuation repeated a file")
							}
						}
						if change == "grant_replacement" && second.Federation.Scope.Members[0].Grant == proof.Scope.Members[0].Grant {
							t.Fatal("new page reused the replaced relationship's evidence")
						}
					case "membership":
						storekit.Must(t, err)
						if second.Reason != cleanup.ReasonFederationUnknown || len(second.Items) != 0 || second.Next != "" || second.Federation != nil {
							t.Fatal("disabled member left stale continuation authority", second.Reason, len(second.Items))
						}
					case "incarnation":
						if !errors.Is(err, kernel.ErrNotFound) || len(second.Items) != 0 {
							t.Fatal("old continuation adopted recreated repository", second.Reason, err)
						}
					case "ownership":
						if !errors.Is(err, kernel.ErrForbidden) || len(second.Items) != 0 {
							t.Fatal("old continuation exposed foreign repository", second.Reason, err)
						}
					case "credential":
						if !errors.Is(err, kernel.ErrBadCredentials) || len(second.Items) != 0 {
							t.Fatal("old continuation borrowed new credentials", second.Reason, err)
						}
					}
					if change != "incarnation" {
						f.preserved(t)
					} else {
						response := support.Do(t, f.handler, http.MethodGet, "/artifactory/execution/package/1/one.bin", support.Basic("admin", "pw"), "")
						if response.Code != http.StatusOK || response.Body.String() != "replacement-incarnation" {
							t.Fatal("old continuation changed replacement content", response.Code)
						}
					}
				})
			}
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

type cleanupRepositoryFailure struct {
	artifactsapi.CleanupMetadata
	failure error
}

func (m cleanupRepositoryFailure) Repository(context.Context, string) (artifactsapi.CleanupRepository, error) {
	return artifactsapi.CleanupRepository{}, m.failure
}

type cleanupPeerReadFailure struct {
	fedport.PeerStore
	fedport.PeerRevisions
}

func (cleanupPeerReadFailure) Get(context.Context, string) (fedmodel.Peer, error) {
	return fedmodel.Peer{}, fmt.Errorf("fixture peer backend read: %w", federationapi.ErrCleanupUnavailable)
}

type cleanupScopeReadFailure struct {
	fedcleanup.Store
	peers fedport.PeerStore
}

func (s cleanupScopeReadFailure) Peers() fedport.PeerStore { return s.peers }

// Only expected logical unavailability may leave a joined read transaction
// usable. Dropping an actual backend/authority error must still poison the
// outer transaction under the unchanged SQL unit-of-work contract.
func TestAccFederatedScopeNestedRefusalsSQL(t *testing.T) {
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), nil) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			for _, failure := range []string{"unavailable", "storage", "authority", "wrapped_peer_storage"} {
				t.Run(failure, func(t *testing.T) {
					db := newStore(t)
					_, _, _, boundary := newFederatedSelection(t, db, "selected")
					service := boundary.Cleanup.(*fedcleanup.Service)
					want := error(federationapi.ErrCleanupUnavailable)
					if failure == "wrapped_peer_storage" {
						peers := service.Store.Peers()
						service.Store = cleanupScopeReadFailure{service.Store, cleanupPeerReadFailure{peers, peers.(fedport.PeerRevisions)}}
					} else if failure == "unavailable" {
						cfg := storekit.Get[repo.Config](t)(db.Repos().Get(t.Context(), "alpha-federated"))
						cfg.Members[1].Enabled = repo.Flex(false)
						storekit.Must(t, db.Repos().Update(t.Context(), cfg))
					} else {
						want = kernel.ErrStore
						if failure == "authority" {
							want = kernel.ErrForbidden
						}
						service.Metadata = cleanupRepositoryFailure{service.Metadata, want}
					}
					var scope federationapi.CleanupScope
					var scopeErr error
					outerErr := db.Tx(t.Context(), func(ctx context.Context) error { scope, scopeErr = service.Scope(ctx, "alpha-federated"); return nil })
					if !errors.Is(scopeErr, want) {
						t.Fatal("scope changed its public refusal/error contract", failure, scopeErr)
					}
					if failure == "unavailable" {
						storekit.Must(t, outerErr)
						if scope.Repository != "alpha-federated" || scope.Project != "alpha" || scope.PackageType != "npm" || scope.Incarnation == "" || scope.Generation == "" {
							t.Fatal("logical refusal lost pinned repository context", scope)
						}
					} else if !errors.Is(outerErr, want) {
						t.Fatal("dropped backend/authority error did not fail the outer transaction", failure, outerErr)
					}
				})
			}
		},
		sqltest.WithPostgres(),
	)
}
