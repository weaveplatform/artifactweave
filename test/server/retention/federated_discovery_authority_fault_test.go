package retention_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	federationapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retstoretest "github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	retport "github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

var errFederatedDiscoveryStorage = errors.New("federated discovery storage unavailable")

// The collector, metadata transactions and candidate index are real. Hooks
// expose failures after reads have happened, rather than manufacturing proof
// or granting a different caller access to a saved candidate.
type discoveryEvidenceBoundary struct {
	federationapi.Cleanup
	db                                 app.RouterStore
	scopeCalls, guardCalls, freshCalls int
	scope                              func(int, *federationapi.CleanupScope) error
	collected                          func()
	collectionErr                      error
	guarded                            func(int) error
	freshened                          func(int) error
}

func (b *discoveryEvidenceBoundary) Scope(ctx context.Context, key string) (federationapi.CleanupScope, error) {
	b.scopeCalls++
	scope, err := b.Cleanup.Scope(ctx, key)
	if err == nil && b.scope != nil {
		err = b.scope(b.scopeCalls, &scope)
	}
	return scope, err
}

func (b *discoveryEvidenceBoundary) Collect(ctx context.Context, scope federationapi.CleanupScope, versions []federationapi.CleanupVersion) (federationapi.CleanupEvidence, error) {
	if b.db.InTx(ctx) {
		return federationapi.CleanupEvidence{}, kernel.ErrStore
	}
	evidence, err := b.Cleanup.Collect(ctx, scope, versions)
	if err != nil {
		return evidence, err
	}
	if b.collected != nil {
		b.collected()
	}
	return evidence, b.collectionErr
}

func (b *discoveryEvidenceBoundary) Guard(ctx context.Context, evidence federationapi.CleanupEvidence) error {
	b.guardCalls++
	if b.guarded != nil {
		if err := b.guarded(b.guardCalls); err != nil {
			return err
		}
	}
	return b.Cleanup.Guard(ctx, evidence)
}

func (b *discoveryEvidenceBoundary) Fresh(ctx context.Context, evidence federationapi.CleanupEvidence) error {
	b.freshCalls++
	if b.freshened != nil {
		if err := b.freshened(b.freshCalls); err != nil {
			return err
		}
	}
	return b.Cleanup.Fresh(ctx, evidence)
}

type discoveryIndexBoundary struct {
	artifactsapi.PackageIndex
	versions        int
	beforeVersion   func(context.Context, int) error
	afterVersion    func(int)
	repositoryError func() bool
}

func (p *discoveryIndexBoundary) Version(ctx context.Context, ref artifactsapi.IndexVersion, keep int) (artifactsapi.IndexVersion, error) {
	p.versions++
	if p.beforeVersion != nil {
		if err := p.beforeVersion(ctx, p.versions); err != nil {
			return artifactsapi.IndexVersion{}, err
		}
	}
	value, err := p.PackageIndex.Version(ctx, ref, keep)
	if err == nil && p.afterVersion != nil {
		p.afterVersion(p.versions)
	}
	return value, err
}

func (p *discoveryIndexBoundary) Repositories(ctx context.Context, query artifactsapi.IndexRepositoryQuery, page kernel.Page) ([]artifactsapi.IndexRepository, string, error) {
	if p.repositoryError != nil && p.repositoryError() {
		return nil, "", errFederatedDiscoveryStorage
	}
	return p.PackageIndex.Repositories(ctx, query, page)
}

func requireSelectionContent(t *testing.T, w *selectionWorld) {
	t.Helper()
	for _, item := range w.items {
		actual := storekit.Get[artifactmodel.Item](t)(w.db.Items().GetByID(t.Context(), item.ID))
		if actual.Actual != item.Actual || actual.Size != item.Size {
			t.Fatal("failed selection changed content identity")
		}
		stats := storekit.Get[artifactmodel.ItemStats](t)(w.db.Items().Stats(t.Context(), item.ID))
		if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
			t.Fatal("read-only selection changed public download statistics")
		}
	}
}

