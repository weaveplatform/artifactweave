package inmem

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	testkit "github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var errCleanupPersistenceUnavailable = errors.New("cleanup metadata persistence unavailable")

type cleanupFaultFixture struct {
	store         *Store
	configuration repo.Config
	items         []model.Item
	ref           api.CleanupExclusion
}

func newCleanupFaultFixture(t *testing.T) cleanupFaultFixture {
	t.Helper()
	ctx := t.Context()
	f := cleanupFaultFixture{store: New(memtx.New()), configuration: *repo.Defaults(&repo.Config{Key: "faults", Class: repo.Federated, PackageType: repo.NPM})}
	testkit.Must(t, f.store.Repos().Create(ctx, f.configuration))
	pkg := testkit.Get[model.Package](t)(f.store.Packages().Ensure(ctx, model.Package{RepoKey: f.configuration.Key, Type: repo.NPM, Name: "widget"}))
	source := testkit.Get[model.SourceIdentity](t)(f.store.RemoteCache().Source(ctx, f.configuration.Key))
	f.ref = api.CleanupExclusion{
		ID:          "operation",
		Owner:       "coordinator",
		Repository:  f.configuration.Key,
		Incarnation: string(source.Repository),
		Generation:  string(source.Generation),
	}
	for _, number := range []string{"1", "2"} {
		version := testkit.Get[model.PackageVersion](t)(f.store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: number}))
		item := testkit.Get[model.Item](
			t,
		)(
			f.store.Items().Put(ctx, model.Item{RepoKey: f.configuration.Key, Name: number + ".tgz", Size: 1, Actual: kernel.Checksums{SHA256: number}}),
		)
		testkit.Must(t, f.store.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: item.ID}))
		f.items = append(f.items, item)
		f.ref.Versions = append(f.ref.Versions, api.CleanupVersionIdentity{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: number}, ID: version.ID})
	}
	return f
}

// Faults sit at storage boundaries; successful calls delegate to real metadata
// and records. After-write faults verify that the public transaction rolls the
// actual durable effect back, not just that an error reaches its caller.
type cleanupFaultRecords struct {
	storekit.CleanupRecords
	fail string
}

func (r cleanupFaultRecords) Barrier(ctx context.Context, key string) (api.CleanupExclusion, error) {
	if r.fail == "barrier" {
		return api.CleanupExclusion{}, errCleanupPersistenceUnavailable
	}
	return r.CleanupRecords.Barrier(ctx, key)
}

func (r cleanupFaultRecords) Closed(ctx context.Context, ref api.CleanupExclusion) (bool, error) {
	if r.fail == "closed" {
		return false, errCleanupPersistenceUnavailable
	}
	return r.CleanupRecords.Closed(ctx, ref)
}

func (r cleanupFaultRecords) LinkedVersions(ctx context.Context, key string, id int64) ([]int64, error) {
	if r.fail == "links" {
		return nil, errCleanupPersistenceUnavailable
	}
	return r.CleanupRecords.LinkedVersions(ctx, key, id)
}

func (r cleanupFaultRecords) Active(ctx context.Context, key string) (bool, error) {
	if r.fail == "active" {
		return false, errCleanupPersistenceUnavailable
	}
	return r.CleanupRecords.Active(ctx, key)
}

func (r cleanupFaultRecords) AddAdmission(ctx context.Context, record storekit.CleanupAdmissionRecord, at time.Time) error {
	if err := r.CleanupRecords.AddAdmission(ctx, record, at); err != nil {
		return err
	}
	if r.fail == "add after write" {
		return errCleanupPersistenceUnavailable
	}
	return nil
}

func (r cleanupFaultRecords) RemoveAdmission(ctx context.Context, token api.CleanupAdmission) error {
	if err := r.CleanupRecords.RemoveAdmission(ctx, token); err != nil {
		return err
	}
	if r.fail == "complete after write" {
		return errCleanupPersistenceUnavailable
	}
	return nil
}

