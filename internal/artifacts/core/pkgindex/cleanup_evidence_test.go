package pkgindex

import (
	"context"
	"errors"
	"reflect"
	"testing"

	artinmem "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var errCleanupGuardRejected = errors.New("cleanup metadata guard rejected")

func TestUnitCleanupMetadataExecutesEveryGuard(t *testing.T) {
	store := artinmem.New(memtx.New())
	if err := store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "guarded", Class: repo.Federated})); err != nil {
		t.Fatal(err)
	}
	var calls []int
	guards := []func(context.Context) error{
		func(context.Context) error { calls = append(calls, 1); return nil },
		nil,
		func(context.Context) error { calls = append(calls, 2); return nil },
	}
	reader := NewCleanupMetadata(store, guards...)
	guards[0] = func(context.Context) error { return errCleanupGuardRejected }
	if _, err := reader.Repository(t.Context(), "guarded"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []int{1, 2}) {
		t.Fatalf("guards not snapshotted/executed in order: %v", calls)
	}
	calls = nil
	reader = NewCleanupMetadata(store,
		func(context.Context) error { calls = append(calls, 1); return nil },
		func(context.Context) error { calls = append(calls, 2); return errCleanupGuardRejected },
		func(context.Context) error { calls = append(calls, 3); return nil },
	)
	if _, err := reader.Repository(t.Context(), "guarded"); !errors.Is(err, errCleanupGuardRejected) {
		t.Fatalf("later authorization guard ignored: %v", err)
	}
	if !reflect.DeepEqual(calls, []int{1, 2}) {
		t.Fatalf("guards continued after refusal: %v", calls)
	}
}
