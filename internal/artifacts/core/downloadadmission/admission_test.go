package downloadadmission_test

import (
	"context"
	"errors"
	"testing"
	"time"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/downloadadmission"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	testkit "github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var errResponseCompletionUnavailable = errors.New("response completion persistence unavailable")

type responseAdmissionFixture struct {
	store         *artinmem.Store
	configuration repo.Config
	item          model.Item
	ref           api.CleanupExclusion
}

func newResponseAdmissionFixture(t *testing.T) responseAdmissionFixture {
	t.Helper()
	f := responseAdmissionFixture{store: artinmem.New(memtx.New()), configuration: *repo.Defaults(&repo.Config{Key: "response", Class: repo.Federated, PackageType: repo.NPM})}
	ctx := t.Context()
	testkit.Must(t, f.store.Repos().Create(ctx, f.configuration))
	pkg := testkit.Get[model.Package](t)(f.store.Packages().Ensure(ctx, model.Package{RepoKey: f.configuration.Key, Type: repo.NPM, Name: "widget"}))
	version := testkit.Get[model.PackageVersion](t)(f.store.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg.ID, Version: "1"}))
	f.item = testkit.Get[model.Item](
		t,
	)(
		f.store.Items().Put(ctx, model.Item{RepoKey: f.configuration.Key, Name: "widget.tgz", Size: 3, Actual: kernel.Checksums{SHA256: "immutable-response"}}),
	)
	testkit.Must(t, f.store.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: f.item.ID}))
	source := testkit.Get[model.SourceIdentity](t)(f.store.RemoteCache().Source(ctx, f.configuration.Key))
	f.ref = api.CleanupExclusion{
		ID:          "cleanup",
		Owner:       "coordinator",
		Repository:  f.configuration.Key,
		Incarnation: string(source.Repository),
		Generation:  string(source.Generation),
		Versions:    []api.CleanupVersionIdentity{{DownloadVersion: api.DownloadVersion{Package: pkg.Name, Version: version.Version}, ID: version.ID}},
	}
	return f
}

// These decorators remove or fail runtime capabilities while the private token
// itself is still issued and owned by the real transactional artifact store.
type admissionRuntimeOnly struct{ exclusions api.CleanupExclusions }

func (s admissionRuntimeOnly) CleanupExclusions() api.CleanupExclusions { return s.exclusions }

type admissionRuntimeReader struct {
	admissionRuntimeOnly
	items port.ItemStore
}

func (s admissionRuntimeReader) Items() port.ItemStore { return s.items }

type admittedItemRecheck struct {
	port.ItemStore
	missing   bool
	wrongKind bool
}

func (s admittedItemRecheck) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if s.missing {
		return model.Item{}, kernel.ErrNotFound
	}
	item, err := s.ItemStore.GetByID(ctx, id)
	if s.wrongKind {
		item.Folder = true
	}
	return item, err
}

type trackedResponseAdmission struct {
	api.CleanupExclusions
	token      api.CleanupAdmission
	completed  []api.CleanupAdmission
	completion func(context.Context, api.CleanupAdmission) error
}

func (s *trackedResponseAdmission) Admit(ctx context.Context, key string, id int64) (api.CleanupAdmission, error) {
	token, err := s.CleanupExclusions.Admit(ctx, key, id)
	s.token = token
	return token, err
}

func (s *trackedResponseAdmission) Complete(ctx context.Context, token api.CleanupAdmission) error {
	s.completed = append(s.completed, token)
	if s.completion != nil {
		return s.completion(ctx, token)
	}
	return s.CleanupExclusions.Complete(ctx, token)
}

func TestUnitBeginRequiresHostedRuntimeCapability(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		for _, runtime := range []struct {
			name  string
			store any
		}{{name: "missing capability", store: struct{}{}}, {name: "nil capability", store: admissionRuntimeOnly{}}} {
			t.Run(string(class)+"/"+runtime.name, func(t *testing.T) {
				configuration := *repo.Defaults(&repo.Config{Key: "hosted", Class: class})
				done, err := downloadadmission.Begin(t.Context(), runtime.store, configuration, model.Item{ID: 1, RepoKey: configuration.Key})
				if done != nil || !errors.Is(err, kernel.ErrStore) {
					t.Fatalf("untracked hosted response admitted: done=%v error=%v", done != nil, err)
				}
			})
		}
	}
	// Metadata-only item kinds and remote cache reads keep their established
	// opt-out, without requiring the hosted runtime capability.
	for _, test := range []struct {
		name  string
		class repo.Class
		item  model.Item
	}{
		{name: "remote cache", class: repo.Remote, item: model.Item{Cached: true}},
		{name: "folder metadata", class: repo.Federated, item: model.Item{Folder: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			done, err := downloadadmission.Begin(t.Context(), struct{}{}, repo.Config{Class: test.class}, test.item)
			if err != nil || done == nil {
				t.Fatalf("metadata opt-out refused: %v", err)
			}
			done()
		})
	}
}

