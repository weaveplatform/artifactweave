package remote_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func executionPublication() *source.Publication {
	return &source.Publication{
		Check: func(context.Context) error { return nil },
		Fence: func(context.Context) error { return nil },
	}
}

func TestAccSelectedPublicationFinalFenceRollsBackTheCacheItemAndRawState(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/selected.bin", fakeupstream.Resource{Body: []byte("verified")})
	q := w.request("selected.bin", source.ResourceKindArtifact)
	q.Resource.SelectedContent, q.Publication = selectedContent("verified"), executionPublication()
	properties := model.Properties{"quality": {"verified"}}
	q.SelectedProperties = &properties
	fences := 0
	q.Publication.Fence = func(ctx context.Context) error {
		fences++
		if !w.db.InTx(ctx) {
			t.Error("worker fence ran outside publication transaction")
		}
		item, err := w.db.Items().Get(ctx, q.Repo.Key, true, q.Resource.Path)
		if err != nil || item.Actual.SHA256 != q.Resource.SelectedContent.Checksums.SHA256 {
			t.Errorf("worker fence preceded complete item publication: %v", err)
		}
		entry, err := w.db.RemoteCache().Read(ctx, w.p.CacheKey(q, "credential-1"))
		if err != nil || entry.ItemID != item.ID {
			t.Errorf("worker fence preceded raw-cache publication: %v", err)
		}
		props, err := w.db.Items().Properties(ctx, item.ID)
		if err != nil || strings.Join(props["quality"], ",") != "verified" {
			t.Errorf("worker fence preceded selected property publication: %v", err)
		}
		return source.ErrSuperseded
	}
	result, err := w.read(q)
	if !errors.Is(err, source.ErrSuperseded) || result.Item != nil || result.Stream != nil || fences != 1 {
		t.Fatalf("expired owner published content: %v fences=%d", err, fences)
	}
	if _, err = w.db.Items().Get(t.Context(), q.Repo.Key, true, q.Resource.Path); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("expired worker left a cache item", err)
	}
	entry, err := w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
	if err == nil && (entry.ItemID != 0 || entry.Blob.Checksums.SHA256 != "") {
		t.Fatal("expired worker left raw cache state")
	}
	// Ordinary demand fetch still works, with the original cache identity.
	q.Publication, q.Resource.SelectedContent = nil, nil
	q.SelectedProperties = nil
	w.mustRead(q, "verified")
}

func TestAccSelectedPublicationCurrentConfigurationFencesABlockedFetch(t *testing.T) {
	w := policyFixture(t)
	barrier := new(fakeupstream.Barrier)
	w.up.Set("/selected.bin", fakeupstream.Resource{Body: []byte("verified"), Fault: fakeupstream.Fault{BeforeBody: barrier}})
	q := w.request("selected.bin", source.ResourceKindArtifact)
	q.Resource.SelectedContent, q.Publication = selectedContent("verified"), executionPublication()
	current := make(chan struct{})
	q.Publication.Check = func(context.Context) error {
		select {
		case <-current:
			return source.ErrSuperseded
		default:
			return nil
		}
	}
	done := make(chan error, 1)
	go func() { _, err := w.read(q); done <- err }()
	<-barrier.Reached()
	close(current)
	barrier.Release()
	if err := <-done; !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("blocked transfer published under superseded execution configuration", err)
	}
	if _, err := w.db.Items().Get(t.Context(), q.Repo.Key, true, q.Resource.Path); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("superseded configuration left cache content", err)
	}
}

func TestAccSelectedPublicationFinalFenceProtects304AndNegativeWrites(t *testing.T) {
	for _, mode := range []string{"304", "negative"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := w.request("selected.bin", source.ResourceKindArtifact)
			q.Resource.Mutable = true
			var before model.RemoteCacheEntry
			if mode == "304" {
				w.up.Set("/selected.bin", fakeupstream.Resource{Body: []byte("verified")})
				w.mustRead(q, "verified")
				var err error
				before, err = w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
				if err != nil {
					t.Fatal(err)
				}
				w.clock.Advance(time.Minute)
			}
			q.Resource.SelectedContent, q.Publication = selectedContent("verified"), executionPublication()
			fences := 0
			q.Publication.Fence = func(ctx context.Context) error {
				fences++
				if !w.db.InTx(ctx) {
					t.Error("cache refresh fence did not join its transaction")
				}
				return source.ErrSuperseded
			}
			result, err := w.read(q)
			if !errors.Is(err, source.ErrSuperseded) || result.Item != nil || result.Stream != nil || fences != 1 {
				t.Fatalf("expired owner changed %s cache state: %v fences=%d", mode, err, fences)
			}
			after, err := w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
			if mode == "304" && (err != nil || !after.ValidatedAt.Equal(before.ValidatedAt) || after.Blob != before.Blob) {
				t.Fatal("failed ownership fence refreshed a former entry", err)
			}
			if mode == "negative" && err == nil && !after.NegativeUntil.IsZero() {
				t.Fatal("failed ownership fence installed a negative entry")
			}
		})
	}
}
