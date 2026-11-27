package retention_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retstoretest "github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

var (
	errRefusalOutcomeStorage = errors.New("cleanup refusal outcome storage unavailable")
	errRefusalNativeStorage  = errors.New("native removal storage unavailable")
)

// A real selected entry reaches the native permission/storage boundary before
// the injected refusal. The reporting transaction must not treat that refusal
// itself as a successfully committed item or version outcome.
type refusalNativeBoundary struct {
	artifactsapi.ItemRemoval
	calls   map[int64]int
	refused func()
	cause   error
}

func (r refusalNativeBoundary) Remove(
	_ context.Context,
	request artifactsapi.RemovalRequest,
	_ func(context.Context) error,
	_ func(context.Context, artifactsapi.RemovalResult) error,
) (artifactsapi.RemovalResult, error) {
	if r.calls != nil {
		r.calls[request.Item.ID]++
	}
	if r.refused != nil {
		r.refused()
	}
	return artifactsapi.RemovalResult{}, r.cause
}

func refusalFence(f *cleanupExecutorFixture, item work.WorkItem) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := f.db.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
		return err
	}
}

func requireRefusalUnchanged(t *testing.T, f *cleanupExecutorFixture, before cleanupDurableState) {
	t.Helper()
	storekit.Same(t, "unpublished refusal preserves run/continuation/outcome set", before, cleanupDurableSnapshot(t, f, before.Run.ID))
	f.preserved(t)
}

func requireAllNativeDenials(t *testing.T, f *cleanupExecutorFixture, run model.CleanupRun, item work.WorkItem) {
	t.Helper()
	recovered := f.executor
	recovered.Removal = refusalNativeBoundary{ItemRemoval: recovered.Removal, cause: kernel.ErrForbidden}
	storekit.Must(t, recovered.Execute(f.ctx, item))
	current := cleanupDurableSnapshot(t, f, run.ID)
	if current.Run.Status != retapi.RunCompletedWithErrors || current.Run.TotalItems != 2 || current.Run.CleanedItems != 0 || current.Run.CleanedSize != 0 {
		t.Fatal("native refusal recovery invented successful effects", current.Run)
	}
	failed := 0
	for _, entry := range current.Entries {
		if entry.Kind == model.CleanupItemEntry {
			if entry.State != model.CleanupEntryFailed || entry.Reason != "delete_permission_denied" {
				t.Fatal("pending native denial lost its independent outcome", entry)
			}
			failed++
		}
	}
	if failed != 2 {
		t.Fatal("native refusal recovery lost/duplicated pending files", failed)
	}
	f.preserved(t)
}

func TestAccCleanupRefusalSkipPublicationFaultsPreserveOutcomes(t *testing.T) {
	for _, operation := range []string{"Entries", "AdvanceEntry"} {
		t.Run(operation, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			remote := enableFederatedExecutor(t, f)
			run, item := f.selected(t)
			remote.err = errRefusalNativeStorage
			_, err := f.executor.Step(f.ctx, run.ID, refusalFence(f, item))
			storekit.Must(t, err)
			before := cleanupDurableSnapshot(t, f, run.ID)
			if before.Progress.Phase != model.CleanupSkipping || before.Progress.Reason != string(cleanup.ReasonFederationUnknown) {
				t.Fatal("real unavailable member did not enter conservative skip recovery", before.Progress)
			}
			hit := false
			faultStore := cleanupExecutionBoundaryStore{
				CleanupExecutionStore: f.store,
				cleanup: retstoretest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, method string) error {
					if method == operation {
						hit = true
						return errRefusalOutcomeStorage
					}
					return nil
				}},
			}
			faulty := f.executor
			faulty.Store, faulty.Policies.Store = faultStore, faultStore
			_, err = faulty.Step(f.ctx, run.ID, refusalFence(f, item))
			if !hit || !errors.Is(err, errRefusalOutcomeStorage) {
				t.Fatalf("skip recovery did not preserve its storage refusal: hit=%v err=%v", hit, err)
			}
			requireRefusalUnchanged(t, f, before)
			storekit.Must(t, f.executor.Execute(f.ctx, item))
			finished := cleanupDurableSnapshot(t, f, run.ID)
			if finished.Run.Status != retapi.RunCompleted || finished.Run.TotalItems != 2 || finished.Run.CleanedItems != 0 {
				t.Fatal("unavailable member retry invented cleanup success", finished.Run)
			}
			skipped := 0
			for _, entry := range finished.Entries {
				if entry.Kind == model.CleanupItemEntry {
					if entry.State != model.CleanupSkipped || entry.Reason != string(cleanup.ReasonFederationUnknown) {
						t.Fatal("skip retry lost its conservative member outcome", entry)
					}
					skipped++
				}
			}
			if skipped != 2 {
				t.Fatal("skip retry lost/duplicated actual candidate files", skipped)
			}
			f.preserved(t)
		})
	}
}

