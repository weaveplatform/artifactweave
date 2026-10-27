package remote

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccPolicyHEADRevalidatesWithoutGETAndPreservesStale(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good"), ETag: `"one"`})
	q := w.request("meta.txt", source.ResourceKindMetadata)
	w.mustRead(q, "good")
	w.clock.Advance(30 * time.Second)
	q.Resource.Method = http.MethodHead
	r, err := w.read(q)
	if err != nil || r.Stream == nil || r.Stream.Representation.Size != 4 {
		t.Fatal(err)
	}
	if w.up.Count("GET", "") != 1 || w.up.Count("HEAD", "") != 2 {
		t.Fatal(w.up.Requests())
	}
	e, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
	if err != nil || !e.ValidatedAt.Equal(w.clock.Now()) {
		t.Fatal(e, err)
	}
	w.clock.Advance(30 * time.Second)
	w.up.Set("/meta.txt", fakeupstream.Resource{Status: 503})
	r, err = w.read(q)
	if err != nil || r.Stream == nil || r.Stream.Representation.Size != 4 {
		t.Fatal(r, err)
	}
	_ = r.Stream.Body.Close()
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("new"), ETag: `"two"`})
	r, err = w.read(q)
	if err != nil || r.Stream == nil || r.Stream.Representation.ETag != `"two"` ||
		r.Stream.Representation.Size != 3 {
		t.Fatal(r, err)
	}
	_ = r.Stream.Body.Close()
	q.Resource.Method = http.MethodGet
	w.mustRead(q, "new")
	missing := w.request("absent", source.ResourceKindArtifact)
	missing.Resource.Method = http.MethodHead
	if _, err = w.read(missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = w.read(missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if w.up.Count("HEAD", "/absent") != 1 {
		t.Fatal("HEAD negative was not cached")
	}
}

func TestAccPolicyRawMetadataOfflineUsesNoResolverAndTracksUsage(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("raw upstream")})
	q := w.request("meta.txt", source.ResourceKindVersions)
	r := w.mustRead(q, "raw upstream")
	if r.Used == nil {
		t.Fatal("missing successful-use hook")
	}
	key := w.p.cacheKey(q, "credential-1")
	e, err := w.db.RemoteCache().Read(t.Context(), key)
	if err != nil || !e.LastUsed.Equal(w.clock.Now()) {
		t.Fatal(e, err)
	}
	w.clock.Advance(time.Hour)
	if err = r.Used(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, err = w.db.RemoteCache().Read(t.Context(), key)
	if err != nil || !e.LastUsed.Equal(w.clock.Now()) {
		t.Fatal(e, err)
	}
	w.p.Options = func(context.Context, repo.Config) (Options, error) {
		t.Fatal("offline resolved transport")
		return Options{}, nil
	}
	q.Repo.Offline = ptr(true)
	q.Repo.HardFail = ptr(true)
	w.mustRead(q, "raw upstream")
	if w.up.Count("", "") != 2 {
		t.Fatal(w.up.Requests())
	}
	q.Resource.Path = "absent"
	if _, err = w.read(q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAccPolicyInvalidationAndSourceChangeFencePendingFetch(t *testing.T) {
	for _, operation := range []string{"zap", "delete", "recreate", "url", "offline"} {
		t.Run(operation, func(t *testing.T) {
			w := policyFixture(t)
			gate := new(fakeupstream.Barrier)
			w.up.Set(
				"/meta.txt",
				fakeupstream.Resource{
					Body:  []byte("complete"),
					Fault: fakeupstream.Fault{BeforeBody: gate},
				},
			)
			q := w.request("meta.txt", source.ResourceKindMetadata)
			done := make(chan error, 1)
			go func() { _, err := w.read(q); done <- err }()
			<-gate.Reached()
			var err error
			switch operation {
			case "zap", "delete":
				_, err = w.db.RemoteCache().
					Invalidate(t.Context(), w.c.Key, "meta.txt", operation == "zap")
			case "recreate":
				err = w.db.Repos().Delete(t.Context(), w.c.Key)
				if err == nil {
					err = w.db.Repos().Create(t.Context(), w.c)
				}
			case "url":
				c := w.c
				c.URL += "/new"
				err = w.db.Repos().Update(t.Context(), c)
			case "offline":
				c := w.c
				c.Offline = ptr(true)
				err = w.db.Repos().Update(t.Context(), c)
			}
			if err != nil {
				t.Fatal(err)
			}
			gate.Release()
			if err = <-done; !errors.Is(err, source.ErrSuperseded) {
				t.Fatal(err)
			}
			e, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
			if err == nil && e.Blob.Checksums.SHA256 != "" {
				t.Fatal("stale publication became visible")
			}
		})
	}
}

func TestAccPolicyIntegrityAndProtocolFailuresNeverServeStale(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource fakeupstream.Resource
		limit    int64
	}{
		{"checksum", fakeupstream.Resource{Body: []byte("bad"), Header: http.Header{"X-Checksum-Sha256": {strings.Repeat("0", 64)}}}, 0},
		{"html error", fakeupstream.Resource{Body: []byte("<html>upstream login</html>"), Header: http.Header{"Content-Type": {"text/html"}}}, 0},
		{"metadata limit", fakeupstream.Resource{Body: []byte("longer than limit")}, 4},
		{"partial", fakeupstream.Resource{Status: 206, Body: []byte("partial")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
			q := w.request("meta.txt", source.ResourceKindMetadata)
			w.mustRead(q, "good")
			w.clock.Advance(time.Hour)
			w.p.MetadataLimit = tc.limit
			w.up.Set("/meta.txt", tc.resource)
			_, err := w.read(q)
			var f *source.HTTPError
			if !errors.As(err, &f) || f.Status != 502 {
				t.Fatal(err)
			}
			state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
			if err != nil || state.ConnectionFailures != 0 {
				t.Fatal(state, err)
			}
			q.Repo.Offline = ptr(true)
			w.mustRead(q, "good")
		})
	}
}

func TestAccPolicyTruncationCircuitAndRecoveryAfterRestart(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Repo.BypassHeadRequests = ptr(true)
	q.Repo.AssumedOfflinePeriodSecs = ptr(5)
	w.up.Set(
		"/file.txt",
		fakeupstream.Resource{Body: []byte("abcdef"), Fault: fakeupstream.Fault{TruncateAfter: 2}},
	)
	for range 10 {
		if _, err := w.read(q); err == nil {
			t.Fatal("truncated body succeeded")
		}
	}
	state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil || state.ConnectionFailures != 10 ||
		!state.OfflineUntil.Equal(w.clock.Now().Add(5*time.Second)) {
		t.Fatal(state, err)
	}
	if w.up.Count("GET", "") != 10 {
		t.Fatal(w.up.Requests())
	}
	// A new policy instance shares only durable state, never its old flights.
	w.p = &Policy{
		Store:   w.db,
		Blobs:   w.blobs,
		Items:   w.p.Items,
		Pool:    w.p.Pool,
		Options: w.p.Options,
		Key:     w.p.Key,
		Now:     w.clock.Now,
	}
	if _, err = w.read(q); err == nil {
		t.Fatal("assumed offline empty cache succeeded")
	}
	if w.up.Count("GET", "") != 10 {
		t.Fatal("cooldown fetched")
	}
	w.clock.Advance(5 * time.Second)
	gate := new(fakeupstream.Barrier)
	w.up.Set(
		"/file.txt",
		fakeupstream.Resource{
			Body:  []byte("recovered"),
			Fault: fakeupstream.Fault{BeforeBody: gate},
		},
	)
	w.up.Set("/second.txt", fakeupstream.Resource{Body: []byte("second")})
	first := make(chan error, 1)
	go func() { _, err := w.read(q); first <- err }()
	<-gate.Reached()
	secondQ := q
	secondQ.Resource.Path = "second.txt"
	second := make(chan error, 1)
	go func() { _, err := w.read(secondQ); second <- err }()
	probeKey := w.p.probeKey(w.p.cacheKey(q, "credential-1"), w.permit, state)
	waitFor(t, func() bool {
		w.p.probes.mu.Lock()
		defer w.p.probes.mu.Unlock()
		f := w.p.probes.active[probeKey]
		return f != nil && f.waiters == 2
	})
	if w.up.Count("GET", "/second.txt") != 0 {
		t.Fatal("second path bypassed probe")
	}
	gate.Release()
	if err = <-first; err != nil {
		t.Fatal(err)
	}
	if err = <-second; err != nil {
		t.Fatal(err)
	}
	if w.up.Count("GET", "") != 12 {
		t.Fatal(w.up.Requests())
	}
	state, err = w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil || state.ConnectionFailures != 0 || !state.OfflineUntil.IsZero() {
		t.Fatal(state, err)
	}
}

func TestAccPolicyNoStoreFailureCooldownAndHEAD(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Repo.StoreArtifactsLocally = ptr(false)
	w.up.Set(
		"/file.txt",
		fakeupstream.Resource{Status: 429, Header: http.Header{"Retry-After": {"9999"}}},
	)
	for range 2 {
		_, err := w.read(q)
		var f *source.HTTPError
		if !errors.As(err, &f) || f.Status != 429 || f.RetryAfter != 300*time.Second {
			t.Fatal(err)
		}
	}
	if w.up.Count("HEAD", "") != 1 {
		t.Fatal("cooldown fetched")
	}
	w.clock.Advance(300 * time.Second)
	q.Resource.Method = http.MethodHead
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("complete"), ETag: `"one"`})
	r, err := w.read(q)
	if err != nil || r.Stream == nil || r.Stream.Representation.Size != 8 {
		t.Fatal(r, err)
	}
	_ = r.Stream.Body.Close()
	if w.up.Count("GET", "") != 0 {
		t.Fatal("HEAD fetched body")
	}
	q.Resource.Method = http.MethodGet
	w.mustRead(q, "complete")
	if _, err = w.db.RemoteCache().
		Read(t.Context(), w.p.cacheKey(q, "credential-1")); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("no-store persisted representation", err)
	}
}

