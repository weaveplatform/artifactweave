package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

var errCoordinatorUnknown = errors.New("coordinator unknown")

type exclusionFixture struct {
	store      port.Store
	exclusions api.CleanupExclusions
	config     repo.Config
	ref        api.CleanupExclusion
	items      [2]model.Item
	versions   [2]model.PackageVersion
	metadata   api.CleanupMetadata
}

func newExclusionFixture(t *testing.T, newStore Factory) exclusionFixture {
	t.Helper()
	s := newStore(t)
	f := exclusionFixture{store: s, metadata: pkgindex.NewCleanupMetadata(s)}
	provider, ok := s.(interface{ CleanupExclusions() api.CleanupExclusions })
	if !ok {
		t.Fatal("artifact store has no durable exclusion binding")
	}
	f.exclusions = provider.CleanupExclusions()
	f.config = *repo.Defaults(&repo.Config{Key: "exclusion", Class: repo.Federated, PackageType: repo.NPM})
	storekit.Must(t, s.Repos().Create(t.Context(), f.config))
	pkg := storekit.Get[model.Package](t)(s.Packages().Ensure(t.Context(), model.Package{RepoKey: f.config.Key, Type: repo.NPM, Name: "widget"}))
	for index, version := range []string{"1", "2"} {
		f.versions[index] = storekit.Get[model.PackageVersion](
			t,
		)(
			s.Packages().PutVersion(t.Context(), model.PackageVersion{PackageID: pkg.ID, Version: version, Created: storekit.At(1)}),
		)
		f.items[index] = storekit.Get[model.Item](
			t,
		)(
			s.Items().Put(t.Context(), model.Item{RepoKey: f.config.Key, Name: version + ".tgz", Size: 1, Actual: kernel.Checksums{SHA256: version}, Created: storekit.At(1)}),
		)
		storekit.Must(t, s.Packages().LinkFile(t.Context(), model.VersionFile{VersionID: f.versions[index].ID, ItemID: f.items[index].ID}))
	}
	source := storekit.Get[api.CleanupRepository](t)(f.metadata.Repository(t.Context(), f.config.Key))
	f.ref = api.CleanupExclusion{
		ID:          "intent",
		Owner:       "coordinator",
		Repository:  f.config.Key,
		Incarnation: source.Incarnation,
		Generation:  source.Generation,
		Versions:    []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: "1"}, ID: f.versions[0].ID}},
	}
	return f
}

func wantExcluded(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, api.ErrCleanupExcluded) {
		t.Fatalf("want durable exclusion, got %v", err)
	}
}

