package pull_test

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func propertyFile(t *testing.T, w *world, properties model.Properties) api.PullItem {
	t.Helper()
	w.peer.entries, w.peer.props = []pull.Entry{file("a")}, properties
	selection := api.PullSelection{Properties: true}
	repository := binding(t, w, selection)
	items, _, err := w.source.Items(t.Context(), repository, selection, kernel.Page{})
	if err != nil || len(items) != 1 {
		t.Fatal("property fixture", err)
	}
	return items[0]
}

type pullFaultStore struct {
	item.Store
	items port.ItemStore
	cache port.RemoteCacheStore
}

func (s pullFaultStore) Items() port.ItemStore              { return s.items }
func (s pullFaultStore) RemoteCache() port.RemoteCacheStore { return s.cache }

type pullFaultItems struct {
	port.ItemStore
	mode string
}

func (s pullFaultItems) Get(ctx context.Context, key string, cached bool, path string) (model.Item, error) {
	if s.mode == "snapshot item" {
		return model.Item{}, kernel.ErrStore
	}
	it, err := s.ItemStore.Get(ctx, key, cached, path)
	if s.mode == "snapshot folder" {
		it.Folder = true
	}
	return it, err
}

func (s pullFaultItems) Delete(ctx context.Context, key string, cached bool, path string) (port.DeleteResult, error) {
	if s.mode == "delete" {
		return port.DeleteResult{}, kernel.ErrStore
	}
	return s.ItemStore.Delete(ctx, key, cached, path)
}

func (s pullFaultItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if s.mode == "item" {
		return model.Item{}, kernel.ErrStore
	}
	return s.ItemStore.GetByID(ctx, id)
}

func (s pullFaultItems) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if s.mode == "properties" {
		return nil, kernel.ErrStore
	}
	return s.ItemStore.Properties(ctx, id)
}

func (s pullFaultItems) PatchProperties(ctx context.Context, id int64, props model.Properties, remove []string, at time.Time) error {
	if s.mode == "annotation write" {
		return kernel.ErrStore
	}
	return s.ItemStore.PatchProperties(ctx, id, props, remove, at)
}

type pullFaultCache struct {
	port.RemoteCacheStore
	mode       string
	exactCalls int
}

func (s *pullFaultCache) Source(ctx context.Context, key string) (model.SourceIdentity, error) {
	if s.mode == "snapshot source" {
		return model.SourceIdentity{}, kernel.ErrStore
	}
	identity, err := s.RemoteCacheStore.Source(ctx, key)
	if s.mode == "foreign snapshot" {
		identity.Generation = "other-generation"
	}
	return identity, err
}

func (s *pullFaultCache) List(ctx context.Context, key, prefix string, page kernel.Page) ([]model.RemoteCacheEntry, string, error) {
	if s.mode == "cache listing" {
		return nil, "", kernel.ErrStore
	}
	return s.RemoteCacheStore.List(ctx, key, prefix, page)
}

func (s *pullFaultCache) State(ctx context.Context, key string) (model.RemoteSourceState, error) {
	if s.mode == "policy" {
		return model.RemoteSourceState{}, kernel.ErrStore
	}
	return s.RemoteCacheStore.State(ctx, key)
}

func (s *pullFaultCache) Exact(ctx context.Context, key, path string, page kernel.Page) ([]model.RemoteCacheEntry, string, error) {
	s.exactCalls++
	if s.mode == "representations" || s.mode == "shared ownership" && s.exactCalls > 1 {
		return nil, "", kernel.ErrStore
	}
	return s.RemoteCacheStore.Exact(ctx, key, path, page)
}

func (s *pullFaultCache) Begin(ctx context.Context, key model.RemoteCacheKey) (model.RemoteCacheToken, error) {
	if s.mode == "current token" {
		return model.RemoteCacheToken{}, kernel.ErrStore
	}
	return s.RemoteCacheStore.Begin(ctx, key)
}

func (s *pullFaultCache) Check(ctx context.Context, token model.RemoteCacheToken) error {
	if s.mode == "token fence" {
		return kernel.ErrStore
	}
	return s.RemoteCacheStore.Check(ctx, token)
}

func (s *pullFaultCache) Put(ctx context.Context, token model.RemoteCacheToken, entry model.RemoteCacheEntry) (model.RemoteCacheEntry, error) {
	if s.mode == "ownership write" {
		return model.RemoteCacheEntry{}, kernel.ErrStore
	}
	return s.RemoteCacheStore.Put(ctx, token, entry)
}

