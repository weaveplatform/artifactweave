package remote_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// missingFaults arms only after the origin's 404, so the fault reaches the
// retained-copy read or its clock transaction rather than the initial lookup.
type missingFaults struct {
	remote.Store
	origin, retaining bool
	phase             string
	fault             error
}

func (s *missingFaults) RemoteCache() port.RemoteCacheStore {
	return missingCache{RemoteCacheStore: s.Store.RemoteCache(), s: s}
}

func (s *missingFaults) Items() port.ItemStore {
	return missingItems{ItemStore: s.Store.Items(), s: s}
}

type missingCache struct {
	port.RemoteCacheStore
	s *missingFaults
}

func (c missingCache) Read(ctx context.Context, k model.RemoteCacheKey) (model.RemoteCacheEntry, error) {
	if c.s.origin && c.s.phase == "entry" {
		return model.RemoteCacheEntry{}, c.s.fault
	}
	e, err := c.RemoteCacheStore.Read(ctx, k)
	if c.s.origin {
		c.s.retaining = true
	}
	return e, err //nolint:wrapcheck // the adapter's fault under test
}

func (c missingCache) Put(
	ctx context.Context, token model.RemoteCacheToken, e model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	if c.s.retaining && c.s.phase == "put" {
		return model.RemoteCacheEntry{}, c.s.fault
	}
	return c.RemoteCacheStore.Put(ctx, token, e) //nolint:wrapcheck // the adapter's fault under test
}

type missingItems struct {
	port.ItemStore
	s *missingFaults
}

func (s missingItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if s.s.retaining && s.s.phase == "item" {
		return model.Item{}, s.s.fault
	}
	return s.ItemStore.GetByID(ctx, id) //nolint:wrapcheck // the adapter's fault under test
}

type missingBlob struct {
	blob.Store
	stat func(context.Context, string) error
}

func (b missingBlob) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := b.stat(ctx, sha); err != nil {
		return blob.Info{}, err
	}
	return b.Store.Stat(ctx, sha) //nolint:wrapcheck // the adapter's fault under test
}

func TestAccPolicyMissingOriginPreservesOwnStorageFaults(t *testing.T) {
	for _, readAlone := range []bool{false, true} {
		for _, phase := range []string{"entry", "item", "blob", "check", "put"} {
			for _, timeout := range []bool{false, true} {
				t.Run(fmt.Sprintf("read-only=%v/%s/timeout=%v", readAlone, phase, timeout), func(t *testing.T) {
					w := policyFixture(t)
					q := w.request("file.txt", source.ResourceKindArtifact)
					q.Resource.Mutable = true
					w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("old")})
					w.mustRead(q, "old")
					key := w.p.CacheKey(q, "credential-1")
					before, err := w.db.RemoteCache().Read(t.Context(), key)
					if err != nil {
						t.Fatal(err)
					}
					fault := errors.Join(kernel.ErrStore, errDropped)
					if timeout {
						fault = errors.Join(fault, context.DeadlineExceeded)
					}
					s := &missingFaults{Store: w.db, phase: phase, fault: fault}
					w.p.Store = s
					w.p.ProjectResponse = func(_ source.Request, res *http.Response) error {
						s.origin = res.StatusCode == http.StatusNotFound
						return nil
					}
					w.p.CheckCurrent = func(ctx context.Context, _ remote.Store, _ source.Request, _ source.Permit, _ string) error {
						if s.retaining && phase == "check" {
							return fault
						}
						return nil
					}
					w.p.Blobs = missingBlob{Store: w.blobs, stat: func(context.Context, string) error {
						if s.retaining && phase == "blob" {
							return fault
						}
						return nil
					}}
					w.clock.Advance(time.Minute)
					w.up.Set("/file.txt", fakeupstream.Resource{Status: http.StatusNotFound})
					if readAlone {
						q = readOnly(q)
					}
					_, err = w.read(q)
					wantStorageFault(t, err)
					if !errors.Is(err, errDropped) || !errors.Is(err, kernel.ErrStore) {
						t.Fatal("lost the original own-store cause", err)
					}
					after, err := w.db.RemoteCache().Read(t.Context(), key)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("failed retention changed cached content, clock or negative state", after, err)
					}
					if w.circuit() != 0 {
						t.Fatal("own-store failure entered the upstream circuit")
					}
				})
			}
		}
	}
}

func TestAccPolicyMissingOriginConcurrentEvictionWinsOverMissingBlob(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("old")})
	w.mustRead(q, "old")
	key := w.p.CacheKey(q, "credential-1")
	before, err := w.db.RemoteCache().Read(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	w.p.Blobs = missingBlob{Store: w.blobs, stat: func(ctx context.Context, sha string) error {
		if err = w.db.RemoteCache().Evict(ctx, before.Token); err != nil {
			return err
		}
		if err = w.blobs.Delete(ctx, sha); err != nil {
			return err
		}
		return kernel.ErrNotFound
	}}
	w.clock.Advance(time.Minute)
	w.up.Set("/meta.txt", fakeupstream.Resource{Status: http.StatusNotFound})
	_, err = w.read(q)
	if !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("concurrent cache eviction must supersede the retained copy", err)
	}
}

func TestAccPolicyMissingOriginRetainsStoreVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		fault, want error
	}{
		{"item removed", "item", kernel.ErrNotFound, kernel.ErrNotFound},
		{"item conflict", "item", kernel.ErrConflict, source.ErrSuperseded},
		{"blob removed under stable entry", "blob", blob.ErrNotFound, nil},
		{"current permission denied", "check", source.ErrDenied, source.ErrDenied},
		{"current credential superseded", "check", source.ErrSuperseded, source.ErrSuperseded},
		{"publication superseded", "put", kernel.ErrConflict, source.ErrSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := policyFixture(t)
			q := w.request("file.txt", source.ResourceKindArtifact)
			q.Resource.Mutable = true
			w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("old")})
			w.mustRead(q, "old")
			s := &missingFaults{Store: w.db, phase: tc.phase, fault: tc.fault}
			w.p.Store = s
			w.p.ProjectResponse = func(_ source.Request, res *http.Response) error {
				s.origin = res.StatusCode == http.StatusNotFound
				return nil
			}
			w.p.CheckCurrent = func(context.Context, remote.Store, source.Request, source.Permit, string) error {
				if s.retaining && tc.phase == "check" {
					return tc.fault
				}
				return nil
			}
			w.p.Blobs = missingBlob{Store: w.blobs, stat: func(context.Context, string) error {
				if s.retaining && tc.phase == "blob" {
					return tc.fault
				}
				return nil
			}}
			w.clock.Advance(time.Minute)
			w.up.Set("/file.txt", fakeupstream.Resource{Status: http.StatusNotFound})
			_, err := w.read(q)
			if tc.want == nil {
				wantStorageFault(t, err)
			} else if !errors.Is(err, tc.want) {
				t.Fatal("changed store verdict", err, tc.want)
			}
		})
	}
}