func TestUnitBeginRecheckRefusalCompletesExactRealAdmission(t *testing.T) {
	for _, test := range []struct {
		name                                  string
		missingReader, missingItem, wrongKind bool
		want                                  error
	}{
		{name: "reader capability disappeared", missingReader: true, want: kernel.ErrStore},
		{name: "item disappeared", missingItem: true, want: kernel.ErrNotFound},
		{name: "item kind changed", wrongKind: true, want: kernel.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newResponseAdmissionFixture(t)
			tracked := &trackedResponseAdmission{CleanupExclusions: f.store.CleanupExclusions()}
			runtime := any(
				admissionRuntimeReader{
					admissionRuntimeOnly: admissionRuntimeOnly{exclusions: tracked},
					items:                admittedItemRecheck{ItemStore: f.store.Items(), missing: test.missingItem, wrongKind: test.wrongKind},
				},
			)
			if test.missingReader {
				runtime = admissionRuntimeOnly{exclusions: tracked}
			}
			done, err := downloadadmission.Begin(t.Context(), runtime, f.configuration, f.item)
			if done != nil || !errors.Is(err, test.want) {
				t.Fatalf("recheck did not refuse response: done=%v err=%v", done != nil, err)
			}
			if tracked.token.ID == "" || len(tracked.completed) != 1 || tracked.completed[0] != tracked.token {
				t.Fatalf("refusal lost or completed a foreign token: admitted=%+v completed=%+v", tracked.token, tracked.completed)
			}
			// Successful real preparation proves that rejection did not strand the
			// response's actual durable pin, rather than just calling a fake cleanup.
			testkit.Must(t, f.store.CleanupExclusions().Prepare(t.Context(), f.ref))
			stats := testkit.Get[model.ItemStats](t)(f.store.Items().Stats(t.Context(), f.item.ID))
			if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
				t.Fatal("private rejected response changed public statistics")
			}
		})
	}
}

type responseContextKey struct{}

func TestUnitBeginFailedCompletionPreservesUnknownPinAfterCancellation(t *testing.T) {
	f := newResponseAdmissionFixture(t)
	request, cancel := context.WithCancel(context.WithValue(t.Context(), responseContextKey{}, "response-owner"))
	defer cancel()
	tracked := &trackedResponseAdmission{CleanupExclusions: f.store.CleanupExclusions()}
	var completionContext context.Context
	tracked.completion = func(ctx context.Context, token api.CleanupAdmission) error {
		completionContext = ctx
		if ctx.Err() != nil || ctx.Value(responseContextKey{}) != "response-owner" {
			t.Errorf("completion inherited cancellation or lost request ownership: err=%v owner=%v", ctx.Err(), ctx.Value(responseContextKey{}))
		}
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.After(time.Now()) || time.Until(deadline) > 5*time.Second {
			t.Errorf("completion is not independently bounded: deadline=%v exists=%v", deadline, ok)
		}
		return errResponseCompletionUnavailable
	}
	runtime := admissionRuntimeReader{admissionRuntimeOnly: admissionRuntimeOnly{exclusions: tracked}, items: f.store.Items()}
	done, err := downloadadmission.Begin(request, runtime, f.configuration, f.item)
	testkit.Must(t, err)
	if done == nil || tracked.token.ID == "" {
		t.Fatal("hosted response did not retain its completion owner")
	}
	cancel()
	done()
	if completionContext == nil || len(tracked.completed) != 1 || tracked.completed[0] != tracked.token {
		t.Fatal("cancelled response did not complete its exact admission")
	}
	if err := f.store.CleanupExclusions().Prepare(t.Context(), f.ref); !errors.Is(err, api.ErrCleanupExcluded) {
		t.Fatalf("failed completion lost the real unknown admission: %v", err)
	}
	// Only durable completion of that same token releases the conservative pin.
	testkit.Must(t, f.store.CleanupExclusions().Complete(t.Context(), tracked.token))
	testkit.Must(t, f.store.CleanupExclusions().Prepare(t.Context(), f.ref))
	stats := testkit.Get[model.ItemStats](t)(f.store.Items().Stats(t.Context(), f.item.ID))
	if stats.DownloadCount != 0 || !stats.LastDownloaded.IsZero() {
		t.Fatal("private completion failure changed public download statistics")
	}
}