func (r cleanupFaultRecords) PutBarrier(ctx context.Context, key string, ref api.CleanupExclusion) error {
	if err := r.CleanupRecords.PutBarrier(ctx, key, ref); err != nil {
		return err
	}
	if r.fail == "prepare after write" {
		return errCleanupPersistenceUnavailable
	}
	return nil
}

func (r cleanupFaultRecords) Close(ctx context.Context, ref api.CleanupExclusion) error {
	if err := r.CleanupRecords.Close(ctx, ref); err != nil {
		return err
	}
	if r.fail == "close after write" {
		return errCleanupPersistenceUnavailable
	}
	return nil
}

func (r cleanupFaultRecords) RemoveBarrier(ctx context.Context, ref api.CleanupExclusion) error {
	if err := r.CleanupRecords.RemoveBarrier(ctx, ref); err != nil {
		return err
	}
	if r.fail == "release after write" {
		return errCleanupPersistenceUnavailable
	}
	return nil
}

type cleanupFaultStore struct {
	*Store
	fail string
}

func (s cleanupFaultStore) RemoteCache() port.RemoteCacheStore {
	return cleanupFaultSource{RemoteCacheStore: s.Store.RemoteCache(), fail: s.fail}
}

func (s cleanupFaultStore) Repos() port.RepoStore {
	return cleanupFaultRepos{RepoStore: s.Store.Repos(), fail: s.fail}
}

func (s cleanupFaultStore) Items() port.ItemStore {
	return cleanupFaultItems{ItemStore: s.Store.Items(), fail: s.fail}
}

func (s cleanupFaultStore) Packages() port.PackageStore {
	actual := s.Store.Packages()
	if s.fail == "missing evidence capability" {
		return cleanupHiddenEvidence{PackageStore: actual}
	}
	return cleanupFaultEvidence{PackageStore: actual, evidence: actual.(port.PackageDownloadEvidence), fail: s.fail}
}

type cleanupFaultSource struct {
	port.RemoteCacheStore
	fail string
}

func (s cleanupFaultSource) Source(ctx context.Context, key string) (model.SourceIdentity, error) {
	if s.fail == "source" {
		return model.SourceIdentity{}, errCleanupPersistenceUnavailable
	}
	return s.RemoteCacheStore.Source(ctx, key)
}

type cleanupFaultRepos struct {
	port.RepoStore
	fail string
}

func (s cleanupFaultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if s.fail == "repository" {
		return repo.Config{}, errCleanupPersistenceUnavailable
	}
	return s.RepoStore.Get(ctx, key)
}

type cleanupFaultItems struct {
	port.ItemStore
	fail string
}

func (s cleanupFaultItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if s.fail == "item" {
		return model.Item{}, errCleanupPersistenceUnavailable
	}
	return s.ItemStore.GetByID(ctx, id)
}

type (
	cleanupHiddenEvidence struct{ port.PackageStore }
	cleanupFaultEvidence  struct {
		port.PackageStore
		evidence port.PackageDownloadEvidence
		fail     string
	}
)

func (s cleanupFaultEvidence) VersionDownloads(ctx context.Context, key string, wanted []api.DownloadVersion) ([]api.VersionDownload, error) {
	if s.fail == "evidence" {
		return nil, errCleanupPersistenceUnavailable
	}
	rows, err := s.evidence.VersionDownloads(ctx, key, wanted)
	if err != nil {
		return nil, err
	}
	if s.fail == "missing version" {
		return rows[:len(rows)-1], nil
	}
	return rows, nil
}

func (f cleanupFaultFixture) failing(stage string) api.CleanupExclusions {
	return storekit.NewCleanupExclusions(cleanupFaultStore{Store: f.store, fail: stage}, cleanupFaultRecords{CleanupRecords: cleanupRecords{f.store}, fail: stage})
}