func TestAccFederatedSelectionEvidenceFailuresRetainContent(t *testing.T) {
	for _, name := range []string{"scope storage", "scope binding", "collection storage", "cancelled collection", "current scope storage", "context changed", "guard storage", "fresh storage"} {
		t.Run(name, func(t *testing.T) {
			w, _, version, previous := newFederatedSelection(t, inmem.New(), "selected")
			boundary := &discoveryEvidenceBoundary{Cleanup: previous.Cleanup, db: w.db}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := error(errFederatedDiscoveryStorage)
			switch name {
			case "scope storage":
				boundary.scope = func(int, *federationapi.CleanupScope) error { return want }
			case "scope binding":
				want = kernel.ErrConflict
				boundary.scope = func(_ int, scope *federationapi.CleanupScope) error { scope.Project = "foreign-project"; return nil }
			case "collection storage":
				boundary.collectionErr = want
			case "cancelled collection":
				want = context.Canceled
				boundary.collected = cancel
			case "current scope storage":
				boundary.scope = func(n int, _ *federationapi.CleanupScope) error {
					if n > 1 {
						return want
					}
					return nil
				}
			case "context changed":
				want = kernel.ErrConflict
				boundary.collected = func() {
					configuration := storekit.Get[repo.Config](t)(w.db.Repos().Get(t.Context(), version.Package.Repository.Key))
					configuration.DownloadRedirect = repo.Flex(true)
					storekit.Must(t, w.db.Repos().Update(t.Context(), configuration))
				}
			case "guard storage":
				boundary.guarded = func(int) error { return want }
			case "fresh storage":
				boundary.freshened = func(int) error { return want }
			}
			w.service.Federation = boundary
			selection := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), "selection-policy"))
			page, err := selection.Candidates(ctx, version, kernel.Page{Limit: 1})
			if !errors.Is(err, want) {
				t.Fatalf("evidence failure became an apparently successful candidate/refusal: %v", err)
			}
			if len(page.Items) != 0 || page.Federation != nil || page.Next != "" {
				t.Fatal("failed evidence disclosed usable candidate metadata", page)
			}
			requireSelectionContent(t, w)
			// A transient failure may be retried only through the same public caller
			// and a fresh collection; it cannot grant authority from the failed proof.
			w.service.Federation = previous
			retry := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), "selection-policy"))
			recovered := storekit.Get[cleanup.CandidatePage](t)(retry.Candidates(t.Context(), version, kernel.Page{Limit: 1}))
			if recovered.Reason != cleanup.ReasonSelected || len(recovered.Items) != 1 || recovered.Federation == nil {
				t.Fatal("valid selection did not recover after transient evidence failure", recovered)
			}
		})
	}
}