func TestAccCleanupNativeDenialPublicationFaultsRetainPendingItems(t *testing.T) {
	for _, operation := range []string{"PendingEntry", "AdvanceEntry"} {
		t.Run(operation, func(t *testing.T) {
			f := newCleanupExecutorFixture(t, inmem.New())
			run, item := f.selected(t)
			before := cleanupDurableSnapshot(t, f, run.ID)
			denied, hit := false, false
			faultStore := cleanupExecutionBoundaryStore{
				CleanupExecutionStore: f.store,
				cleanup: retstoretest.CleanupFaults{CleanupStore: f.db.Cleanup(), After: func(_ context.Context, method string) error {
					if denied && method == operation {
						hit = true
						return errRefusalOutcomeStorage
					}
					return nil
				}},
			}
			faulty := f.executor
			faulty.Store, faulty.Policies.Store = faultStore, faultStore
			faulty.Removal = refusalNativeBoundary{ItemRemoval: faulty.Removal, cause: kernel.ErrForbidden, refused: func() { denied = true }}
			_, err := faulty.Step(f.ctx, run.ID, refusalFence(f, item))
			if !denied || !hit || !errors.Is(err, errRefusalOutcomeStorage) {
				t.Fatalf("denial publication missed real pending outcome boundary: denied=%v hit=%v err=%v", denied, hit, err)
			}
			requireRefusalUnchanged(t, f, before)
			requireAllNativeDenials(t, f, run, item)
		})
	}
}

func TestAccCleanupIndependentNativeDenialsPreserveFailedVersion(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	run, item := f.selected(t)
	calls := make(map[int64]int)
	denied := f.executor
	denied.Removal = refusalNativeBoundary{ItemRemoval: denied.Removal, cause: kernel.ErrForbidden, calls: calls}
	_, err := denied.Step(f.ctx, run.ID, refusalFence(f, item))
	storekit.Must(t, err)
	progress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
	first := storekit.Get[model.CleanupEntry](t)(f.db.Cleanup().Entry(f.ctx, run.ID, model.CleanupVersionEntry, progress.Version.ID))
	if first.State != model.CleanupEntryFailed || first.Reason != "delete_permission_denied" {
		t.Fatal("first native denial did not retain failed version authority", first)
	}
	_, err = denied.Step(f.ctx, run.ID, refusalFence(f, item))
	storekit.Must(t, err)
	second := storekit.Get[model.CleanupEntry](t)(f.db.Cleanup().Entry(f.ctx, run.ID, model.CleanupVersionEntry, progress.Version.ID))
	storekit.Same(t, "later denied file cannot relabel or rewrite prior failed version", first, second)
	for _, id := range f.ids {
		if calls[id] != 1 {
			t.Fatal("pending files were not independently refused exactly once", calls)
		}
	}
	storekit.Must(t, denied.Execute(f.ctx, item))
	finished := cleanupDurableSnapshot(t, f, run.ID)
	if finished.Run.Status != retapi.RunCompletedWithErrors || finished.Run.CleanedItems != 0 || finished.Run.TotalItems != 2 {
		t.Fatal("independent denials became successful deletion", finished.Run)
	}
	storekit.Must(t, denied.Execute(f.ctx, item))
	for _, id := range f.ids {
		if calls[id] != 1 {
			t.Fatal("completed run replay repeated native denial", calls)
		}
	}
	f.preserved(t)
}

// Create a genuinely project-bound policy before admission. Project identity
// failures below therefore cross the real current-project lookup; an unscoped
// default policy would never exercise that ownership boundary.
func newRefusalAuthorityFixture(t *testing.T, phase model.CleanupPhase, projectBound bool) (*cleanupExecutorFixture, model.CleanupRun, work.WorkItem) {
	t.Helper()
	f := newCleanupExecutorFixture(t, inmem.New())
	key := "execution-policy"
	if projectBound {
		project := "refusal-owner"
		storekit.Must(t, f.db.Projects().Create(f.ctx, accesstest.Project{Key: project}))
		configuration := storekit.Get[repo.Config](t)(f.db.Repos().Get(f.ctx, "execution"))
		configuration.ProjectKey = project
		storekit.Must(t, f.db.Repos().Update(f.ctx, configuration))
		key = "project-refusal-policy"
		policy := model.CleanupPolicy{
			Key:             key,
			ProjectKey:      project,
			DurationMinutes: 1,
			Criteria: model.CleanupCriteria{
				PackageTypes:     []string{"generic"},
				Repos:            []string{"execution"},
				IncludedPackages: []string{"**"},
				IncludedProjects: []string{project},
			},
		}
		storekit.Get[model.CleanupPolicy](t)(f.policies.Create(f.caller, key, policy))
		storekit.Must(t, f.policies.Enable(f.caller, key, true))
	}
	run := storekit.Get[model.CleanupRun](t)(f.policies.ManualRun(f.caller, key, false))
	item, taken, err := f.db.WorkQueue().
		LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(run.ID, 10), work.WorkLease{Holder: "refusal-authority", Now: f.now, Until: f.now.Add(time.Hour)})
	storekit.Must(t, err)
	if !taken {
		t.Fatal("refusal authority fixture did not lease real run")
	}
	for range 30 {
		progress := storekit.Get[model.CleanupProgress](t)(f.db.Cleanup().Progress(f.ctx, run.ID))
		active := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, run.ID))
		if progress.Phase == phase && !active.Started.IsZero() {
			return f, run, item
		}
		_, err = f.executor.Step(f.ctx, run.ID, refusalFence(f, item))
		storekit.Must(t, err)
	}
	t.Fatal("refusal authority fixture missed intended real phase", phase)
	return nil, model.CleanupRun{}, work.WorkItem{}
}

