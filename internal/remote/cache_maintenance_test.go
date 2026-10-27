package remote

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccCacheMaintenanceZapRefreshAndStalePolicy(t *testing.T) {
	for _, hard := range []bool{false, true} {
		name := "stale"
		if hard {
			name = "hard-fail"
		}
		t.Run(name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("immutable")})
			w.up.Set(
				"/index.json",
				fakeupstream.Resource{
					Body:   []byte(`{"version":1}`),
					ETag:   `"v1"`,
					Header: http.Header{"Content-Type": {"application/json"}},
				},
			)
			binary := w.request("file.bin", source.ResourceKindArtifact)
			metadata := w.request("index.json", source.ResourceKindMetadata)
			w.mustRead(binary, "immutable")
			w.mustRead(metadata, `{"version":1}`)
			w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("changed binary")})
			w.up.Set(
				"/index.json",
				fakeupstream.Resource{
					Body:   []byte(`{"version":2}`),
					ETag:   `"v2"`,
					Header: http.Header{"Content-Type": {"application/json"}},
				},
			)
			if _, err := w.p.Items.Zap(
				t.Context(),
				item.Target{Repo: w.c, Cached: true},
			); err != nil {
				t.Fatal(err)
			}
			before := w.up.Count("", "")
			cacheOnly := w.permit
			cacheOnly.Intent = source.RetrievalIntentCacheOnly
			cacheOnly.UpstreamNetworkAllowed = false
			cached, err := w.p.Read(t.Context(), metadata, cacheOnly)
			if err != nil || w.bytes(cached) != `{"version":1}` {
				t.Fatal("cache alias lost usable metadata", err)
			}
			if w.up.Count("", "") != before {
				t.Fatal("cache alias fetched")
			}
			w.mustRead(metadata, `{"version":2}`)
			w.mustRead(binary, "immutable")
			if w.up.Count("GET", "/file.bin") != 1 {
				t.Fatal("zap refetched immutable binary")
			}
			if _, err = w.p.Items.Zap(
				t.Context(),
				item.Target{Repo: w.c, Cached: true, Path: "index.json"},
			); err != nil {
				t.Fatal(err)
			}
			metadata.Repo.HardFail = ptr(hard)
			w.up.Set("/index.json", fakeupstream.Resource{Status: 503})
			result, err := w.read(metadata)
			if hard {
				if err == nil {
					if result.Stream != nil {
						_ = result.Stream.Body.Close()
					}
					t.Fatal("hardFail served stale metadata")
				}
			} else if err != nil || w.bytes(result) != `{"version":2}` {
				t.Fatal("temporary source failure lost stale metadata", err)
			}
			entry, err := w.db.RemoteCache().
				Read(t.Context(), w.p.cacheKey(metadata, "credential-1"))
			if err != nil || !entry.ValidatedAt.IsZero() {
				t.Fatal("failed refresh changed validation", entry, err)
			}
		})
	}
}