func TestAccFederatedSelectionRefreshesParentAndPackageAuthority(t *testing.T) {
	for _, name := range []string{"policy read", "index capability", "excluded repository", "changed repository context", "excluded package versions", "excluded package candidates", "version parent read", "lead parent read"} {
		t.Run(name, func(t *testing.T) {
			w, _, version, previous := newFederatedSelection(t, inmem.New(), "selected")
			want := error(kernel.ErrForbidden)
			switch name {
			case "policy read":
				want = errFederatedDiscoveryStorage
				w.service.Store = cleanupExecutionBoundaryStore{
					CleanupExecutionStore: w.db.(retport.CleanupExecutionStore),
					cleanup: retstoretest.CleanupFaults{CleanupStore: w.db.Cleanup(), After: func(_ context.Context, method string) error {
						if method == "Policy" {
							return want
						}
						return nil
					}},
				}
			case "index capability":
				want = kernel.ErrStore
				w.service.PackageIndex = nil
			}
			selection, err := w.service.Select(t.Context(), "selection-policy")
			if name == "policy read" || name == "index capability" {
				if selection != nil || !errors.Is(err, want) {
					t.Fatalf("selection started without required policy/index: %v", err)
				}
				requireSelectionContent(t, w)
				return
			}
			storekit.Must(t, err)
			switch name {
			case "excluded repository":
				w.repository("alpha-safe", "alpha", repo.NPM)
				configuration := storekit.Get[repo.Config](t)(w.db.Repos().Get(t.Context(), "alpha-safe"))
				ref := version.Package.Repository
				ref.Key = configuration.Key
				ref.Class = configuration.Class
				rows, next, readErr := selection.Packages(t.Context(), ref, kernel.Page{Limit: 1})
				if len(rows) != 0 || next != "" || !errors.Is(readErr, want) {
					t.Fatalf("excluded parent disclosed packages: %v", readErr)
				}
			case "changed repository context":
				want = kernel.ErrConflict
				configuration := storekit.Get[repo.Config](t)(w.db.Repos().Get(t.Context(), version.Package.Repository.Key))
				configuration.Environments = append(configuration.Environments, "PROD")
				storekit.Must(t, w.db.Repos().Update(t.Context(), configuration))
				rows, next, readErr := selection.Packages(t.Context(), version.Package.Repository, kernel.Page{Limit: 1})
				if len(rows) != 0 || next != "" || !errors.Is(readErr, want) {
					t.Fatalf("stale repository parent disclosed packages: %v", readErr)
				}
			case "excluded package versions", "excluded package candidates":
				excluded := w.version(version.Package.Repository.Key, repo.NPM, "keep", "1.0.0", nil)
				excluded.Package.Repository = version.Package.Repository
				if name == "excluded package versions" {
					rows, next, readErr := selection.Versions(t.Context(), excluded.Package, kernel.Page{Limit: 1})
					if len(rows) != 0 || next != "" || !errors.Is(readErr, want) {
						t.Fatalf("excluded package disclosed versions: %v", readErr)
					}
				} else {
					page, readErr := selection.Candidates(t.Context(), excluded, kernel.Page{Limit: 1})
					if len(page.Items) != 0 || page.Federation != nil || !errors.Is(readErr, want) {
						t.Fatalf("excluded package disclosed candidates: %v", readErr)
					}
				}
			case "version parent read", "lead parent read":
				want = errFederatedDiscoveryStorage
				armed := name == "version parent read"
				index := &discoveryIndexBoundary{PackageIndex: w.service.PackageIndex, repositoryError: func() bool { return armed }}
				if name == "lead parent read" {
					index.afterVersion = func(int) { armed = true }
				}
				w.service.PackageIndex = index
				current := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), "selection-policy"))
				if name == "version parent read" {
					rows, next, readErr := current.Versions(t.Context(), version.Package, kernel.Page{Limit: 1})
					if len(rows) != 0 || next != "" || !errors.Is(readErr, want) {
						t.Fatalf("unavailable parent disclosed version page: %v", readErr)
					}
				} else {
					page, readErr := current.Candidates(t.Context(), version, kernel.Page{Limit: 1})
					if len(page.Items) != 0 || page.Federation != nil || !errors.Is(readErr, want) {
						t.Fatalf("lead traversal ignored current parent outage: %v", readErr)
					}
				}
			}
			requireSelectionContent(t, w)
			w.service.Federation = previous
		})
	}
}

func TestAccFederatedSelectionRechecksComparatorAndContentAfterEvaluation(t *testing.T) {
	for _, change := range []string{"newly protected", "changed version revision"} {
		t.Run(change, func(t *testing.T) {
			w, _, version, _ := newFederatedSelection(t, inmem.New(), "selected")
			hit := false
			index := &discoveryIndexBoundary{PackageIndex: w.service.PackageIndex}
			index.beforeVersion = func(ctx context.Context, n int) error {
				if n != 2 {
					return nil
				}
				hit = true
				if change == "newly protected" {
					newer := storekit.Get[artifactmodel.PackageVersion](t)(w.db.Packages().Version(ctx, version.Package.ID, "2.0.0"))
					return w.db.Packages().DeleteVersion(ctx, newer.ID)
				}
				return w.db.Items().RecordDownload(ctx, w.items[0].ID, artport.Download{By: "reader-after-evaluation", At: w.now})
			}
			w.service.PackageIndex = index
			selection := storekit.Get[*cleanup.Selection](t)(w.service.Select(t.Context(), "selection-policy"))
			page, err := selection.Candidates(t.Context(), version, kernel.Page{Limit: 1})
			storekit.Must(t, err)
			want := cleanup.ReasonChanged
			if change == "newly protected" {
				want = cleanup.ReasonProtected
			}
			if !hit || page.Reason != want || len(page.Items) != 0 || page.Federation != nil {
				t.Fatal("late comparator/use mutation escaped candidate fence", hit, page)
			}
			for _, item := range w.items {
				storekit.Get[artifactmodel.Item](t)(w.db.Items().GetByID(t.Context(), item.ID))
			}
		})
	}
}