func (f cleanupFaultFixture) evidence(t *testing.T) []api.VersionDownload {
	t.Helper()
	wanted := make([]api.DownloadVersion, 0, len(f.ref.Versions))
	for _, version := range f.ref.Versions {
		wanted = append(wanted, version.DownloadVersion)
	}
	return testkit.Get[[]api.VersionDownload](t)(f.store.Packages().(port.PackageDownloadEvidence).VersionDownloads(t.Context(), f.configuration.Key, wanted))
}

func requireCleanupFault(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errCleanupPersistenceUnavailable) {
		t.Fatalf("storage failure lost its identity: %v", err)
	}
}

func requireNoCleanupGate(t *testing.T, f cleanupFaultFixture) {
	t.Helper()
	if err := f.store.CleanupExclusions().Guard(t.Context(), f.ref); !errors.Is(err, api.ErrCleanupExcluded) {
		t.Fatalf("failed operation left a usable destructive guard: %v", err)
	}
	token := testkit.Get[api.CleanupAdmission](t)(f.store.CleanupExclusions().Admit(t.Context(), f.configuration.Key, f.items[0].ID))
	testkit.Must(t, f.store.CleanupExclusions().Complete(t.Context(), token))
}

func TestUnitCleanupAdmissionStorageFailuresHaveNoPartialPin(t *testing.T) {
	for _, stage := range []string{"source", "repository", "barrier", "item", "links", "add after write"} {
		t.Run(stage, func(t *testing.T) {
			f := newCleanupFaultFixture(t)
			before := f.evidence(t)
			token, err := f.failing(stage).Admit(t.Context(), f.configuration.Key, f.items[0].ID)
			requireCleanupFault(t, err)
			if token != (api.CleanupAdmission{}) {
				t.Fatalf("failed admission leaked a usable token: %+v", token)
			}
			if !slices.Equal(before, f.evidence(t)) {
				t.Fatal("failed admission persisted private download recency/revision")
			}
			testkit.Must(t, f.store.CleanupExclusions().Prepare(t.Context(), f.ref))
			stats := testkit.Get[model.ItemStats](t)(f.store.Items().Stats(t.Context(), f.items[0].ID))
			if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
				t.Fatal("failed private admission changed public statistics")
			}
		})
	}
}

func TestUnitCleanupFailedCompletionRestoresExactRealPin(t *testing.T) {
	f := newCleanupFaultFixture(t)
	token := testkit.Get[api.CleanupAdmission](t)(f.store.CleanupExclusions().Admit(t.Context(), f.configuration.Key, f.items[0].ID))
	before := f.evidence(t)
	requireCleanupFault(t, f.failing("complete after write").Complete(t.Context(), token))
	if err := f.store.CleanupExclusions().Prepare(t.Context(), f.ref); !errors.Is(err, api.ErrCleanupExcluded) {
		t.Fatalf("failed completion lost an active/unknown pin: %v", err)
	}
	if !slices.Equal(before, f.evidence(t)) {
		t.Fatal("completion failure rewrote admitted-use evidence")
	}
	testkit.Must(t, f.store.CleanupExclusions().Complete(t.Context(), token))
	testkit.Must(t, f.store.CleanupExclusions().Prepare(t.Context(), f.ref))
}

