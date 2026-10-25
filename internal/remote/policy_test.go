package remote

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type policyWorld struct {
	t      *testing.T
	db     *inmem.Store
	blobs  blob.Store
	clock  *fakeupstream.Clock
	up     *fakeupstream.Server
	p      *Policy
	c      repo.Config
	permit source.Permit
}

func policyFixture(t *testing.T) *policyWorld {
	t.Helper()
	clock := fakeupstream.NewClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	up := fakeupstream.New(t, fakeupstream.Options{Now: clock.Now})
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: up.URL})
	c.RetrievalCachePeriodSecs = ptr(30)
	c.MissedRetrievalCachePeriodSecs = ptr(5)
	if err = db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	identity, err := db.RemoteCache().Source(t.Context(), c.Key)
	if err != nil {
		t.Fatal(err)
	}
	p := &Policy{
		Store: db,
		Blobs: blobs,
		Items: &item.Service{Store: db, Blobs: blobs, Now: clock.Now},
		Pool:  new(Pool),
		Now:   clock.Now,
		Key: []byte(
			"test-only-query-identity",
		),
		Options: func(context.Context, repo.Config) (Options, error) { return Options{Revision: "credential-1"}, nil },
	}
	t.Cleanup(p.Pool.Close)
	return &policyWorld{
		t:     t,
		db:    db,
		blobs: blobs,
		clock: clock,
		up:    up,
		p:     p,
		c:     c,
		permit: source.Permit{
			Identity:               identity,
			Intent:                 source.MayFetch,
			UpstreamNetworkAllowed: true,
		},
	}
}

func (w *policyWorld) request(path string, kind source.ResourceKind) source.Request {
	return source.Request{
		Repo:     w.c,
		Resource: source.Resource{Kind: kind, Path: path, Method: "GET"},
	}
}

func (w *policyWorld) read(q source.Request) (source.Result, error) {
	return w.p.Read(w.t.Context(), q, w.permit)
}