type refusalMissingParentIndex struct {
	artifactsapi.PackageIndex
	refused func()
}

func (p refusalMissingParentIndex) Files(
	ctx context.Context,
	version artifactsapi.IndexVersion,
	properties map[string][]string,
	page kernel.Page,
) ([]artifactsapi.IndexFile, string, error) {
	_, _, err := p.PackageIndex.Files(ctx, version, properties, page)
	if err != nil {
		return nil, "", err
	}
	p.refused()
	return nil, "", kernel.ErrNotFound
}

type refusalDeniedPolicy struct{ cleanup.Policy }

func (refusalDeniedPolicy) Allows(string) bool { return false }

func TestAccCleanupRefusalRechecksCurrentAuthorityBeforeReporting(t *testing.T) {
	for _, phase := range []model.CleanupPhase{model.CleanupRemoving, model.CleanupCandidates} {
		for _, boundary := range []string{"authority revoked", "project lookup unavailable"} {
			t.Run(string(phase)+"/"+boundary, func(t *testing.T) {
				f, run, item := newRefusalAuthorityFixture(t, phase, boundary == "project lookup unavailable")
				before := cleanupDurableSnapshot(t, f, run.ID)
				refused, hit := false, false
				faulty := f.executor
				if phase == model.CleanupRemoving {
					faulty.Removal = refusalNativeBoundary{ItemRemoval: faulty.Removal, cause: kernel.ErrForbidden, refused: func() { refused = true }}
				} else {
					faulty.Policies.PackageIndex = refusalMissingParentIndex{PackageIndex: faulty.Policies.PackageIndex, refused: func() { refused = true }}
				}
				expected := error(kernel.ErrForbidden)
				if boundary == "authority revoked" {
					authorize := faulty.Policies.Authorize
					faulty.Policies.Authorize = func(ctx context.Context) (cleanup.Policy, error) {
						authority, err := authorize(ctx)
						if err == nil && refused {
							hit = true
							return refusalDeniedPolicy{authority}, nil
						}
						return authority, err
					}
				} else {
					expected = errRefusalOutcomeStorage
					lifetime := faulty.Policies.ProjectIncarnation
					faulty.Policies.ProjectIncarnation = func(ctx context.Context, key string) (string, error) {
						identity, err := lifetime(ctx, key)
						if err == nil && refused {
							hit = true
							return "", errRefusalOutcomeStorage
						}
						return identity, err
					}
				}
				_, err := faulty.Step(f.ctx, run.ID, refusalFence(f, item))
				if !refused || !hit || !errors.Is(err, expected) {
					t.Fatalf("refusal published without current caller/project boundary: refused=%v hit=%v err=%v", refused, hit, err)
				}
				requireRefusalUnchanged(t, f, before)
				requireAllNativeDenials(t, f, run, item)
			})
		}
	}
}

func TestAccCleanupFinalRefusalGuardOutageRetainsRecoverableRun(t *testing.T) {
	f := newCleanupExecutorFixture(t, inmem.New())
	run, item := f.selected(t)
	before := cleanupDurableSnapshot(t, f, run.ID)
	failed, hit := false, false
	faulty := f.executor
	faulty.Removal = refusalNativeBoundary{ItemRemoval: faulty.Removal, cause: errRefusalNativeStorage, refused: func() { failed = true }}
	guard := faulty.Policies.Guard
	faulty.Policies.Guard = func(ctx context.Context) error {
		if err := guard(ctx); err != nil {
			return err
		}
		if failed {
			hit = true
			return errRefusalOutcomeStorage
		}
		return nil
	}
	item.FinalAttempt = true
	err := faulty.Execute(f.ctx, item)
	if !failed || !hit || !errors.Is(err, work.ErrUnsettled) || !errors.Is(err, errRefusalOutcomeStorage) || !errors.Is(err, errRefusalNativeStorage) {
		t.Fatalf("unrecorded final native failure became terminal success: failed=%v hit=%v err=%v", failed, hit, err)
	}
	requireRefusalUnchanged(t, f, before)
	recovered := f.executor
	recovered.Removal = refusalNativeBoundary{ItemRemoval: recovered.Removal, cause: errRefusalNativeStorage}
	storekit.Must(t, recovered.Execute(f.ctx, item))
	finished := cleanupDurableSnapshot(t, f, run.ID)
	if finished.Run.Status != retapi.RunFailed || finished.Run.CleanedItems != 0 || finished.Run.CleanedSize != 0 {
		t.Fatal("failure guard recovery invented successful removal", finished.Run)
	}
	f.preserved(t)
}