func TestUnitCleanupPrepareFaultsNeverPublishAuthority(t *testing.T) {
	for _, stage := range []string{"source", "repository", "closed", "barrier", "evidence", "active", "prepare after write", "missing evidence capability", "missing version"} {
		t.Run(stage, func(t *testing.T) {
			f := newCleanupFaultFixture(t)
			err := f.failing(stage).Prepare(t.Context(), f.ref)
			switch stage {
			case "missing evidence capability":
				if !errors.Is(err, kernel.ErrStore) {
					t.Fatalf("missing evidence admitted cleanup: %v", err)
				}
			case "missing version":
				if !errors.Is(err, kernel.ErrConflict) {
					t.Fatalf("incomplete version identity admitted cleanup: %v", err)
				}
			default:
				requireCleanupFault(t, err)
			}
			requireNoCleanupGate(t, f)
			// A changed configuration may reuse the failed operation ID only if the
			// failed publication rolled its immutable binding back as well as its gate.
			configuration := f.configuration
			configuration.DownloadRedirect = repo.Flex(true)
			testkit.Must(t, f.store.Repos().Update(t.Context(), configuration))
			source := testkit.Get[model.SourceIdentity](t)(f.store.RemoteCache().Source(t.Context(), configuration.Key))
			current := f.ref
			current.Generation = string(source.Generation)
			testkit.Must(t, f.store.CleanupExclusions().Prepare(t.Context(), current))
			testkit.Must(t, f.store.CleanupExclusions().Guard(t.Context(), current))
		})
	}
}

func TestUnitCleanupReleaseFaultsCannotOpenOrTerminalizeGate(t *testing.T) {
	for _, stage := range []string{"closed", "close after write", "release after write"} {
		t.Run(stage, func(t *testing.T) {
			f := newCleanupFaultFixture(t)
			actual := f.store.CleanupExclusions()
			testkit.Must(t, actual.Prepare(t.Context(), f.ref))
			requireCleanupFault(t, f.failing(stage).Release(t.Context(), f.ref, func(context.Context) error { return nil }))
			testkit.Must(t, actual.Guard(t.Context(), f.ref))
			// Idempotent Prepare still succeeding proves no terminal binding leaked.
			testkit.Must(t, actual.Prepare(t.Context(), f.ref))
			if _, err := actual.Admit(t.Context(), f.configuration.Key, f.items[1].ID); !errors.Is(err, api.ErrCleanupExcluded) {
				t.Fatalf("failed release opened response admission: %v", err)
			}
			testkit.Must(t, actual.Release(t.Context(), f.ref, func(context.Context) error { return nil }))
			if err := actual.Prepare(t.Context(), f.ref); !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("successful terminal release was not irreversible: %v", err)
			}
			token := testkit.Get[api.CleanupAdmission](t)(actual.Admit(t.Context(), f.configuration.Key, f.items[1].ID))
			testkit.Must(t, actual.Complete(t.Context(), token))
		})
	}
}

func TestUnitCleanupPublicationUsesCompleteLifetimeAndVersionBinding(t *testing.T) {
	f := newCleanupFaultFixture(t)
	actual := f.store.CleanupExclusions()
	for _, test := range []struct {
		name   string
		change func(*api.CleanupExclusion)
	}{
		{name: "missing owner", change: func(ref *api.CleanupExclusion) { ref.Owner = "" }},
		{name: "duplicate immutable version", change: func(ref *api.CleanupExclusion) { ref.Versions[1] = ref.Versions[0] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := f.ref
			ref.Versions = slices.Clone(f.ref.Versions)
			test.change(&ref)
			if err := actual.Prepare(t.Context(), ref); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("invalid binding obtained cleanup authority: %v", err)
			}
			requireNoCleanupGate(t, f)
		})
	}
	testkit.Must(t, actual.Prepare(t.Context(), f.ref))
	reversed := f.ref
	reversed.Versions = slices.Clone(f.ref.Versions)
	slices.Reverse(reversed.Versions)
	testkit.Must(t, actual.Guard(t.Context(), reversed))
	duplicate := f.ref
	duplicate.Versions = slices.Clone(f.ref.Versions)
	duplicate.Versions[1] = duplicate.Versions[0]
	if _, err := f.store.Items().
		Delete(api.WithCleanupExclusion(t.Context(), duplicate), f.configuration.Key, false, f.items[0].FullPath()); !errors.Is(
		err,
		api.ErrCleanupExcluded,
	) {
		t.Fatalf("duplicate forged context deleted real bytes: %v", err)
	}
	stale := f.ref
	stale.Generation = "old-generation"
	if err := actual.Release(t.Context(), stale, func(context.Context) error { return nil }); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("foreign binding released current gate: %v", err)
	}
	testkit.Must(t, actual.Guard(t.Context(), f.ref))
	for _, item := range f.items {
		testkit.Get[model.Item](t)(f.store.Items().GetByID(t.Context(), item.ID))
	}
	testkit.Must(t, actual.Release(t.Context(), reversed, func(context.Context) error { return nil }))
	if err := actual.Guard(t.Context(), f.ref); !errors.Is(err, api.ErrCleanupExcluded) {
		t.Fatalf("equivalent ordered release did not close exact barrier: %v", err)
	}
}