func (w *policyWorld) bytes(result source.Result) string {
	w.t.Helper()
	var r io.ReadCloser
	if result.Stream != nil {
		r = result.Stream.Body
	} else {
		var err error
		r, _, err = w.blobs.Open(w.t.Context(), result.Item.Actual.SHA256)
		if err != nil {
			w.t.Fatal(err)
		}
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *policyWorld) mustRead(q source.Request, want string) source.Result {
	w.t.Helper()
	r, err := w.read(q)
	if err != nil {
		w.t.Fatal(err)
	}
	if got := w.bytes(r); got != want {
		w.t.Fatalf("got %q want %q", got, want)
	}
	return r
}

func TestAccPolicyUniqueCacheOnlyOfflineAndNoStore(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("original")})
	q := w.request("file.txt", source.Artifact)
	w.mustRead(q, "original") // R1
	w.clock.Advance(24 * time.Hour)
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("changed")})
	w.mustRead(q, "original") // R2
	if w.up.Count("", "") != 2 {
		t.Fatal(w.up.Requests())
	}
	cachePermit := w.permit
	cachePermit.Intent = source.CacheOnly
	cachePermit.UpstreamNetworkAllowed = false
	r, err := w.p.Read(t.Context(), q, cachePermit)
	if err != nil || w.bytes(r) != "original" {
		t.Fatal(err)
	} // R8
	missing := w.request("missing", source.Artifact)
	if _, err = w.p.Read(t.Context(), missing, cachePermit); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	q.Repo.Offline = ptr(true)
	q.Repo.HardFail = ptr(true)
	w.mustRead(q, "original") // R9
	q.Repo.Offline = ptr(false)
	q.Repo.StoreArtifactsLocally = ptr(false)
	w.mustRead(q, "changed")
	w.mustRead(q, "changed") // R13
	if w.up.Count("GET", "/file.txt") != 3 {
		t.Fatal(w.up.Requests())
	}
	kept, err := w.db.Items().Get(t.Context(), w.c.Key, true, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := w.blobs.Open(t.Context(), kept.Actual.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(b)
	b.Close()
	if string(content) != "original" {
		t.Fatal("no-store replaced prior cache")
	}
}

func TestAccPolicyMetadataRevalidationAndNegativeExpiry(t *testing.T) {
	w := policyFixture(t)
	w.up.Set(
		"/index.json",
		fakeupstream.Resource{
			Body:   []byte(`{"version":1}`),
			ETag:   `"v1"`,
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	q := w.request("index.json", source.Metadata)
	w.mustRead(q, `{"version":1}`) // R3
	w.mustRead(q, `{"version":1}`) // R4
	if w.up.Count("", "") != 2 {
		t.Fatal(w.up.Requests())
	}
	w.clock.Advance(30 * time.Second)
	w.mustRead(q, `{"version":1}`) // R5, HEAD304
	if w.up.Count("HEAD", "") != 2 || w.up.Count("GET", "") != 1 {
		t.Fatal(w.up.Requests())
	}
	w.up.Set(
		"/index.json",
		fakeupstream.Resource{
			Body:   []byte(`{"version":2}`),
			ETag:   `"v2"`,
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	w.clock.Advance(30 * time.Second)
	w.mustRead(q, `{"version":2}`)
	missing := w.request("new.txt", source.Artifact)
	if _, err := w.read(missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	w.up.Set("/new.txt", fakeupstream.Resource{Body: []byte("arrived")})
	if _, err := w.read(missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	} // R7
	if w.up.Count("HEAD", "/new.txt") != 1 {
		t.Fatal(w.up.Requests())
	}
	w.clock.Advance(5 * time.Second)
	w.mustRead(missing, "arrived")
	if w.up.Count("HEAD", "/new.txt") != 2 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccPolicyFailureClassesPreserveGoodMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		hard   bool
		want   int
	}{
		{"temporary stale", 503, false, 200},
		{"temporary hard", 503, true, 503},
		{"auth denied", 401, false, 401},
		{"forbidden", 403, false, 403},
		{"throttled stale", 429, false, 200},
		{"throttled hard", 429, true, 429},
		{"confirmed deleted", 404, false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good"), ETag: `"old"`})
			q := w.request("meta.txt", source.Metadata)
			q.Repo.HardFail = ptr(tc.hard)
			w.mustRead(q, "good")
			w.clock.Advance(31 * time.Second)
			w.up.Set(
				"/meta.txt",
				fakeupstream.Resource{Status: tc.status, Body: []byte("secret upstream error")},
			)
			r, err := w.read(q)
			if tc.want == 200 {
				if err != nil || w.bytes(r) != "good" {
					t.Fatal(err)
				}
			} else if tc.want == 404 {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatal(err)
				}
			} else {
				var f *source.HTTPError
				if !errors.As(err, &f) || f.Status != tc.want ||
					strings.Contains(err.Error(), "secret") {
					t.Fatal(err)
				}
			}
			entry, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
			if err != nil {
				t.Fatal(err)
			}
			if !entry.ValidatedAt.Equal(w.clock.Now().Add(-31 * time.Second)) {
				t.Fatal("failure refreshed clock")
			}
		})
	}
}

func TestAccPolicyConcurrentMissCancellationAndIsolation(t *testing.T) {
	w := policyFixture(t)
	barrier := new(fakeupstream.Barrier)
	w.up.Set(
		"/file.txt",
		fakeupstream.Resource{
			Body:  []byte("complete"),
			Fault: fakeupstream.Fault{BeforeBody: barrier},
		},
	)
	q := w.request("file.txt", source.Artifact)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := w.p.Read(ctx, q, w.permit); first <- err }()
	<-barrier.Reached()
	second := make(chan error, 1)
	go func() { _, err := w.read(q); second <- err }()
	key := w.p.flightKey(w.p.cacheKey(q, "credential-1"), w.permit.Identity)
	waitFor(t, func() bool {
		w.p.flights.mu.Lock()
		defer w.p.flights.mu.Unlock()
		return w.p.flights.active[key] != nil && w.p.flights.active[key].waiters == 2
	})
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	barrier.Release()
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if w.up.Count("HEAD", "") != 1 || w.up.Count("GET", "") != 1 {
		t.Fatal(w.up.Requests())
	} // R14
	w.p.Options = func(context.Context, repo.Config) (Options, error) { return Options{Revision: "credential-2"}, nil }
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("new credential content")})
	w.mustRead(q, "new credential content")
	if w.up.Count("GET", "") != 2 {
		t.Fatal("credential revisions shared cache")
	}
}

func TestAccPolicyNoStoreLeavesNoEntriesAndHTTPNoStoreIsNotShared(t *testing.T) {
	w := policyFixture(t)
	w.up.Set(
		"/private.txt",
		fakeupstream.Resource{
			Body:   []byte("private"),
			Header: http.Header{"Cache-Control": {"no-store"}},
		},
	)
	q := w.request("private.txt", source.Artifact)
	q.Repo.StoreArtifactsLocally = ptr(false)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			r, err := w.read(q)
			if err != nil {
				t.Error(err)
				return
			}
			if w.bytes(r) != "private" {
				t.Error("body")
			}
		})
	}
	wg.Wait()
	if w.up.Count("GET", "") != 3 {
		t.Fatal(w.up.Requests())
	}
	if _, err := w.db.Items().
		Get(t.Context(), w.c.Key, true, "private.txt"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
	if _, err := w.db.RemoteCache().
		Read(t.Context(), w.p.cacheKey(q, "credential-1")); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("no-store cache record", err)
	}
	q.Repo.StoreArtifactsLocally = ptr(true)
	w.mustRead(q, "private")
	if _, err := w.db.Items().
		Get(t.Context(), w.c.Key, true, "private.txt"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
}