type discoveryRunFixture struct {
	*cleanupExecutorFixture
	run   model.CleanupRun
	item  work.WorkItem
	fence func(context.Context) error
}

func newPublicDiscoveryFixture(t *testing.T, phase model.CleanupPhase, setup func(*cleanupExecutorFixture)) discoveryRunFixture {
	t.Helper()
	f := newCleanupExecutorFixture(t, inmem.New())
	enableFederatedExecutor(t, f)
	if setup != nil {
		setup(f)
	}
	run := storekit.Get[model.CleanupRun](t)(f.policies.ManualRun(f.caller, "execution-policy", true))
	item, taken, err := f.db.WorkQueue().
		LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(run.ID, 10), work.WorkLease{Holder: "discovery-authority", Now: f.now, Until: f.now.Add(time.Hour)})
	storekit.Must(t, err)
	if !taken {
		t.Fatal("discovery did not acquire real queued run")
	}
	fence := func(ctx context.Context) error {
		_, err := f.db.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
		return err
	}
	for range 30 {
		progress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
		current := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
		if progress.Phase == phase && !current.Started.IsZero() {
			return discoveryRunFixture{f, run, item, fence}
		}
		_, err = f.executor.Step(f.ctx, run.ID, fence)
		storekit.Must(t, err)
	}
	t.Fatal("real discovery never reached requested phase", phase)
	return discoveryRunFixture{}
}

type cleanupDurableState struct {
	Run      model.CleanupRun
	Progress model.CleanupProgress
	Entries  []model.CleanupEntry
}

func cleanupDurableSnapshot(t *testing.T, f *cleanupExecutorFixture, run int64) cleanupDurableState {
	t.Helper()
	entries, _, err := f.db.Cleanup().Entries(f.ctx, retport.CleanupEntryQuery{RunID: run}, kernel.Page{Limit: 100})
	storekit.Must(t, err)
	return cleanupDurableState{
		storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run)),
		storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run)),
		entries,
	}
}

func (f discoveryRunFixture) snapshot(t *testing.T) cleanupDurableState {
	t.Helper()
	return cleanupDurableSnapshot(t, f.cleanupExecutorFixture, f.run.ID)
}

func (f discoveryRunFixture) unchanged(t *testing.T, before cleanupDurableState) {
	t.Helper()
	storekit.Same(t, "failed discovery preserves run/cursor/private candidate set", before, f.snapshot(t))
	f.preserved(t)
}

func (f discoveryRunFixture) completePreview(t *testing.T, want int64) {
	t.Helper()
	storekit.Must(t, f.executor.Execute(f.ctx, f.item))
	current := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, f.run.ID))
	if current.Status != retapi.RunCompleted || current.TotalItems != want || current.CleanedItems != 0 || current.CleanedSize != 0 {
		t.Fatal("discovery recovery invented or duplicated effects", current)
	}
	f.preserved(t)
}

func TestAccFederatedDiscoveryRequiresRestoredExternalCaller(t *testing.T) {
	for _, failure := range []string{"restore unavailable", "nil restored caller"} {
		t.Run(failure, func(t *testing.T) {
			f := newPublicDiscoveryFixture(t, model.CleanupCandidates, nil)
			before := f.snapshot(t)
			faulty := f.executor
			faulty.Policies.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) {
				if failure == "restore unavailable" {
					return nil, errFederatedDiscoveryStorage
				}
				return nil, nil
			}
			_, err := faulty.Step(f.ctx, f.run.ID, f.fence)
			want := error(errFederatedDiscoveryStorage)
			if failure == "nil restored caller" {
				want = kernel.ErrStore
			}
			if !errors.Is(err, want) {
				t.Fatalf("discovery continued without a restored caller: %v", err)
			}
			f.unchanged(t, before)
			f.completePreview(t, 2)
		})
	}
}

