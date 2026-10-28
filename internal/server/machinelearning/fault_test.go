package machinelearning_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errInjected = errors.New("injected model publication failure")

type fault struct {
	enabled  atomic.Bool
	op, path string
	count    atomic.Int32
	after    int32
}

func (f *fault) fails(op, path string) bool {
	if !f.enabled.Load() || f.op != op || !strings.Contains(path, f.path) {
		return false
	}
	return f.count.Add(1) > f.after
}

type faultyStore struct {
	store.Store
	fault *fault
}

func (s faultyStore) Items() store.ItemStore { return faultyItems{s.Store.Items(), s.fault} }

type faultyItems struct {
	store.ItemStore
	fault *fault
}

func (s faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if s.fault.fails("get", path) {
		return types.Item{}, errInjected
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

func (s faultyItems) Create(ctx context.Context, it types.Item) (types.Item, error) {
	if s.fault.fails("create", it.FullPath()) {
		return types.Item{}, errInjected
	}
	return s.ItemStore.Create(ctx, it)
}

func (s faultyItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if s.fault.fails("properties", "") {
		return nil, errInjected
	}
	return s.ItemStore.Properties(ctx, id)
}

func (s faultyItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	page store.Page,
) ([]types.Item, string, error) {
	if s.fault.fails("descendants", path) {
		return nil, "", errInjected
	}
	return s.ItemStore.Descendants(ctx, key, cached, path, page)
}

type faultyBlobs struct {
	blob.Store
	fault *fault
}

func (s faultyBlobs) BeginUpload(
	ctx context.Context,
	opts ...blob.UploadOption,
) (blob.Upload, error) {
	if s.fault.fails("upload", "") {
		return nil, errInjected
	}
	return s.Store.BeginUpload(ctx, opts...)
}

func (s faultyBlobs) Stat(ctx context.Context, hash string) (blob.Info, error) {
	if s.fault.fails("stat", hash) {
		return blob.Info{}, errInjected
	}
	return s.Store.Stat(ctx, hash)
}

func withFault(f *fault) func(*server.Config) {
	return func(c *server.Config) { c.Store = faultyStore{c.Store, f}; c.Blobs = faultyBlobs{c.Blobs, f} }
}

func TestAccModelPublicationRollbackOnStorageFailure(t *testing.T) {
	for _, tc := range []struct {
		op, path string
		after    int32
	}{
		{"get", ".transaction.json", 0},
		{"get", "/1/model-manifest.json", 0},
		{"get", "/tmp/1/", 1},
		{"upload", "", 0},
		{"stat", "", 0},
		{"stat", "", 1},
		{"create", "/1/files/model.pkl", 0},
		{"create", "/1/model-manifest.json", 0},
		{"descendants", "models", 0},
	} {
		t.Run(tc.op+tc.path, func(t *testing.T) {
			failure := &fault{op: tc.op, path: tc.path, after: tc.after}
			f := newFixture(t, repo.Local, withFault(failure))
			tx := f.start("controlled", "1")
			data := []byte("model data")
			f.status(
				201,
				"PUT",
				"/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl",
				data,
				"",
				"",
			)
			failure.enabled.Store(true)
			f.commit("controlled", "1", tx, manifest(data), 500)
			failure.enabled.Store(false)
			for _, path := range []string{"models/controlled/1/files/model.pkl", "models/controlled/1/model-manifest.json"} {
				if _, err := f.db.Items().
					Get(t.Context(), "ml", false, path); !errors.Is(
					err,
					store.ErrNotFound,
				) {
					t.Fatalf("failed transaction exposed %s: %v", path, err)
				}
			}
			f.commit("controlled", "1", tx, manifest(data), 201)
		})
	}
}

func TestAccModelStartAndReplayStorageFailures(t *testing.T) {
	for _, op := range []string{"get", "upload", "create"} {
		t.Run("start/"+op, func(t *testing.T) {
			failure := &fault{op: op}
			failure.enabled.Store(true)
			f := newFixture(t, repo.Local, withFault(failure))
			f.status(500, "POST", api+"model/start-transaction/1", nil, "", "")
		})
	}
	for _, op := range []string{"properties", "stat"} {
		t.Run("replay/"+op, func(t *testing.T) {
			failure := &fault{op: op}
			f := newFixture(t, repo.Local, withFault(failure))
			tx := f.start("model", "1")
			data := []byte("model")
			f.status(
				201,
				"PUT",
				"/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl",
				data,
				"",
				"",
			)
			m := manifest(data)
			f.commit("model", "1", tx, m, 201)
			failure.enabled.Store(true)
			f.commit("model", "1", tx, m, 500)
			failure.enabled.Store(false)
			f.commit("model", "1", tx, m, 201)
		})
	}
}