func TestAccPolicyAdapterTargetsAndPreparation(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/actual/file.txt", fakeupstream.Resource{Body: []byte("first")})
	w.up.Set("/different/file.txt", fakeupstream.Resource{Body: []byte("second")})
	q := w.request("canonical/file.txt", source.Artifact)
	q.Resource.UpstreamPath = "actual/file.txt"
	prepared := 0
	w.p.PrepareUpload = func(ctx context.Context, got source.Request, _ source.Permit, _ Options, u *item.CacheUpload) error {
		prepared++
		outside := false
		w.db.AfterCommit(ctx, func() { outside = true })
		if !outside || got.Resource.Path != "canonical/file.txt" ||
			u.Token.Key.Path != got.Resource.Path {
			t.Fatal("lost canonical path or preparation in SQL")
		}
		return nil
	}
	result := w.mustRead(q, "first")
	if result.Item.FullPath() != q.Resource.Path || prepared != 1 {
		t.Fatal(result, prepared)
	}
	w.mustRead(q, "first")
	if prepared != 1 {
		t.Fatal("warm lookup prepared upstream")
	}
	q.Resource.UpstreamPath = "different/file.txt"
	w.mustRead(q, "second")
	if prepared != 2 {
		t.Fatal("target identity reused previous bytes")
	}
	oldKey := w.p.cacheKey(q, "credential-1")
	q.Resource.Checksums.SHA256 = strings.Repeat("0", 64)
	if w.p.cacheKey(q, "credential-1") == oldKey {
		t.Fatal("pin absent from identity")
	}
	if _, err := w.read(q); err == nil {
		t.Fatal("mismatching pin admitted")
	}
	q.Repo.Offline = ptr(true)
	if _, err := w.read(q); err == nil {
		t.Fatal("offline bytes violated metadata pin")
	}
	q.Resource.Checksums = result.Item.Actual
	if _, err := w.read(q); err == nil {
		t.Fatal("offline bytes reused replaced representation")
	}
	q.Resource.Checksums = types.Checksums{SHA256: "invalid"}
	if _, err := w.read(q); err == nil {
		t.Fatal("invalid offline pin accepted")
	}
	q.Resource.Checksums = types.Checksums{}
	w.mustRead(q, "second")
	q.Resource.ExternalURL = w.up.URL + "/other"
	q.Resource.UpstreamPath = ""
	if w.p.cacheKey(q, "credential-1") == oldKey {
		t.Fatal("URL absent from identity")
	}
	r := transportRequest(q, types.RemoteCacheEntry{})
	if r.ExternalURL != q.Resource.ExternalURL || r.Path != "" || r.Query != "" {
		t.Fatal(r)
	}
}

func TestAccPolicyAdapterPreparationFailureAndFence(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("body")})
	q := w.request("file.txt", source.Artifact)
	w.p.PrepareUpload = func(context.Context, source.Request, source.Permit, Options, *item.CacheUpload) error {
		return item.ErrBadChecksum
	}
	if _, err := w.read(q); err == nil {
		t.Fatal("preparation failure ignored")
	}
	if _, err := w.db.Items().
		Get(t.Context(), q.Repo.Key, true, q.Resource.Path); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("failure published", err)
	}
	w.p.PrepareUpload = func(context.Context, source.Request, source.Permit, Options, *item.CacheUpload) error {
		w.p.CheckCurrent = func(context.Context, store.Store, source.Request, source.Permit, string) error {
			return source.ErrDenied
		}
		return nil
	}
	if _, err := w.read(q); !errors.Is(err, source.ErrDenied) {
		t.Fatal("post-preparation authority fence missing", err)
	}
}

func TestUnitPolicyDefaultCacheIdentityIsStable(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.Artifact)
	q.Resource.Query, q.Resource.Accept = "ignored=true", "application/octet-stream"
	old, err := json.Marshal(
		[]any{
			q.Resource.Kind,
			q.Resource.Mutable,
			q.Resource.EscapedPath,
			q.Resource.Variant,
			q.Resource.Accept,
			"",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, w.p.Key)
	_, _ = mac.Write(old)
	if w.p.cacheKey(q, "credential").Variant != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("existing generic identity changed")
	}
}

func TestUnitPolicyMIMEAlternativesParticipateInIdentity(t *testing.T) {
	w := policyFixture(t)
	q := w.request("index", source.Metadata)
	before := w.p.cacheKey(q, "revision")
	q.Resource.ExpectedContentTypes = []string{"text/html", "application/json"}
	after := w.p.cacheKey(q, "revision")
	if before == after {
		t.Fatal("MIME contract omitted from identity")
	}
	q.Resource.ExpectedContentTypes = []string{"text/html"}
	if w.p.cacheKey(q, "revision") == after {
		t.Fatal("MIME alternatives not pinned")
	}
}