type discoveryDeniedPolicy struct{ cleanup.Policy }

func (discoveryDeniedPolicy) Allows(string) bool { return false }

func TestAccFederatedDiscoveryRechecksAuthorityAndPersistenceAfterPage(t *testing.T) {
	for _, failure := range []string{"guard after page", "fresh after page", "worker fence after page", "version after page", "policy after page", "run after page", "authority before guard", "authority before publication"} {
		t.Run(failure, func(t *testing.T) {
			f := newPublicDiscoveryFixture(t, model.CleanupCandidates, nil)
			before := f.snapshot(t)
			boundary := &discoveryEvidenceBoundary{Cleanup: f.policies.Federation, db: f.db}
			index := &discoveryIndexBoundary{PackageIndex: f.policies.PackageIndex}
			armed, denied, hit := false, false, false
			boundary.guarded = func(n int) error {
				if n == 2 && failure == "guard after page" {
					hit = true
					return errFederatedDiscoveryStorage
				}
				return nil
			}
			boundary.freshened = func(n int) error {
				if n == 1 && failure == "authority before guard" {
					denied = true
					hit = true
				}
				if n == 2 && failure == "fresh after page" {
					hit = true
					return errFederatedDiscoveryStorage
				}
				return nil
			}
			index.beforeVersion = func(context.Context, int) error {
				if boundary.guardCalls >= 2 && failure == "version after page" {
					hit = true
					return errFederatedDiscoveryStorage
				}
				return nil
			}
			index.afterVersion = func(int) {
				if boundary.guardCalls < 2 {
					return
				}
				armed = true
				if failure == "authority before publication" {
					denied = true
					hit = true
				}
			}
			faulty := f.executor
			faulty.Policies.Federation = boundary
			faulty.Policies.PackageIndex = index
			authorize := faulty.Policies.Authorize
			faulty.Policies.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
				authority, err := authorize(ctx)
				if err == nil && denied {
					return discoveryDeniedPolicy{authority}, nil
				}
				return authority, err
			}
			wrapped := cleanupExecutionBoundaryStore{
				CleanupExecutionStore: f.store,
				cleanup: retstoretest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, method string) error {
					if armed && (failure == "policy after page" && method == "Policy" || failure == "run after page" && method == "Run") {
						hit = true
						return errFederatedDiscoveryStorage
					}
					return nil
				}},
			}
			faulty.Store, faulty.Policies.Store = wrapped, wrapped
			fence := func(ctx context.Context) error {
				if err := f.fence(ctx); err != nil {
					return err
				}
				if boundary.freshCalls > 0 && failure == "worker fence after page" {
					hit = true
					return errFederatedDiscoveryStorage
				}
				return nil
			}
			_, err := faulty.Step(f.ctx, f.run.ID, fence)
			if !hit {
				t.Fatal("fault did not reach its real post-page boundary", failure, boundary.guardCalls, boundary.freshCalls)
			}
			if denied {
				storekit.Must(t, err)
				current := f.snapshot(t)
				if current.Run.Status != retapi.RunFailed || current.Run.CleanedItems != 0 || current.Progress.Reason != "authority_revoked" || len(current.Entries) != 0 {
					t.Fatal("revoked page authority published candidates or false success", current)
				}
				f.preserved(t)
				return
			}
			if !errors.Is(err, errFederatedDiscoveryStorage) {
				t.Fatalf("post-page failure did not retain its storage/lease cause: %v", err)
			}
			f.unchanged(t, before)
			f.completePreview(t, 2)
		})
	}
}