func TestAccPolicyMutableAndDerivedClassification(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Resource.Mutable = true
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("one")})
	w.mustRead(q, "one")
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("two")})
	w.mustRead(q, "one")
	w.clock.Advance(30 * time.Second)
	w.mustRead(q, "two")
	e, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
	if err != nil || e.Kind != types.RemoteMutable {
		t.Fatal(e, err)
	}
	q.Repo.PackageType = repo.Maven
	q.Resource.Path = "maven-metadata.xml"
	q.Resource.Mutable = false
	if w.p.kind(q) != types.RemoteDerived {
		t.Fatal("format classification not applied")
	}
}

func TestAccPolicyDerivedNeverFetches(t *testing.T) {
	w := policyFixture(t)
	q := w.request("maven-metadata.xml", source.ResourceKindArtifact)
	q.Repo.PackageType = repo.Maven
	if _, err := w.read(q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if w.up.Count("", "") != 0 {
		t.Fatal("derived index fetched upstream")
	}
}

func TestAccPolicyVariantReplacementRefetchesAndNeverReturnsWrongBytes(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Repo.PropagateQueryParams = ptr(true)
	q.Resource.Query = "variant=A"
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("A")})
	w.mustRead(q, "A")
	q.Resource.Query = "variant=B"
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("B")})
	w.mustRead(q, "B")
	q.Resource.Query = "variant=A"
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("A")})
	w.mustRead(q, "A")
	if w.up.Count("GET", "") != 3 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccPolicyRecoveryHEADWaiterHonorsRenewedCooldown(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	q.Repo.BypassHeadRequests = ptr(true)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
	w.mustRead(q, "good")
	w.clock.Advance(time.Hour)
	state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	state.ConnectionFailures = 10
	state.OfflineUntil = w.clock.Now()
	state.SettingsRevision = "credential-1"
	if err = w.db.RemoteCache().PutState(t.Context(), w.c.Key, state); err != nil {
		t.Fatal(err)
	}
	w.p.Pool.Remove(w.c.Key)
	gate := new(fakeupstream.Barrier)
	w.up.Set(
		"/meta.txt",
		fakeupstream.Resource{Fault: fakeupstream.Fault{BeforeHeaders: gate, DropConnection: true}},
	)
	first := make(chan error, 1)
	go func() {
		r, e := w.read(q)
		if e == nil {
			_ = r.Stream.Body.Close()
		}
		first <- e
	}()
	<-gate.Reached()
	head := q
	head.Resource.Method = http.MethodHead
	second := make(chan error, 1)
	go func() {
		r, e := w.read(head)
		if e == nil {
			_ = r.Stream.Body.Close()
		}
		second <- e
	}()
	key := w.p.probeKey(w.p.cacheKey(q, "credential-1"), w.permit, state)
	waitFor(t, func() bool {
		w.p.probes.mu.Lock()
		defer w.p.probes.mu.Unlock()
		f := w.p.probes.active[key]
		return f != nil && f.waiters == 2
	})
	gate.Release()
	if err = <-first; err != nil {
		t.Fatal(err)
	}
	if err = <-second; err != nil {
		t.Fatal(err)
	}
	if w.up.Count("HEAD", "") != 0 || w.up.Count("GET", "") != 2 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccPolicyDisableCircuitAndCredentialRotationRecoverImmediately(t *testing.T) {
	for _, mode := range []string{"disabled", "rotation"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := w.request("file.txt", source.ResourceKindArtifact)
			q.Repo.BypassHeadRequests = ptr(true)
			state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
			if err != nil {
				t.Fatal(err)
			}
			state.ConnectionFailures = 10
			state.OfflineUntil = w.clock.Now().Add(time.Hour)
			state.SettingsRevision = "credential-1"
			if err = w.db.RemoteCache().PutState(t.Context(), w.c.Key, state); err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" {
				q.Repo.AssumedOfflinePeriodSecs = ptr(0)
			} else {
				w.p.Options = func(context.Context, repo.Config) (Options, error) { return Options{Revision: "credential-2"}, nil }
			}
			w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("healthy")})
			w.mustRead(q, "healthy")
			if w.up.Count("GET", "") != 1 {
				t.Fatal(w.up.Requests())
			}
		})
	}
}