func TestUnitCleanupGuardOutagePreservesPreparedAuthorityAndBytes(t *testing.T) {
	for _, stage := range []string{"source", "repository", "barrier"} {
		t.Run(stage, func(t *testing.T) {
			f := newCleanupFaultFixture(t)
			actual := f.store.CleanupExclusions()
			testkit.Must(t, actual.Prepare(t.Context(), f.ref))
			requireCleanupFault(t, f.failing(stage).Guard(t.Context(), f.ref))
			// An unavailable guard is neither destructive authority nor a release.
			// The durable owner still excludes ordinary mutation and new responses.
			testkit.Must(t, actual.Guard(t.Context(), f.ref))
			if _, err := f.store.Items().Delete(t.Context(), f.configuration.Key, false, f.items[0].FullPath()); !errors.Is(err, api.ErrCleanupExcluded) {
				t.Fatalf("guard outage exposed prepared content to deletion: %v", err)
			}
			if _, err := actual.Admit(t.Context(), f.configuration.Key, f.items[1].ID); !errors.Is(err, api.ErrCleanupExcluded) {
				t.Fatalf("guard outage reopened response admission: %v", err)
			}
			for _, item := range f.items {
				current := testkit.Get[model.Item](t)(f.store.Items().GetByID(t.Context(), item.ID))
				if current.Actual != item.Actual || current.Size != item.Size {
					t.Fatal("guard outage changed the prepared content")
				}
			}
			testkit.Must(t, actual.Release(t.Context(), f.ref, func(context.Context) error { return nil }))
		})
	}
}

func TestUnitCleanupInvalidReleaseCannotOpenOrClosePreparedIdentity(t *testing.T) {
	for _, name := range []string{"malformed binding", "missing authorization"} {
		t.Run(name, func(t *testing.T) {
			f := newCleanupFaultFixture(t)
			actual := f.store.CleanupExclusions()
			testkit.Must(t, actual.Prepare(t.Context(), f.ref))
			ref := f.ref
			called := false
			authorize := func(context.Context) error { called = true; return nil }
			if name == "malformed binding" {
				ref.ID = ""
			} else {
				authorize = nil
			}
			if err := actual.Release(t.Context(), ref, authorize); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("invalid release acquired permission to change the gate: %v", err)
			}
			if called {
				t.Fatal("malformed binding reached irreversible-outcome authorization")
			}
			testkit.Must(t, actual.Guard(t.Context(), f.ref))
			// A spurious closed marker would make this same-ID retry conflict.
			testkit.Must(t, actual.Prepare(t.Context(), f.ref))
			if _, err := actual.Admit(t.Context(), f.configuration.Key, f.items[0].ID); !errors.Is(err, api.ErrCleanupExcluded) {
				t.Fatalf("invalid release reopened client admission: %v", err)
			}
			testkit.Must(t, actual.Release(t.Context(), f.ref, func(context.Context) error { return nil }))
			token := testkit.Get[api.CleanupAdmission](t)(actual.Admit(t.Context(), f.configuration.Key, f.items[0].ID))
			testkit.Must(t, actual.Complete(t.Context(), token))
		})
	}
}