func TestAccFederatedDiscoverySkipsEmptyCurrentParentsWithoutDeleting(t *testing.T) {
	for _, phase := range []model.CleanupPhase{model.CleanupRepositories, model.CleanupPackages, model.CleanupVersions} {
		t.Run(string(phase), func(t *testing.T) {
			setup := func(f *cleanupExecutorFixture) {
				if phase == model.CleanupPackages {
					policy := storekit.Get[model.CleanupPolicy](t)(f.policies.Get(f.caller, "execution-policy", false))
					policy.Criteria.Repos = []string{"execution*"}
					storekit.Get[model.CleanupPolicy](t)(f.policies.Update(f.caller, policy.Key, policy))
					storekit.Must(t, f.db.Repos().Create(f.ctx, *repo.Defaults(&repo.Config{Key: "execution-2", Class: repo.Local, PackageType: repo.Generic})))
				}
				if phase == model.CleanupVersions {
					storekit.Get[artifactmodel.Package](t)(f.db.Packages().Ensure(f.ctx, artifactmodel.Package{RepoKey: "execution", Type: repo.Generic, Name: "z-empty"}))
				}
			}
			f := newPublicDiscoveryFixture(t, phase, setup)
			switch phase {
			case model.CleanupRepositories:
				configuration := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
				configuration.ProjectKey = "outside-policy"
				storekit.Must(t, f.db.Repos().Update(f.ctx, configuration))
			case model.CleanupPackages:
				pkg := storekit.Get[artifactmodel.Package](t)(f.db.Packages().Find(f.ctx, "execution", "package"))
				storekit.Must(t, f.db.Packages().Delete(f.ctx, pkg.ID))
			case model.CleanupVersions:
				pkg := storekit.Get[artifactmodel.Package](t)(f.db.Packages().Find(f.ctx, "execution", "package"))
				versions := storekit.Get[[]artifactmodel.PackageVersion](t)(f.db.Packages().Versions(f.ctx, pkg.ID))
				for _, version := range versions {
					storekit.Must(t, f.db.Packages().DeleteVersion(f.ctx, version.ID))
				}
			}
			_, err := f.executor.Step(f.ctx, f.run.ID, f.fence)
			storekit.Must(t, err)
			f.completePreview(t, 0)
		})
	}
}

func TestAccFederatedSelectionRetainsVersionWithNoIndexedFiles(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	enableFederatedExecutor(t, f)
	pkg := storekit.Get[artifactmodel.Package](t)(f.db.Packages().Find(f.ctx, "execution", "package"))
	storekit.Get[artifactmodel.PackageVersion](t)(f.db.Packages().PutVersion(f.ctx, artifactmodel.PackageVersion{PackageID: pkg.ID, Version: "0"}))
	selection := storekit.Get[*cleanup.Selection](t)(f.policies.Select(f.caller, "execution-policy"))
	repositories, _, err := selection.Repositories(f.caller, kernel.Page{Limit: 1})
	storekit.Must(t, err)
	packages, _, err := selection.Packages(f.caller, repositories[0], kernel.Page{Limit: 10})
	storekit.Must(t, err)
	versions, _, err := selection.Versions(f.caller, packages[0], kernel.Page{Limit: 10})
	storekit.Must(t, err)
	found := false
	for _, version := range versions {
		if version.Version == "0" {
			found = true
			page := storekit.Get[cleanup.CandidatePage](t)(selection.Candidates(f.caller, version, kernel.Page{Limit: 1}))
			if page.Reason != cleanup.ReasonNoFiles || len(page.Items) != 0 || page.Federation != nil {
				t.Fatal("empty indexed version became deletion authority", page)
			}
		}
	}
	if !found {
		t.Fatal("fixture did not expose the genuine empty indexed version")
	}
	f.preserved(t)
}

func TestAccFederatedDiscoveryCannotExecuteWithObservationOnlyBoundary(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	enableFederatedExecutor(t, f)
	run, item := f.selected(t)
	before := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
	faulty := f.executor
	// Static interface embedding intentionally withholds durable coordination,
	// while delegating collection/guard/freshness to the real member boundary.
	faulty.Policies.Federation = struct{ federationapi.Cleanup }{f.policies.Federation}
	_, err := faulty.Step(f.ctx, run.ID, func(ctx context.Context) error {
		_, err := f.db.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
		return err
	})
	if !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("observations alone authorized configured-member deletion: %v", err)
	}
	storekit.Same(t, "missing coordination preserves selected progress", before, storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID)))
	f.preserved(t)
}
