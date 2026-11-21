package transfer

import (
	"context"
	"errors"
	"io"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

type failedSourceStore struct {
	Store
	items port.ItemStore
}

func (s failedSourceStore) Items() port.ItemStore { return s.items }

type failedSourceItems struct {
	port.ItemStore
	operation string
}

func (s failedSourceItems) Get(ctx context.Context, repository string, cached bool, path string) (model.Item, error) {
	if s.operation == "exact root" {
		return model.Item{}, errFixture
	}
	return s.ItemStore.Get(ctx, repository, cached, path)
}

func (s failedSourceItems) DescendantsByFullPath(ctx context.Context, repository string, cached bool, path string, page kernel.Page) ([]model.Item, string, error) {
	if s.operation == "page" {
		return nil, "", errFixture
	}
	return s.ItemStore.DescendantsByFullPath(ctx, repository, cached, path, page)
}

func (s failedSourceItems) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if s.operation == "properties" {
		return nil, errFixture
	}
	return s.ItemStore.Properties(ctx, id)
}

func (s failedSourceItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if s.operation == "refresh" {
		return model.Item{}, errFixture
	}
	return s.ItemStore.GetByID(ctx, id)
}

func (s failedSourceItems) Stats(ctx context.Context, id int64) (model.ItemStats, error) {
	if s.operation == "statistics" {
		return model.ItemStats{}, errFixture
	}
	return s.ItemStore.Stats(ctx, id)
}

func TestUnitSourceReadFaultsNeverDisclosePartialSnapshotsOrSettle(t *testing.T) {
	for _, operation := range []string{"exact root", "page", "properties", "refresh", "statistics"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			it := f.put(t, "selected/one.pkg", "original")
			expected := f.one(t, it.FullPath())
			f.source.Store = failedSourceStore{Store: f.db, items: failedSourceItems{ItemStore: f.db.Items(), operation: operation}}
			query := api.TransferSelection{Root: it.FullPath(), Properties: true, Statistics: true}
			if operation == "page" {
				query.Root = ""
			}
			rows, next, err := f.source.Items(t.Context(), f.identity, query, kernel.Page{Limit: 1})
			if !errors.Is(err, errFixture) || rows != nil || next != "" {
				t.Fatal("failed source read returned partial progress", err, len(rows), next)
			}
			if operation != "page" {
				called := false
				err := f.source.WithCurrent(t.Context(), expected, func(context.Context) error { called = true; return nil })
				if !errors.Is(err, errFixture) || called {
					t.Fatal("failed current read admitted a checkpoint", called, err)
				}
			}
		})
	}
}

func TestUnitSourceInvalidScopeAndAuthorityDoNotBecomeEmptySuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*sourceFixture)
		want error
	}{
		{"unavailable store", func(f *sourceFixture) { f.source.Store = nil }, kernel.ErrStore},
		{"failed guard", func(f *sourceFixture) { f.source.Guard = func(context.Context) error { return errFixture } }, errFixture},
		{"missing decider", func(f *sourceFixture) {
			f.source.Current = func(context.Context) (accessapi.Decider, error) { return nil, nil }
		}, kernel.ErrForbidden},
		{"nonlocal source", func(f *sourceFixture) {
			f.config.Class, f.config.URL = repo.Remote, "https://upstream.invalid/repository"
			if err := f.db.Repos().Update(t.Context(), f.config); err != nil {
				t.Fatal(err)
			}
		}, kernel.ErrInvalid},
		{"removed repository", func(f *sourceFixture) {
			if err := f.db.Repos().Delete(t.Context(), f.config.Key); err != nil {
				t.Fatal(err)
			}
		}, kernel.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.edit(f)
			got, err := f.source.Repository(t.Context(), f.config.Key)
			if !errors.Is(err, tc.want) || got.Key != "" {
				t.Fatal("invalid source became a usable identity", err, got)
			}
		})
	}
	f := newFixture(t)
	for _, tc := range []struct {
		name  string
		query api.TransferSelection
		page  kernel.Page
		want  error
	}{
		{"binary selection", api.TransferSelection{Include: "bad\x00pattern"}, kernel.Page{Limit: 1}, kernel.ErrInvalid},
		{"escaped scope", api.TransferSelection{Root: "a/../private"}, kernel.Page{Limit: 1}, kernel.ErrInvalid},
		{"invalid continuation encoding", api.TransferSelection{}, kernel.Page{Limit: 1, After: "*"}, kernel.ErrInvalidCursor},
		{"foreign continuation", api.TransferSelection{}, kernel.Page{Limit: 1, After: kernel.Cursor("a", "b")}, kernel.ErrInvalidCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, next, err := f.source.Items(t.Context(), f.identity, tc.query, tc.page)
			if !errors.Is(err, tc.want) || rows != nil || next != "" {
				t.Fatal("invalid scope became an exhausted page", err, rows, next)
			}
		})
	}
	if err := f.source.WithAbsent(t.Context(), f.identity, "missing.pkg", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.source.Items(t.Context(), f.identity, api.TransferSelection{Root: "."}, kernel.Page{Limit: 1}); err != nil {
		t.Fatal("repository-relative root refused", err)
	}
}

type sourceBlobFault struct {
	blob.Store
	mode   string
	closed *bool
}

type sourceObservedBody struct {
	io.ReadSeekCloser
	closed *bool
}

func (b sourceObservedBody) Close() error { *b.closed = true; return b.ReadSeekCloser.Close() }

func (b sourceBlobFault) Open(ctx context.Context, digest string) (io.ReadSeekCloser, blob.Info, error) {
	if b.mode == "backend fault" {
		return nil, blob.Info{}, errFixture
	}
	body, info, err := b.Store.Open(ctx, digest)
	if err != nil {
		return nil, info, err
	}
	info.Size++
	return sourceObservedBody{ReadSeekCloser: body, closed: b.closed}, info, nil
}

func TestUnitSourceOpenRejectsUnavailableOrInconsistentImmutableBody(t *testing.T) {
	for _, mode := range []string{"missing backend", "backend fault", "wrong body metadata", "revoked source"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			it := f.put(t, "one.pkg", "original")
			expected := f.one(t, it.FullPath())
			closed, want := false, errFixture
			switch mode {
			case "missing backend":
				f.source.Blobs, want = nil, kernel.ErrStore
			case "revoked source":
				f.decider.AllowsFunc = func(accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions) bool { return false }
				want = kernel.ErrForbidden
			default:
				f.source.Blobs = sourceBlobFault{Store: f.source.Blobs, mode: mode, closed: &closed}
				if mode == "wrong body metadata" {
					want = kernel.ErrConflict
				}
			}
			body, err := f.source.Open(t.Context(), expected)
			if !errors.Is(err, want) || body != nil || closed != (mode == "wrong body metadata") {
				t.Fatal("invalid immutable body admitted or leaked", err, body, closed)
			}
		})
	}
}