// RunCleanupExclusions is shared by memory, SQLite and PostgreSQL. The calls
// establish both serial orders without sleeps or assumptions about I/O speed.
func RunCleanupExclusions(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("admission precedes preparation", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		before := storekit.Get[[]api.VersionDownload](t)(f.metadata.Downloads(ctx, storekit.Get[api.CleanupRepository](t)(f.metadata.Repository(ctx, f.config.Key)), []api.DownloadVersion{f.ref.Versions[0].DownloadVersion}))[0]
		token := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, f.config.Key, f.items[0].ID))
		if token.ID == "" {
			t.Fatal("federated GET was untracked")
		}
		wantExcluded(t, f.exclusions.Prepare(ctx, f.ref))
		wantExcluded(t, f.exclusions.Guard(api.WithCleanupExclusion(ctx, f.ref), f.ref))
		stats := storekit.Get[model.ItemStats](t)(f.store.Items().Stats(ctx, f.items[0].ID))
		if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
			t.Fatal("private admission changed public statistics")
		}
		after := storekit.Get[[]api.VersionDownload](t)(f.metadata.Downloads(ctx, storekit.Get[api.CleanupRepository](t)(f.metadata.Repository(ctx, f.config.Key)), []api.DownloadVersion{f.ref.Versions[0].DownloadVersion}))[0]
		if after.AdmittedAt.IsZero() || after.AdmissionRevision == before.AdmissionRevision || after.Revision != before.Revision || after.LastDownloaded != before.LastDownloaded {
			t.Fatalf("private admission evidence was not independent: before=%+v after=%+v", before, after)
		}
		// A new binding over the same durable store still sees the unfinished pin.
		provider := f.store.(interface{ CleanupExclusions() api.CleanupExclusions })
		wantExcluded(t, provider.CleanupExclusions().Prepare(ctx, f.ref))
		foreign := token
		foreign.Incarnation = "another-lifetime"
		if err := f.exclusions.Complete(ctx, foreign); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("foreign completion: %v", err)
		}
		wantExcluded(t, f.exclusions.Prepare(ctx, f.ref))
		storekit.Must(t, f.exclusions.Complete(ctx, token))
		storekit.Must(t, f.exclusions.Complete(ctx, token))
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
	})
	t.Run("preparation precedes admission and ordinary mutation", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
		_, err := f.exclusions.Admit(ctx, f.config.Key, f.items[1].ID)
		wantExcluded(t, err)
		wantExcluded(t, f.store.Repos().Update(ctx, f.config))
		wantExcluded(t, f.store.Repos().Delete(ctx, f.config.Key))
		wantExcluded(t, f.store.Repos().Update(artifactevents.WithRemote(ctx), f.config))
		wantExcluded(t, f.store.Repos().Delete(artifactevents.WithRemote(ctx), f.config.Key))
		_, err = f.store.Items().Put(ctx, f.items[0])
		wantExcluded(t, err)
		_, err = f.store.Items().Delete(ctx, f.config.Key, false, f.items[0].FullPath())
		wantExcluded(t, err)
		wantExcluded(t, f.store.Items().SetProperties(ctx, f.items[0].ID, model.Properties{"a": {"b"}}, storekit.At(3)))
		_, err = f.store.Packages().PutVersion(ctx, f.versions[0])
		wantExcluded(t, err)
		wantExcluded(t, f.store.Packages().DeleteVersion(ctx, f.versions[0].ID))
		forged := f.ref
		forged.Owner = "foreign"
		_, err = f.store.Items().Delete(api.WithCleanupExclusion(ctx, forged), f.config.Key, false, f.items[0].FullPath())
		wantExcluded(t, err)
		other := f.ref
		other.ID = "another-intent"
		wantExcluded(t, f.exclusions.Prepare(ctx, other))
		// Ordered content application remains possible, including cleanup tombstones.
		_, err = f.store.Items().Put(artifactevents.WithRemote(ctx), f.items[1])
		storekit.Must(t, err)
		_, err = f.store.Items().Delete(artifactevents.WithRemote(ctx), f.config.Key, false, f.items[1].FullPath())
		storekit.Must(t, err)
		storekit.Must(t, f.exclusions.Guard(ctx, f.ref))
		_, err = f.store.Items().Delete(api.WithCleanupExclusion(ctx, f.ref), f.config.Key, false, f.items[0].FullPath())
		storekit.Must(t, err)
	})
	t.Run("other version admission drains before repository exclusion", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		token := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, f.config.Key, f.items[1].ID))
		wantExcluded(t, f.exclusions.Prepare(ctx, f.ref))
		wantExcluded(t, f.exclusions.Guard(ctx, f.ref))
		// A native hook may delete this other version's file. Ordered federation
		// tombstones bypass ordinary content guards, so acknowledgment must wait
		// for this response even though the requested version has no readers.
		unrelated := *repo.Defaults(&repo.Config{Key: "other-repository", Class: repo.Federated, PackageType: repo.NPM})
		storekit.Must(t, f.store.Repos().Create(ctx, unrelated))
		other := storekit.Get[model.Item](t)(f.store.Items().Put(ctx, model.Item{RepoKey: unrelated.Key, Name: "content", Size: 1}))
		independent := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, unrelated.Key, other.ID))
		storekit.Must(t, f.exclusions.Complete(ctx, token))
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
		_, err := f.store.Items().Delete(api.WithCleanupExclusion(ctx, f.ref), f.config.Key, false, f.items[1].FullPath())
		storekit.Must(t, err)
		storekit.Must(t, f.exclusions.Complete(ctx, independent))
	})
	t.Run("release needs durable authorization and closes replay", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
		denied := errCoordinatorUnknown
		err := f.exclusions.Release(ctx, f.ref, func(context.Context) error { return denied })
		if !errors.Is(err, denied) {
			t.Fatalf("release authorization: %v", err)
		}
		_, err = f.exclusions.Admit(ctx, f.config.Key, f.items[0].ID)
		wantExcluded(t, err)
		authorized := func(context.Context) error { return nil }
		storekit.Must(t, f.exclusions.Release(ctx, f.ref, authorized))
		if err := f.exclusions.Prepare(ctx, f.ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("terminal reservation replayed: %v", err)
		}
		next := f.ref
		next.ID = "new-intent"
		storekit.Must(t, f.exclusions.Prepare(ctx, next))
		storekit.Must(t, f.exclusions.Release(ctx, f.ref, authorized))
		storekit.Must(t, f.exclusions.Guard(ctx, next))
		storekit.Must(t, f.exclusions.Release(ctx, next, authorized))
	})
	t.Run("unknown mapping is repository local", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		unlinked := storekit.Get[model.Item](t)(f.store.Items().Put(ctx, model.Item{RepoKey: f.config.Key, Name: "unlinked", Size: 1}))
		token := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, f.config.Key, unlinked.ID))
		wantExcluded(t, f.exclusions.Prepare(ctx, f.ref))
		other := *repo.Defaults(&repo.Config{Key: "unrelated", Class: repo.Federated, PackageType: repo.NPM})
		storekit.Must(t, f.store.Repos().Create(ctx, other))
		it := storekit.Get[model.Item](t)(f.store.Items().Put(ctx, model.Item{RepoKey: other.Key, Name: "artifact", Size: 1}))
		admitted := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, other.Key, it.ID))
		storekit.Must(t, f.exclusions.Complete(ctx, admitted))
		if _, err := f.exclusions.Admit(ctx, other.Key, f.items[0].ID); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("foreign item admitted: %v", err)
		}
		storekit.Must(t, f.exclusions.Complete(ctx, token))
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
	})
	t.Run("captured links survive replacement", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		token := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, f.config.Key, f.items[0].ID))
		storekit.Must(t, f.store.Packages().DeleteVersion(ctx, f.versions[0].ID))
		replacement := storekit.Get[model.PackageVersion](t)(f.store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: f.versions[0].PackageID, Version: "1"}))
		storekit.Must(t, f.store.Packages().LinkFile(ctx, model.VersionFile{VersionID: replacement.ID, ItemID: f.items[0].ID}))
		if err := f.exclusions.Prepare(ctx, f.ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("old immutable version identity reused: %v", err)
		}
		current := f.ref
		current.Versions = append([]api.CleanupVersionIdentity(nil), f.ref.Versions...)
		current.Versions[0].ID = replacement.ID
		wantExcluded(t, f.exclusions.Prepare(ctx, current))
		storekit.Must(t, f.exclusions.Complete(ctx, token))
		storekit.Must(t, f.exclusions.Prepare(ctx, current))
	})
	t.Run("repository recreation cannot adopt an old permit", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		storekit.Must(t, f.store.Repos().Delete(ctx, f.config.Key))
		storekit.Must(t, f.store.Repos().Create(ctx, f.config))
		if err := f.exclusions.Prepare(ctx, f.ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("recreated repository adopted old exclusion: %v", err)
		}
	})
	runCleanupReleaseRecovery(t, newStore)
}