func TestUnitPullCacheStorageFailuresCannotPublishReceiptsOrPartialProperties(t *testing.T) {
	for _, mode := range []string{"policy", "representations", "current token", "token fence", "item", "properties", "annotation write", "ownership write", "shared ownership"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture(t)
			selected := propertyFile(t, w, model.Properties{"quality": {"upstream"}})
			entry := retainedFile(t, w, selected)
			w.source.Read = func(context.Context, source.Request) (source.Result, error) {
				return source.Result{CacheEntry: &entry}, nil
			}
			w.source.Content.Store = pullFaultStore{
				Store: w.db,
				items: pullFaultItems{ItemStore: w.db.Items(), mode: mode},
				cache: &pullFaultCache{RemoteCacheStore: w.db.RemoteCache(), mode: mode},
			}
			grant := publication()
			recorded := false
			grant.Record = func(context.Context, api.PullCached) error { recorded = true; return nil }
			proof, err := w.source.Fetch(t.Context(), selected, grant)
			if !errors.Is(err, kernel.ErrStore) || proof.Version != "" || recorded {
				t.Fatal("failed storage check published cache success", mode, err)
			}
			properties, err := w.db.Items().Properties(t.Context(), entry.ItemID)
			if err != nil || len(properties) != 0 {
				t.Fatal("failed completion retained partial properties", mode, err)
			}
			current, err := w.db.RemoteCache().Read(t.Context(), entry.Token.Key)
			if err != nil || current.Token != entry.Token || current.Smart != nil {
				t.Fatal("failed completion changed cache authority or ownership", mode, err)
			}
		})
	}
}

func TestUnitPullPropertyCompletionCannotAnnotateAReplacedItem(t *testing.T) {
	w := fixture(t)
	selected := propertyFile(t, w, model.Properties{"quality": {"upstream"}})
	entry := retainedFile(t, w, selected)
	it, err := w.db.Items().GetByID(t.Context(), entry.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	it.Size++
	if _, err := w.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	w.source.Read = func(context.Context, source.Request) (source.Result, error) {
		return source.Result{CacheEntry: &entry}, nil
	}
	if proof, err := w.source.Fetch(t.Context(), selected, publication()); !errors.Is(err, source.ErrSuperseded) || proof.Version != "" {
		t.Fatal("old cache receipt annotated a replaced item", err)
	}
	if err := (pull.Source{}).WithCurrent(t.Context(), api.PullCached{}, nil); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("empty checkpoint authority accepted", err)
	}
}