func TestUnitCleanupEmptyCompletionCannotReleaseAnotherResponse(t *testing.T) {
	f := newCleanupFaultFixture(t)
	actual := f.store.CleanupExclusions()
	token := testkit.Get[api.CleanupAdmission](t)(actual.Admit(t.Context(), f.configuration.Key, f.items[0].ID))
	before := f.evidence(t)
	// A response that opted out may complete with no private token. Even if
	// descriptive repository fields are present, it cannot complete this read.
	empty := api.CleanupAdmission{Repository: token.Repository, Incarnation: token.Incarnation}
	testkit.Must(t, actual.Complete(t.Context(), empty))
	if err := actual.Prepare(t.Context(), f.ref); !errors.Is(err, api.ErrCleanupExcluded) {
		t.Fatalf("empty completion released another response's real pin: %v", err)
	}
	if !slices.Equal(before, f.evidence(t)) {
		t.Fatal("empty completion rewrote private use evidence")
	}
	testkit.Must(t, actual.Complete(t.Context(), token))
	testkit.Must(t, actual.Prepare(t.Context(), f.ref))
}

func TestUnitCleanupAuthorityDistinguishesLocalAndRemoteRepositories(t *testing.T) {
	t.Run("local responses stay tracked without federated deletion authority", func(t *testing.T) {
		f := newCleanupFaultFixture(t)
		configuration := f.configuration
		configuration.Class = repo.Local
		testkit.Must(t, f.store.Repos().Update(t.Context(), configuration))
		source := testkit.Get[model.SourceIdentity](t)(f.store.RemoteCache().Source(t.Context(), configuration.Key))
		ref := f.ref
		ref.Incarnation, ref.Generation = string(source.Repository), string(source.Generation)
		actual := f.store.CleanupExclusions()
		if err := actual.Prepare(t.Context(), ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("local repository acquired federated deletion authority: %v", err)
		}
		if err := actual.Guard(t.Context(), ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("local repository accepted a federated destructive guard: %v", err)
		}
		token := testkit.Get[api.CleanupAdmission](t)(actual.Admit(t.Context(), configuration.Key, f.items[0].ID))
		if token.ID == "" {
			t.Fatal("ordinary local response lost its migration protection")
		}
		testkit.Must(t, actual.Complete(t.Context(), token))
		for _, item := range f.items {
			testkit.Get[model.Item](t)(f.store.Items().GetByID(t.Context(), item.ID))
		}
		stats := testkit.Get[model.ItemStats](t)(f.store.Items().Stats(t.Context(), f.items[0].ID))
		if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
			t.Fatal("private local tracking changed public download statistics")
		}
	})
	t.Run("remote caches do not acquire hosted pins", func(t *testing.T) {
		f := newCleanupFaultFixture(t)
		configuration := f.configuration
		configuration.Class, configuration.URL = repo.Remote, "https://upstream.example"
		testkit.Must(t, f.store.Repos().Update(t.Context(), configuration))
		before := f.evidence(t)
		actual := f.store.CleanupExclusions()
		token := testkit.Get[api.CleanupAdmission](t)(actual.Admit(t.Context(), configuration.Key, f.items[0].ID))
		if token != (api.CleanupAdmission{}) {
			t.Fatalf("remote cache acquired a hosted response pin: %+v", token)
		}
		testkit.Must(t, actual.Complete(t.Context(), token))
		if !slices.Equal(before, f.evidence(t)) {
			t.Fatal("remote cache opt-out changed hosted use evidence")
		}
		// Returning to hosted federation must find no hidden remote pin.
		testkit.Must(t, f.store.Repos().Update(t.Context(), f.configuration))
		source := testkit.Get[model.SourceIdentity](t)(f.store.RemoteCache().Source(t.Context(), configuration.Key))
		ref := f.ref
		ref.Incarnation, ref.Generation = string(source.Repository), string(source.Generation)
		testkit.Must(t, actual.Prepare(t.Context(), ref))
		testkit.Must(t, actual.Guard(t.Context(), ref))
	})
}