func TestAccPolicyNoStoreRecoveryAndTruncationNeverReturnsSuccess(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Repo.StoreArtifactsLocally = ptr(false)
	q.Repo.BypassHeadRequests = ptr(true)
	w.up.Set(
		"/file.txt",
		fakeupstream.Resource{Body: []byte("abcdef"), Fault: fakeupstream.Fault{TruncateAfter: 2}},
	)
	if _, err := w.read(q); err == nil {
		t.Fatal("partial no-store succeeded")
	}
	state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil || state.ConnectionFailures != 1 {
		t.Fatal(state, err)
	}
	state.ConnectionFailures = 10
	state.OfflineUntil = w.clock.Now()
	state.SettingsRevision = "credential-1"
	if err = w.db.RemoteCache().PutState(t.Context(), w.c.Key, state); err != nil {
		t.Fatal(err)
	}
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("healthy")})
	r := w.mustRead(q, "healthy")
	if r.Used != nil {
		t.Fatal("no-store advanced cache usage")
	}
	// One fully validated recovery probe, then the caller's owned body.
	if w.up.Count("GET", "") != 3 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccPolicySecurityTimeoutAndZeroNegativeTTL(t *testing.T) {
	t.Run("security", func(t *testing.T) {
		w := policyFixture(t)
		q := w.request("meta.txt", source.ResourceKindMetadata)
		w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
		w.mustRead(q, "good")
		w.clock.Advance(time.Hour)
		w.p.Options = func(context.Context, repo.Config) (Options, error) { return Options{}, ErrSecurity }
		_, err := w.read(q)
		var f *source.HTTPError
		if !errors.As(err, &f) || f.Kind != source.FailureKindSecurity {
			t.Fatal(err)
		}
		if w.up.Count("", "") != 2 {
			t.Fatal("security failure fetched")
		}
	})
	t.Run("timeout stale", func(t *testing.T) {
		w := policyFixture(t)
		q := w.request("meta.txt", source.ResourceKindMetadata)
		q.Repo.SocketTimeoutMillis = ptr(40)
		w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
		w.mustRead(q, "good")
		w.clock.Advance(time.Hour)
		gate := new(fakeupstream.Barrier)
		defer gate.Release()
		w.up.Set("/meta.txt", fakeupstream.Resource{Fault: fakeupstream.Fault{BeforeHeaders: gate}})
		w.mustRead(q, "good")
		q.Repo.HardFail = ptr(true)
		_, err := w.read(q)
		var f *source.HTTPError
		if !errors.As(err, &f) || f.Kind != source.FailureKindTimeout {
			t.Fatal(err)
		}
	})
	t.Run("zero missing TTL", func(t *testing.T) {
		w := policyFixture(t)
		q := w.request("file.txt", source.ResourceKindArtifact)
		q.Repo.MissedRetrievalCachePeriodSecs = ptr(0)
		for range 2 {
			if _, err := w.read(q); !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
		}
		if w.up.Count("HEAD", "") != 2 {
			t.Fatal("zero TTL reused miss")
		}
	})
}