func TestUnitPullPropertySyncRemovesOwnedValuesWhileKeepingLocalEdits(t *testing.T) {
	w := fixture(t)
	selected := propertyFile(t, w, model.Properties{"quality": {"remote"}, "obsolete": {"remote"}})
	entry := retainedFile(t, w, selected)
	w.source.Read = func(ctx context.Context, _ source.Request) (source.Result, error) {
		current, err := w.db.RemoteCache().Read(ctx, entry.Token.Key)
		return source.Result{CacheEntry: &current}, err
	}
	if _, err := w.source.Fetch(t.Context(), selected, publication()); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Items().PatchProperties(t.Context(), entry.ItemID, model.Properties{"quality": {"local"}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	selected = propertyFile(t, w, nil)
	if _, err := w.source.Fetch(t.Context(), selected, publication()); err != nil {
		t.Fatal(err)
	}
	properties, err := w.db.Items().Properties(t.Context(), entry.ItemID)
	if err != nil || len(properties) != 1 || len(properties["quality"]) != 1 || properties["quality"][0] != "local" {
		t.Fatal("property synchronization removed a local edit or kept obsolete owned values", properties, err)
	}
}

type failingPullBody struct{ io.Reader }

func (failingPullBody) Close() error { return kernel.ErrStore }

func TestUnitPullFetchRefusalsCannotBorrowAlreadyCachedBytes(t *testing.T) {
	for _, mode := range []string{"execution revoked", "file denied", "annotate denied", "inspection failed", "changed file", "read failed", "close failed", "publication revoked", "record rejected"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture(t)
			selected := propertyFile(t, w, nil)
			entry := retainedFile(t, w, selected)
			grant := publication()
			current := true
			grant.Check = func(context.Context) error {
				if !current {
					return kernel.ErrForbidden
				}
				return nil
			}
			want := error(kernel.ErrStore)
			w.source.Read = func(ctx context.Context, q source.Request) (source.Result, error) {
				if mode == "read failed" {
					return source.Result{}, kernel.ErrStore
				}
				if mode == "close failed" {
					return source.Result{CacheEntry: &entry, Stream: &source.Stream{Body: failingPullBody{strings.NewReader("data")}}}, nil
				}
				if mode == "publication revoked" {
					current = false
					if err := q.Publication.Check(ctx); err != nil {
						return source.Result{}, err
					}
				}
				return source.Result{CacheEntry: &entry}, nil
			}
			switch mode {
			case "execution revoked":
				grant.Check = func(context.Context) error { return kernel.ErrForbidden }
				want = kernel.ErrForbidden
			case "file denied":
				w.peer.denied[selected.Path], want = true, source.ErrDenied
			case "annotate denied":
				open := w.source.Open
				w.source.Open = func(ctx context.Context, key, root string) (*source.Service, source.Request, error) {
					svc, q, err := open(ctx, key, root)
					q.Access = &accessapitest.Decider{AllowsFunc: func(_ accessapi.ResourceType, _ accessapi.Repo, _ string, want accessapi.Actions) bool {
						return want == accessapi.ActionRead
					}}
					return svc, q, err
				}
				want = kernel.ErrForbidden
			case "inspection failed":
				w.peer.inspectErr = kernel.ErrStore
			case "changed file":
				w.peer.entries[0].Size++
				want = source.ErrSuperseded
			case "publication revoked":
				want = kernel.ErrForbidden
			case "record rejected":
				grant.Record = func(ctx context.Context, _ api.PullCached) error {
					if err := w.db.Settings().Put(ctx, "rejected-receipt", true); err != nil {
						return err
					}
					return kernel.ErrStore
				}
			}
			proof, err := w.source.Fetch(t.Context(), selected, grant)
			if !errors.Is(err, want) || proof.Version != "" {
				t.Fatal("refused attempt borrowed warm success", mode, err)
			}
			var checkpoint bool
			if err := w.db.Settings().Get(t.Context(), "rejected-receipt", &checkpoint); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("rejected receipt committed its checkpoint", err)
			}
		})
	}
}

func TestUnitPullReceiptAndReconciliationWindowsRefuseExcessRepresentations(t *testing.T) {
	w := fixture(t)
	selected := selectedFile(t, w)
	entry := retainedFile(t, w, selected)
	for n := range 65 {
		key := entry.Token.Key
		key.Variant = "other-" + strconv.Itoa(n)
		token, err := w.db.RemoteCache().Begin(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.RemoteCache().Put(t.Context(), token, entry); err != nil {
			t.Fatal(err)
		}
	}
	w.source.Read = func(context.Context, source.Request) (source.Result, error) {
		return source.Result{CacheEntry: &entry}, nil
	}
	if proof, err := w.source.Fetch(t.Context(), selected, publication()); !errors.Is(err, source.ErrInvalidResult) || proof.Version != "" {
		t.Fatal("unbounded receipt window became success", err)
	}
	if rows, next, err := w.source.CachedItems(
		t.Context(),
		selected.Repository,
		selected.Selection,
		kernel.Page{},
	); !errors.Is(err, source.ErrInvalidResult) || len(rows) != 0 ||
		next != "" {
		t.Fatal("unbounded deletion snapshot became a usable cache page", err)
	}
}

func TestUnitPullRawMetadataCannotPretendToSupportUserProperties(t *testing.T) {
	for _, properties := range []model.Properties{{"quality": {"upstream"}}, {"docker.manifest": {"protected"}}, {}} {
		w := fixture(t)
		selected := propertyFile(t, w, properties)
		entry := retainedFile(t, w, selected)
		entry.ItemID = 0
		var err error
		entry, err = w.db.RemoteCache().Put(t.Context(), entry.Token, entry)
		if err != nil {
			t.Fatal(err)
		}
		w.source.Read = func(context.Context, source.Request) (source.Result, error) {
			return source.Result{CacheEntry: &entry}, nil
		}
		proof, err := w.source.Fetch(t.Context(), selected, publication())
		if _, requested := properties["quality"]; requested {
			if !errors.Is(err, source.ErrUnsupported) || proof.Version != "" {
				t.Fatal("raw metadata claimed user-property support", err)
			}
		} else if err != nil || proof.Version == "" {
			t.Fatal("empty/protected-only properties blocked valid raw bytes", err)
		}
	}
}