func TestAccCacheMaintenanceNegativeDeleteDiscoversUpstream(t *testing.T) {
	w := policyFixture(t)
	q := w.request("new.bin", source.ResourceKindArtifact)
	if _, err := w.read(q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	w.up.Set("/new.bin", fakeupstream.Resource{Body: []byte("newly available")})
	if _, err := w.read(q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("negative not effective", err)
	}
	if _, err := w.p.Items.Delete(
		t.Context(),
		item.Target{Repo: w.c, Cached: true, Path: "new.bin"},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	w.mustRead(q, "newly available")
	if w.up.Count("HEAD", "/new.bin") != 2 || w.up.Count("GET", "/new.bin") != 1 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccCacheMaintenanceFencesBlockedPublication(t *testing.T) {
	for _, operation := range []string{"zap-miss", "zap-refresh", "delete-miss", "delete-refresh", "recreate-refresh"} {
		t.Run(operation, func(t *testing.T) {
			w := policyFixture(t)
			q := w.request("index.txt", source.ResourceKindMetadata)
			refresh := operation != "zap-miss" && operation != "delete-miss"
			if refresh {
				w.up.Set(
					"/index.txt",
					fakeupstream.Resource{Body: []byte("usable old"), ETag: `"old"`},
				)
				w.mustRead(q, "usable old")
				w.clock.Advance(time.Minute)
			}
			gate := new(fakeupstream.Barrier)
			defer gate.Release()
			w.up.Set(
				"/index.txt",
				fakeupstream.Resource{
					Body:  []byte("late candidate"),
					ETag:  `"new"`,
					Fault: fakeupstream.Fault{BeforeBody: gate},
				},
			)
			done := make(chan error, 1)
			go func() {
				r, err := w.read(q)
				if r.Stream != nil {
					_ = r.Stream.Body.Close()
				}
				done <- err
			}()
			select {
			case <-gate.Reached():
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not block")
			}
			target := item.Target{Repo: w.c, Cached: true, Path: q.Resource.Path}
			var err error
			switch operation {
			case "zap-miss", "zap-refresh":
				_, err = w.p.Items.Zap(t.Context(), target)
			case "delete-miss", "delete-refresh":
				_, err = w.p.Items.Delete(t.Context(), target, "admin")
			case "recreate-refresh":
				err = w.db.Repos().Delete(t.Context(), w.c.Key)
				if err == nil {
					err = w.db.Repos().Create(t.Context(), w.c)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			gate.Release()
			if err = <-done; !errors.Is(err, source.ErrSuperseded) {
				t.Fatal("late publication not fenced", err)
			}
			entry, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
			if operation == "zap-refresh" {
				if err != nil || !entry.ValidatedAt.IsZero() {
					t.Fatal("zap lost usable expired metadata", err)
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Fatal("invalidated bytes restored", entry, err)
			}
		})
	}
}

func TestAccCacheMaintenanceRestoreFencesBlockedRefresh(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/snapshot.bin", fakeupstream.Resource{Body: []byte("retained snapshot")})
	q := w.request("snapshot.bin", source.ResourceKindArtifact)
	q.Resource.Mutable = true
	original := w.mustRead(q, "retained snapshot")
	entry, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
	if err != nil {
		t.Fatal(err)
	}
	// Restore takes over the reference retained by its historical trash record.
	if _, err = w.db.Blobs().Ref(t.Context(), entry.Blob, 1); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(time.Minute)
	gate := new(fakeupstream.Barrier)
	defer gate.Release()
	w.up.Set(
		"/snapshot.bin",
		fakeupstream.Resource{
			Body:  []byte("late snapshot"),
			Fault: fakeupstream.Fault{BeforeBody: gate},
		},
	)
	done := make(chan error, 1)
	go func() {
		r, err := w.read(q)
		if r.Stream != nil {
			_ = r.Stream.Body.Close()
		}
		done <- err
	}()
	select {
	case <-gate.Reached():
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach upstream")
	}
	restored, err := w.p.Items.Restore(
		t.Context(),
		item.Target{Repo: w.c, Cached: true, Path: q.Resource.Path},
		types.TrashRecord{Item: *original.Item},
		"admin",
	)
	if err != nil {
		t.Fatal(err)
	}
	gate.Release()
	if err = <-done; !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("restore did not fence refresh", err)
	}
	saved, err := w.db.Items().GetByID(t.Context(), restored.ID)
	if err != nil || saved.Actual != original.Item.Actual {
		t.Fatal("refresh overwrote restored content", err)
	}
	if _, err = w.db.RemoteCache().
		Read(t.Context(), entry.Token.Key); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("restore fabricated retrieval state", err)
	}
	ref, err := w.db.Blobs().Get(t.Context(), saved.Actual.SHA256)
	if err != nil || ref.Refs != 1 {
		t.Fatal("restore references inconsistent", ref, err)
	}
}