func TestAccPolicySharedFallbackHonorsEachCallersHardFail(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
	w.mustRead(q, "good")
	w.clock.Advance(time.Hour)
	gate := new(fakeupstream.Barrier)
	w.up.Set(
		"/meta.txt",
		fakeupstream.Resource{Status: 503, Fault: fakeupstream.Fault{BeforeHeaders: gate}},
	)
	first := make(chan error, 1)
	go func() {
		r, e := w.read(q)
		if e == nil {
			_ = r.Stream.Body.Close()
		}
		first <- e
	}()
	<-gate.Reached()
	strict := q
	strict.Repo.HardFail = ptr(true)
	second := make(chan error, 1)
	go func() { _, e := w.read(strict); second <- e }()
	key := w.p.flightKey(w.p.cacheKey(q, "credential-1"), w.permit.Identity)
	waitFor(t, func() bool {
		w.p.flights.mu.Lock()
		defer w.p.flights.mu.Unlock()
		f := w.p.flights.active[key]
		return f != nil && f.waiters == 2
	})
	gate.Release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	var f *source.HTTPError
	if err := <-second; !errors.As(err, &f) || f.Status != 503 {
		t.Fatal(err)
	}
	if w.up.Count("HEAD", "") != 2 {
		t.Fatal("waiters did not share refresh")
	}
}

