package remote_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func checksumRejectionFixture(t *testing.T) (*policyWorld, source.Request, source.Result) {
	t.Helper()
	w := policyFixture(t)
	q := vanityRequest(w)
	q.Resource.ModuleVanityDocument = false
	q.Resource.ModuleChecksumDocument = true
	q.Resource.Variant = "checksum-rejection-test-v1"
	q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
		var control source.ModuleChecksumResponse
		if json.NewDecoder(r).Decode(&control) != nil || len(control.Content) == 0 {
			return source.ErrInvalidResult
		}
		return nil
	}
	w.up.Set("/module/vanity", fakeupstream.Resource{Body: []byte("original\n"), Header: http.Header{"Content-Type": {"text/plain"}}})
	result, err := w.read(q)
	if err != nil {
		t.Fatal(err)
	}
	if result.RejectMetadata == nil {
		t.Fatal("stored checksum result has no generation-scoped rejection")
	}
	_ = result.Stream.Body.Close()
	return w, q, result
}

func TestAccModuleChecksumRejectionClearsOnlyItsExactGeneration(t *testing.T) {
	for _, mode := range []string{"reject-current", "preserve-replacement", "cache-only", "offline"} {
		t.Run(mode, func(t *testing.T) {
			w, q, original := checksumRejectionFixture(t)
			if mode == "cache-only" || mode == "offline" {
				q.CacheOnly = mode == "cache-only"
				q.Repo.Offline = ptr(mode == "offline")
				result, err := w.read(q)
				if err != nil {
					t.Fatal(err)
				}
				_ = result.Stream.Body.Close()
				if result.RejectMetadata != nil {
					t.Fatal("non-network intent could reject shared checksum metadata")
				}
				return
			}
			if mode == "preserve-replacement" {
				w.up.Set("/module/vanity", fakeupstream.Resource{Body: []byte("replacement\n"), Header: http.Header{"Content-Type": {"text/plain"}}})
				q.Resource.RefreshMetadata = true
				result, err := w.read(q)
				if err != nil {
					t.Fatal(err)
				}
				_ = result.Stream.Body.Close()
				q.Resource.RefreshMetadata = false
			}
			calls := w.up.Count("GET", "/module/vanity")
			if err := original.RejectMetadata(t.Context()); err != nil {
				t.Fatal(err)
			}
			result, err := w.read(q)
			if err != nil {
				t.Fatal(err)
			}
			raw := w.bytes(result)
			if mode == "reject-current" {
				if w.up.Count("GET", "/module/vanity") != calls+1 {
					t.Fatal("rejected checksum generation was reused")
				}
			} else if w.up.Count("GET", "/module/vanity") != calls || !strings.Contains(raw, "cmVwbGFjZW1lbnQK") {
				t.Fatal("stale rejection removed a newer metadata replacement")
			}
		})
	}
}

type (
	checksumRejectionFaultStore struct {
		remote.Store
		cause error
	}
	checksumRejectionFaultCache struct {
		port.RemoteCacheStore
		cause error
	}
)

func (s checksumRejectionFaultStore) RemoteCache() port.RemoteCacheStore {
	return checksumRejectionFaultCache{s.Store.RemoteCache(), s.cause}
}

func (s checksumRejectionFaultCache) Evict(context.Context, model.RemoteCacheToken) error {
	return s.cause
}

func TestAccModuleChecksumRejectionRetainsStorageFaultIdentity(t *testing.T) {
	w, _, result := checksumRejectionFixture(t)
	cause := errors.Join(kernel.ErrStore, errors.New("fixture eviction storage failure"))
	w.p.Store = checksumRejectionFaultStore{Store: w.p.Store, cause: cause}
	if err := result.RejectMetadata(t.Context()); !errors.Is(err, cause) || !errors.Is(err, kernel.ErrStore) {
		t.Fatal("metadata rejection hid its storage fault", err)
	}
}
