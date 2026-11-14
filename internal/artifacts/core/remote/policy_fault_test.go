package remote_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestAccPolicyHEADRevalidatesWithoutGETAndPreservesStale: a HEAD of an
// expired entry whose upstream still answers its validator revalidates it
// without a GET, a HEAD that meets an upstream failure is answered from the
// stale entry, and a HEAD's miss is remembered, whether the HEAD fills the
// cache (#1391) or, for a checksum request, sends the upstream a HEAD only.
func TestAccPolicyHEADRevalidatesWithoutGETAndPreservesStale(t *testing.T) {
	for _, checksum := range []bool{false, true} {
		t.Run(fmt.Sprint("checksum request ", checksum), func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good"), ETag: `"one"`})
			q := w.request("meta.txt", source.ResourceKindMetadata)
			q.Resource.ChecksumRequest = checksum
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
			e, err := w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
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
			if err != nil || r.Stream == nil || r.Stream.Representation.ETag != "" ||
				r.Stream.Representation.Size != 3 {
				t.Fatal(r, err)
			}
			_ = r.Stream.Body.Close()
			q.Resource.Method = http.MethodGet
			w.mustRead(q, "new")
			missing := w.request("absent", source.ResourceKindArtifact)
			missing.Resource.Method = http.MethodHead
			missing.Resource.ChecksumRequest = checksum
			if _, err = w.read(missing); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal(err)
			}
			if _, err = w.read(missing); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal(err)
			}
			if w.up.Count("HEAD", "/absent") != 1 {
				t.Fatal("HEAD negative was not cached")
			}
		})
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
	key := w.p.CacheKey(q, "credential-1")
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
	w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
		t.Fatal("offline resolved transport")
		return remote.Options{}, nil
	}
	q.Repo.Offline = ptr(true)
	q.Repo.HardFail = ptr(true)
	w.mustRead(q, "raw upstream")
	if w.up.Count("", "") != 2 {
		t.Fatal(w.up.Requests())
	}
	q.Resource.Path = "absent"
	if _, err = w.read(q); !errors.Is(err, kernel.ErrNotFound) {
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
			e, err := w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
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
	w.p = &remote.Policy{
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
	probeKey := w.p.ProbeKey(w.p.CacheKey(q, "credential-1"), w.permit, state)
	waitFor(t, func() bool { return w.p.ProbeWaiters(probeKey) == 2 })
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
		Read(t.Context(), w.p.CacheKey(q, "credential-1")); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("no-store persisted representation", err)
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

// TestAccPolicyRecoveryHEADJoinsTheGETsFill: a HEAD that arrives while a
// GET's recovery probe runs on a remote that stores artifacts locally
// joins the GET's fill, as a HEAD downloads as a GET does (#1391), and is
// answered from the stale copy the failed probe falls back to, with no
// upstream request of its own.
func TestAccPolicyRecoveryHEADJoinsTheGETsFill(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	q.Repo.BypassHeadRequests = ptr(true)
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
	w.mustRead(q, "good")
	w.clock.Advance(time.Hour)
	recoveryState(t, w)
	gate, first, second := recoveryReads(w, q)
	key := w.p.FlightKey(w.p.CacheKey(q, "credential-1"), w.permit.Identity)
	waitFor(t, func() bool { return w.p.FlightWaiters(key) == 2 })
	gate.Release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if w.up.Count("HEAD", "") != 0 || w.up.Count("GET", "") != 2 {
		t.Fatal(w.up.Requests())
	}
}

// TestAccPolicyChecksumRecoveryHEADWaiterHonorsRenewedCooldown: a checksum
// request's HEAD, which sends the upstream a HEAD only, waits on a GET's
// recovery probe and, when the probe fails and renews the cooldown, is
// answered from the stale copy without an upstream request of its own.
func TestAccPolicyChecksumRecoveryHEADWaiterHonorsRenewedCooldown(t *testing.T) {
	w := policyFixture(t)
	q := w.request("meta.txt", source.ResourceKindMetadata)
	q.Repo.BypassHeadRequests = ptr(true)
	q.Resource.ChecksumRequest = true
	w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
	w.mustRead(q, "good")
	w.clock.Advance(time.Hour)
	state := recoveryState(t, w)
	gate, first, second := recoveryReads(w, q)
	key := w.p.ProbeKey(w.p.CacheKey(q, "credential-1"), w.permit, state)
	waitFor(t, func() bool { return w.p.ProbeWaiters(key) == 2 })
	gate.Release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if w.up.Count("HEAD", "") != 0 || w.up.Count("GET", "") != 2 {
		t.Fatal(w.up.Requests())
	}
}

// TestAccPolicyNoStoreRecoveryHEADWaiterHonorsRenewedCooldown: on a remote
// that stores nothing, a HEAD waits on a GET's recovery probe and, when the
// probe fails and renews the cooldown, answers its failure without an
// upstream request of its own.
func TestAccPolicyNoStoreRecoveryHEADWaiterHonorsRenewedCooldown(t *testing.T) {
	w := policyFixture(t)
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Repo.BypassHeadRequests = ptr(true)
	q.Repo.StoreArtifactsLocally = ptr(false)
	state := recoveryState(t, w)
	gate, first, second := recoveryReads(w, q)
	key := w.p.ProbeKey(w.p.CacheKey(q, "credential-1"), w.permit, state)
	waitFor(t, func() bool { return w.p.ProbeWaiters(key) == 2 })
	gate.Release()
	for _, done := range []chan error{first, second} {
		var failure *source.HTTPError
		if err := <-done; !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
			failure.Kind != source.FailureKindConnectivity {
			t.Fatalf("%v, want a 502 connectivity failure", err)
		}
	}
	if w.up.Count("HEAD", "") != 0 {
		t.Fatal(w.up.Requests())
	}
}

// recoveryState marks w's remote assumed offline until now, so the next
// read that needs the upstream runs the recovery probe, and returns the
// state it stored.
func recoveryState(t *testing.T, w *policyWorld) model.RemoteSourceState {
	t.Helper()
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
	return state
}

// recoveryReads has the upstream hold q's path until the returned gate
// opens and then drop the connection, starts a GET of q, and once the
// upstream holds it starts a HEAD of q. Their errors arrive on first and
// second.
func recoveryReads(
	w *policyWorld,
	q source.Request,
) (gate *fakeupstream.Barrier, first, second chan error) {
	w.pool.Remove(w.c.Key)
	gate = new(fakeupstream.Barrier)
	w.up.Set(
		"/"+q.Resource.Path,
		fakeupstream.Resource{Fault: fakeupstream.Fault{BeforeHeaders: gate, DropConnection: true}},
	)
	first, second = make(chan error, 1), make(chan error, 1)
	read := func(q source.Request, done chan error) {
		r, e := w.read(q)
		if e == nil && r.Stream != nil {
			_ = r.Stream.Body.Close()
		}
		done <- e
	}
	go read(q, first)
	<-gate.Reached()
	head := q
	head.Resource.Method = http.MethodHead
	go read(head, second)
	return gate, first, second
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
				w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
					return remote.Options{Revision: "credential-2"}, nil
				}
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
		w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
			return remote.Options{}, remote.ErrSecurity
		}
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
	t.Run("zero missing ttl", func(t *testing.T) {
		w := policyFixture(t)
		q := w.request("file.txt", source.ResourceKindArtifact)
		q.Repo.MissedRetrievalCachePeriodSecs = ptr(0)
		for range 2 {
			if _, err := w.read(q); !errors.Is(err, kernel.ErrNotFound) {
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
	key := w.p.FlightKey(w.p.CacheKey(q, "credential-1"), w.permit.Identity)
	waitFor(t, func() bool { return w.p.FlightWaiters(key) == 2 })
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
	if _, err := w.read(q); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("available")})
	q.Repo.MissedRetrievalCachePeriodSecs = ptr(0)
	w.mustRead(q, "available")
	// An expired 404 attached to a former positive must revalidate, even though
	// the positive was originally an immutable artifact.
	key := w.p.CacheKey(q, "credential-1")
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
			e, err := w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
			if err != nil || w.p.Fresh(q, e) {
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