func TestAccPolicyNegativeTTLChangeAndConfirmedDeletionRecheck(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	if _, err := w.read(q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("available")})
	q.Repo.MissedRetrievalCachePeriodSecs = ptr(0)
	w.mustRead(q, "available")
	// An expired 404 attached to a former positive must revalidate, even though
	// the positive was originally an immutable artifact.
	key := w.p.cacheKey(q, "credential-1")
	token, err := w.db.RemoteCache().Begin(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.RemoteCache().Negative(t.Context(), token, w.clock.Now()); err != nil {
		t.Fatal(err)
	}
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("returned")})
	w.mustRead(q, "returned")
	if w.up.Count("GET", "") != 2 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccPolicy304ChecksValidatorsAndUpdatesCacheRules(t *testing.T) {
	for _, mode := range []string{"mismatch", "no-cache", "vary"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := w.request("meta.txt", source.ResourceKindMetadata)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good"), ETag: `"one"`})
			w.mustRead(q, "good")
			w.clock.Advance(time.Hour)
			h := make(http.Header)
			h.Set("ETag", `"one"`)
			if mode == "mismatch" {
				h.Set("ETag", `"wrong"`)
			} else if mode == "vary" {
				h.Set("Vary", "Cookie")
			} else {
				h.Set("Cache-Control", "no-cache")
			}
			w.up.Set(
				"/meta.txt",
				fakeupstream.Resource{Status: 304, Header: h, ETag: h.Get("ETag")},
			)
			r, err := w.read(q)
			if mode == "mismatch" {
				var f *source.HTTPError
				if !errors.As(err, &f) || f.Kind != source.FailureKindProtocol {
					t.Fatal(err)
				}
				return
			}
			if err != nil || w.bytes(r) != "good" {
				t.Fatal(err)
			}
			e, err := w.db.RemoteCache().Read(t.Context(), w.p.cacheKey(q, "credential-1"))
			if err != nil || w.p.fresh(q, e) {
				t.Fatal("304 lost restrictive cache rules", e, err)
			}
		})
	}
}

func TestAccPolicyHTTPNoStoreProbeValidatesBeforeClearingOutage(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	q.Repo.BypassHeadRequests = ptr(true)
	state, err := w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	state.ConnectionFailures = 10
	state.OfflineUntil = w.clock.Now()
	state.SettingsRevision = "credential-1"
	if err = w.db.RemoteCache().PutState(t.Context(), w.c.Key, state); err != nil {
		t.Fatal(err)
	}
	w.up.Set(
		"/meta.txt",
		fakeupstream.Resource{
			Body:   []byte("abcdef"),
			Header: http.Header{"Cache-Control": {"no-store"}},
			Fault:  fakeupstream.Fault{TruncateAfter: 2},
		},
	)
	if _, err = w.read(q); err == nil {
		t.Fatal("truncated transient probe succeeded")
	}
	state, err = w.db.RemoteCache().State(t.Context(), w.c.Key)
	if err != nil || state.ConnectionFailures != 10 || !state.OfflineUntil.After(w.clock.Now()) {
		t.Fatal(state, err)
	}
	if w.up.Count("GET", "") != 1 {
		t.Fatal("failed transient probe retried")
	}
}

func TestAccPolicySuccessfulUsageDoesNotCancelRefresh(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("old")})
	old := w.mustRead(q, "old")
	w.clock.Advance(time.Hour)
	gate := new(fakeupstream.Barrier)
	w.up.Set(
		"/meta.txt",
		fakeupstream.Resource{Body: []byte("new"), Fault: fakeupstream.Fault{BeforeBody: gate}},
	)
	done := make(chan error, 1)
	go func() {
		r, e := w.read(q)
		if e == nil {
			_ = r.Stream.Body.Close()
		}
		done <- e
	}()
	<-gate.Reached()
	if err := old.Used(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w.mustRead(q, "new")
	if w.up.Count("GET", "") != 2 {
		t.Fatal(w.up.Requests())
	}
}