func runCleanupReleaseRecovery(t *testing.T, newStore Factory) {
	t.Helper()
	authorized := func(context.Context) error { return nil }
	for _, change := range []struct {
		name  string
		apply func(*testing.T, exclusionFixture)
	}{
		{name: "changed configuration", apply: func(t *testing.T, f exclusionFixture) {
			configuration := f.config
			configuration.DownloadRedirect = repo.Flex(true)
			storekit.Must(t, f.store.Repos().Update(t.Context(), configuration))
		}},
		{name: "missing repository", apply: func(t *testing.T, f exclusionFixture) {
			storekit.Must(t, f.store.Repos().Delete(t.Context(), f.config.Key))
		}},
		{name: "recreated repository", apply: func(t *testing.T, f exclusionFixture) {
			storekit.Must(t, f.store.Repos().Delete(t.Context(), f.config.Key))
			storekit.Must(t, f.store.Repos().Create(t.Context(), f.config))
		}},
	} {
		t.Run("closed release survives "+change.name, func(t *testing.T) {
			f := newExclusionFixture(t, newStore)
			ctx := t.Context()
			storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
			storekit.Must(t, f.exclusions.Release(ctx, f.ref, authorized))
			change.apply(t, f)
			called := false
			storekit.Must(t, f.exclusions.Release(ctx, f.ref, func(context.Context) error { called = true; return kernel.ErrConflict }))
			if called {
				t.Fatal("known terminal release rechecked current outcome/applied authorization")
			}
			storekit.Must(t, f.exclusions.Release(ctx, f.ref, nil))
			forged := f.ref
			forged.Generation = "forged-generation"
			if err := f.exclusions.Release(ctx, forged, authorized); !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("terminal binding changed: %v", err)
			}
		})
		t.Run("never prepared abort survives "+change.name, func(t *testing.T) {
			f := newExclusionFixture(t, newStore)
			ctx := t.Context()
			change.apply(t, f)
			called := false
			storekit.Must(t, f.exclusions.Release(ctx, f.ref, func(callbackCtx context.Context) error {
				called = true
				if runner, ok := f.store.(interface{ InTx(context.Context) bool }); !ok || !runner.InTx(callbackCtx) {
					return kernel.ErrStore
				}
				return nil
			}))
			if !called {
				t.Fatal("new terminal binding skipped irreversible outcome authorization")
			}
			storekit.Must(t, f.exclusions.Release(ctx, f.ref, nil))
			if err := f.exclusions.Prepare(ctx, f.ref); err == nil {
				t.Fatal("late old prepare resurrected aborted reservation")
			}
		})
	}
	t.Run("never prepared abort preserves replacement barrier", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		configuration := f.config
		configuration.DownloadRedirect = repo.Flex(true)
		storekit.Must(t, f.store.Repos().Update(ctx, configuration))
		repository := storekit.Get[api.CleanupRepository](t)(f.metadata.Repository(ctx, f.config.Key))
		replacement := f.ref
		replacement.ID = "replacement"
		replacement.Generation = repository.Generation
		storekit.Must(t, f.exclusions.Prepare(ctx, replacement))
		storekit.Must(t, f.exclusions.Release(ctx, f.ref, authorized))
		storekit.Must(t, f.exclusions.Guard(ctx, replacement))
		_, err := f.exclusions.Admit(ctx, f.config.Key, f.items[1].ID)
		wantExcluded(t, err)
		storekit.Must(t, f.exclusions.Release(ctx, f.ref, nil))
		storekit.Must(t, f.exclusions.Guard(ctx, replacement))
	})
	t.Run("forged release cannot close an open immutable binding", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		storekit.Must(t, f.exclusions.Prepare(ctx, f.ref))
		forged := f.ref
		forged.Generation = "foreign"
		if err := f.exclusions.Release(ctx, forged, authorized); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("forged close: %v", err)
		}
		storekit.Must(t, f.exclusions.Guard(ctx, f.ref))
		storekit.Must(t, f.exclusions.Release(ctx, f.ref, authorized))
		if err := f.exclusions.Prepare(ctx, f.ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("closed binding reopened: %v", err)
		}
	})
	t.Run("release races an uncommitted prepare", func(t *testing.T) {
		f := newExclusionFixture(t, newStore)
		ctx := t.Context()
		prepared := make(chan struct{})
		commit := make(chan struct{})
		prepareDone := make(chan error, 1)
		go func() {
			prepareDone <- f.store.Tx(ctx, func(txCtx context.Context) error {
				if err := f.exclusions.Prepare(txCtx, f.ref); err != nil {
					return fmt.Errorf("prepare exclusion fixture: %w", err)
				}
				close(prepared)
				select {
				case <-commit:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		select {
		case <-prepared:
		case err := <-prepareDone:
			t.Fatalf("prepare: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		released := make(chan error, 1)
		releaseStarted := make(chan struct{})
		go func() { close(releaseStarted); released <- f.exclusions.Release(ctx, f.ref, authorized) }()
		<-releaseStarted
		close(commit)
		storekit.Must(t, <-prepareDone)
		storekit.Must(t, <-released)
		storekit.Must(t, f.exclusions.Release(ctx, f.ref, nil))
		if err := f.exclusions.Prepare(ctx, f.ref); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("late prepare reopened race winner: %v", err)
		}
		token := storekit.Get[api.CleanupAdmission](t)(f.exclusions.Admit(ctx, f.config.Key, f.items[0].ID))
		storekit.Must(t, f.exclusions.Complete(ctx, token))
	})
}
