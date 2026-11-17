package storekit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var errCopyFault = errors.New("controlled virtual copy store failure")

type copyRows struct {
	old   virtual.Copy
	fault string
}

func (r copyRows) Lock(context.Context, string) error {
	if r.fault == "lock" {
		return errCopyFault
	}
	return nil
}

func (r copyRows) Read(context.Context, string, string) (virtual.Copy, error) {
	if r.fault == "read" {
		return virtual.Copy{}, errCopyFault
	}
	if r.old.Virtual == "" {
		return virtual.Copy{}, kernel.ErrNotFound
	}
	return r.old, nil
}

func (r copyRows) Write(context.Context, virtual.Copy) error {
	if r.fault == "write" {
		return errCopyFault
	}
	return nil
}

func (r copyRows) List(context.Context, string) ([]virtual.Copy, error) {
	if r.fault == "list" {
		return nil, errCopyFault
	}
	return []virtual.Copy{r.old}, nil
}

func (r copyRows) Delete(context.Context, string) error {
	if r.fault == "delete" {
		return errCopyFault
	}
	return nil
}

type copyFaultStore struct {
	port.Store
	fault string
}

func (s copyFaultStore) Blobs() port.BlobIndex {
	return copyFaultIndex{BlobIndex: s.Store.Blobs(), fault: s.fault}
}

type copyFaultIndex struct {
	port.BlobIndex
	fault string
}

func (b copyFaultIndex) Ref(ctx context.Context, v blob.Blob, n int64) (blob.Blob, error) {
	if b.fault == "ref" {
		return blob.Blob{}, errCopyFault
	}
	return b.BlobIndex.Ref(ctx, v, n)
}

func (b copyFaultIndex) Unref(ctx context.Context, sha string, n int64) (blob.Blob, error) {
	if b.fault == "unref" {
		return blob.Blob{}, errCopyFault
	}
	return b.BlobIndex.Unref(ctx, sha, n)
}

func TestUnitVirtualCopyFaultsKeepBlobReferencesAtomic(t *testing.T) {
	for _, fault := range []string{"lock", "read", "write", "ref", "unref", "list", "delete"} {
		t.Run(fault, func(t *testing.T) {
			s := inmem.New(memtx.New())
			ctx := t.Context()
			if err := s.Repos().Create(ctx, repo.Config{Key: "v", Class: repo.Virtual}); err != nil {
				t.Fatal(err)
			}
			old := virtual.Copy{
				Virtual:      "v",
				Path:         "a.pom",
				Member:       "local",
				SourceSHA256: strings.Repeat("1", 64),
				Item:         model.Item{Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 10},
			}
			if _, err := s.Blobs().Ref(ctx, blob.Blob{Checksums: old.Item.Actual, Size: 10}, 1); err != nil {
				t.Fatal(err)
			}
			c := old
			c.Item.Actual.SHA256 = strings.Repeat("b", 64)
			copies := storekit.NewVirtualCopies(copyFaultStore{Store: s, fault: fault}, copyRows{old: old, fault: fault})
			var err error
			if fault == "list" || fault == "delete" {
				err = copies.DeleteRepo(ctx, "v")
			} else {
				err = copies.Put(ctx, c)
			}
			if !errors.Is(err, errCopyFault) {
				t.Fatalf("%s %v", fault, err)
			}
			b, err := s.Blobs().Get(ctx, old.Item.Actual.SHA256)
			if err != nil || b.Refs != 1 {
				t.Fatalf("old reference lost: %+v %v", b, err)
			}
			if b, err = s.Blobs().Get(ctx, c.Item.Actual.SHA256); err == nil || !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("failed write left reference: %+v %v", b, err)
			}
		})
	}
}
