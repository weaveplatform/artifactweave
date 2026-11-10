package smart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccSmartRefreshRetriesParkAndZapRecovery(t *testing.T) {
	w := smartFixture(t)
	w.smartRead()
	entry := w.smartEntry()
	key, err := w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.status = http.StatusServiceUnavailable
	w.mu.Unlock()
	for attempt := 1; attempt <= 3; attempt++ {
		if err = w.s.queue.EnsureFresh(w.ctx, w.s.kind, key); err == nil {
			t.Fatal("outage accepted")
		}
		job, e := w.db.WorkQueue().Get(w.ctx, w.s.kind, key)
		if e != nil || job.Attempts != attempt {
			t.Fatal(job, e)
		}
		if attempt == 3 && job.State != types.WorkParked {
			t.Fatal("unbounded retries", job)
		}
	}
	if _, err = w.s.enqueue(
		w.ctx,
		w.smartRequest(),
		w.permit,
		entry.Token.Key,
		false,
	); !errors.Is(
		err,
		source.ErrUnavailable,
	) {
		t.Fatal("ordinary read revived parked work", err)
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	if err = w.s.queue.EnsureFresh(w.ctx, w.s.kind, key); !errors.Is(err, work.ErrParked) {
		t.Fatal(err)
	}
	w.mu.Lock()
	after := w.calls
	w.mu.Unlock()
	if before != after {
		t.Fatal("parked work performed network I/O")
	}
	if _, err = w.db.RemoteCache().Invalidate(w.ctx, w.c.Key, "file.bin", true); err != nil {
		t.Fatal(err)
	}
	if _, err = w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false); err != nil {
		t.Fatal("explicit zap failed to revive work", err)
	}
	w.mu.Lock()
	w.status = 0
	w.mu.Unlock()
	if err = w.s.queue.EnsureFresh(w.ctx, w.s.kind, key); err != nil {
		t.Fatal(err)
	}
}

func TestAccSmartDurableCapacityAndCorruptPayload(t *testing.T) {
	w := smartFixture(t)
	for n := range 256 {
		if err := w.db.WorkQueue().
			Enqueue(w.ctx, w.s.kind, fmt.Sprintf("pending-%d", n), nil, w.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.s.admit(w.ctx, "another"); !errors.Is(err, remote.ErrBusy) {
		t.Fatal("unbounded admission", err)
	}
	if err := w.s.admit(w.ctx, "pending-1"); err != nil {
		t.Fatal("coalescing blocked", err)
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	for _, payload := range []string{"{", `{}`, `{"item_id":-1}`} {
		if err := w.s.run(
			w.ctx,
			types.WorkItem{Payload: []byte(payload)},
		); !errors.Is(
			err,
			work.ErrPermanent,
		) {
			t.Fatal(err)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.calls != before {
		t.Fatal("invalid job performed I/O")
	}
}

func TestAccSmartStatisticsLostAcknowledgementNeverReplays(t *testing.T) {
	for _, mode := range []string{"oversized", "truncated", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			w := smartFixture(t)
			result := w.smartRead()
			if err := result.Used(w.ctx); err != nil {
				t.Fatal(err)
			}
			entry := w.smartEntry()
			job, err := w.db.WorkQueue().
				Get(w.ctx, w.s.kind, smartWorkKey(entry.Token.Key, w.permit.Identity, true))
			if err != nil {
				t.Fatal(err)
			}
			sends := 0
			w.mu.Lock()
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if !strings.Contains(r.URL.Path, "/statistics/") {
					return false
				}
				sends++
				switch mode {
				case "oversized":
					_, _ = io.WriteString(rw, strings.Repeat("x", (64<<10)+1))
				case "truncated":
					rw.Header().Set("Content-Length", "10")
					_, _ = io.WriteString(rw, "x")
				case "redirect":
					rw.Header().Set("Location", "/again")
					rw.WriteHeader(307)
				}
				return true
			}
			w.mu.Unlock()
			if err = w.s.run(w.ctx, job); !errors.Is(err, errSmartUncertain) {
				t.Fatal(err)
			}
			if err = w.s.run(w.ctx, job); !errors.Is(err, errSmartUncertain) {
				t.Fatal(err)
			}
			w.mu.Lock()
			got := sends
			w.mu.Unlock()
			if got != 1 || w.smartEntry().Smart.Uncertain != 1 {
				t.Fatal("unsafe replay", got, w.smartEntry().Smart)
			}
		})
	}
}

func TestAccSmartOlderRefreshCannotReplaceNewerObservation(t *testing.T) {
	w := smartFixture(t)
	result := w.smartRead()
	entry := w.smartEntry()
	key, err := w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := w.db.WorkQueue().Get(w.ctx, w.s.kind, key)
	if err != nil {
		t.Fatal(err)
	}
	var job smartJob
	if err = json.Unmarshal(queued.Payload, &job); err != nil {
		t.Fatal(err)
	}
	// The peer reply is delayed while a newer representation publishes its state.
	w.mu.Lock()
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if !r.URL.Query().Has("properties") {
			return false
		}
		w.clock.Advance(time.Second)
		e := w.db.Tx(w.ctx, func(tx context.Context) error {
			current, e := w.db.RemoteCache().Read(tx, entry.Token.Key)
			if e != nil {
				return e
			}
			current.Smart.PropertiesAt = w.clock.Now()
			current.Smart.OwnedProperties = types.Properties{"color": {"newer"}}
			if e = w.p.Items.Annotate(
				tx,
				w.c,
				*result.Item,
				types.Properties{"color": {"newer"}},
				nil,
			); e != nil {
				return e
			}
			_, e = w.db.RemoteCache().Put(tx, current.Token, current)
			return e
		})
		if e != nil {
			t.Error(e)
		}
		_, _ = io.WriteString(rw, `{"properties":{"color":["older"]}}`)
		return true
	}
	w.mu.Unlock()
	if err = w.s.run(w.ctx, queued); err != nil {
		t.Fatal(err)
	}
	props, err := w.db.Items().Properties(w.ctx, result.Item.ID)
	if err != nil || strings.Join(props["color"], ",") != "newer" {
		t.Fatal("older reply overwrote state", props, err)
	}
}

func TestAccSmartDiscoveryThrottlePersistsAcrossCalls(t *testing.T) {
	w := smartFixture(t)
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		rw.Header().Set("Retry-After", "120")
		rw.WriteHeader(429)
		return true
	}
	if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
		t.Fatal("throttle ignored")
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
		t.Fatal("cooldown ignored")
	}
	w.mu.Lock()
	after := w.calls
	w.mu.Unlock()
	if after != before {
		t.Fatal("throttle retried", before, after)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || !state.RetryAfterUntil.Equal(w.clock.Now().Add(120*time.Second)) {
		t.Fatal(state, err)
	}
}

func TestAccSmartNoStoreLeavesNoSynchronizationState(t *testing.T) {
	w := smartFixture(t)
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/artifactory/source/file.bin" {
			return false
		}
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(rw, "artifact body")
		return true
	}
	result, err := w.service.Read(w.ctx, w.smartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if w.bytes(result) != "artifact body" {
		t.Fatal("transient content")
	}
	if _, err = w.db.Items().
		Get(w.ctx, w.c.Key, true, "file.bin"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("private bytes persisted", err)
	}
	stats, err := w.db.WorkQueue().Stats(w.ctx)
	if err != nil || len(stats) != 0 {
		t.Fatal("transient sync jobs", stats, err)
	}
}

func TestAccSmartNegotiationDoesNotSurviveCredentialRevision(t *testing.T) {
	w := smartFixture(t)
	w.smartRead()
	before, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || before.Smart == nil {
		t.Fatal(before, err)
	}
	w.c.Password = "new-source-credential"
	w.saveConfig()
	if _, err = w.service.Read(w.ctx, w.smartRequest()); err == nil {
		t.Fatal("cached negotiation accepted changed peer credential")
	}
	w.mu.Lock()
	denied := w.unauthorized
	w.mu.Unlock()
	if denied != 1 {
		t.Fatal("new credential was not checked by discovery", denied)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || state.Smart != nil {
		t.Fatal("stale negotiation persisted", state, err)
	}
}

// enqueue admits, checks and records in one transaction, so a refusal at
// any step queues nothing and leaves the download count as it was.
func TestAccSmartEnqueueFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statistics bool
		fail       func(string) bool
		setup      func(w *smartWorld, ctx *context.Context, q *source.Request, permit *source.Permit)
		want       error
		text       string
	}{
		{name: "no caller", setup: func(w *smartWorld, ctx *context.Context, _ *source.Request, _ *source.Permit) {
			*ctx = w.t.Context()
		}, want: source.ErrDenied},
		{name: "admission held elsewhere", setup: func(w *smartWorld, _ *context.Context, _ *source.Request, _ *source.Permit) {
			now := w.clock.Now()
			lease := types.Lease{Name: w.s.kind, Holder: "another node", Acquired: now, Expires: now.Add(time.Minute)}
			if held, err := w.db.Leases().Acquire(w.ctx, lease, now); err != nil || !held {
				w.t.Fatal(held, err)
			}
		}, want: source.ErrUnavailable},
		{
			name: "work unreadable", fail: failFrom("WorkQueue.Get", 1),
			want: errStoreFault, text: "smart remote work lookup",
		},
		{
			name: "work capacity unreadable", fail: failFrom("WorkQueue.Stats", 1),
			want: errStoreFault, text: "smart remote work capacity",
		},
		{name: "superseded source", setup: func(_ *smartWorld, _ *context.Context, _ *source.Request, permit *source.Permit) {
			permit.Identity.Generation = "superseded"
		}, want: source.ErrSuperseded},
		{
			name: "state lock unavailable", fail: failFrom("RemoteCache.Begin", 1),
			want: errStoreFault, text: "smart remote state lock",
		},
		{
			name: "earlier refresh unreadable", fail: failFrom("WorkQueue.Get", 2),
			want: errStoreFault, text: "smart remote work status",
		},
		{
			name: "download count exhausted", statistics: true,
			setup: func(w *smartWorld, _ *context.Context, _ *source.Request, _ *source.Permit) {
				w.changeEntry(func(e *types.RemoteCacheEntry) { e.Smart.Downloads = math.MaxInt64 })
			},
			want: source.ErrUnavailable,
		},
		{
			name: "download unrecorded", statistics: true, fail: failFrom("RemoteCache.Put", 1),
			want: errStoreFault, text: "store fault: RemoteCache.Put",
		},
		{name: "job too large to queue", setup: func(_ *smartWorld, _ *context.Context, q *source.Request, _ *source.Permit) {
			q.Resource.UpstreamPath = strings.Repeat("a", work.MaxWorkPayload)
		}, want: source.ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.smartRead()
			ctx, q, permit, key := w.ctx, w.smartRequest(), w.permit, w.smartEntry().Token.Key
			if tc.setup != nil {
				tc.setup(w, &ctx, &q, &permit)
			}
			downloads := w.smartEntry().Smart.Downloads
			if tc.fail != nil {
				w.p.Store = &faultStore{Store: w.db, fail: tc.fail}
			}
			_, err := w.s.enqueue(ctx, q, permit, key, tc.statistics)
			if !errors.Is(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) {
				t.Fatal(err)
			}
			stats, err := w.db.WorkQueue().Stats(w.ctx)
			if err != nil || len(stats) != 0 || w.smartEntry().Smart.Downloads != downloads {
				t.Fatal("refused work was recorded", stats, err)
			}
		})
	}
}

// A queued job runs later, under the authority and representation current
// then. Each refusal is permanent, so the queue drops the job rather than
// retry it, and none reaches the peer.
func TestAccSmartQueuedJobFailsClosed(t *testing.T) {
	errDecide := errors.New("smart test: permissions unavailable")
	for _, tc := range []struct {
		name       string
		statistics bool
		config     func(*repo.Config)
		change     func(*smartWorld, *smartJob)
		want       error
	}{
		{name: "registry token outside its grant", change: func(w *smartWorld, job *smartJob) {
			// The token still verifies, so its grant alone refuses the image.
			w.s.authn = &accessapitest.Authenticator{}
			w.s.registryPath = func(path string) (string, string, string, error) {
				image, reference, _ := strings.Cut(path, "/manifests/")
				return image, "manifests", reference, nil
			}
			job.UpstreamPath = "image/manifests/1.0"
			job.Principal.Registry = &kernel.RegistryReference{Verified: true, Access: types.RegistryAccess{
				Realm:  w.c.Key,
				Grants: []types.RegistryGrant{{Type: "repository", Name: "other", Actions: []string{"pull"}}},
			}}
		}, want: source.ErrDenied},
		{name: "permissions unavailable", change: func(w *smartWorld, _ *smartJob) {
			w.s.authz = &accessapitest.Authorizer{
				DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					return nil, errDecide
				},
			}
		}, want: source.ErrDenied},
		{name: "statistics not synchronised", statistics: true, config: func(c *repo.Config) {
			c.ContentSynchronisation.Statistics = &repo.EnabledFlag{}
		}, want: source.ErrDenied},
		{name: "representation replaced", change: func(_ *smartWorld, job *smartJob) {
			job.SHA256 = strings.Repeat("0", 64)
		}, want: source.ErrSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			if tc.config != nil {
				tc.config(&w.c)
				w.saveConfig()
			}
			w.smartRead()
			queued := w.queued(tc.statistics)
			var job smartJob
			if err := json.Unmarshal(queued.Payload, &job); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(w, &job)
			}
			payload, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			queued.Payload = payload
			before := w.peerCalls()
			err = w.s.run(w.ctx, queued)
			if !errors.Is(err, work.ErrPermanent) || !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if w.peerCalls() != before {
				t.Fatal("refused job reached the peer")
			}
		})
	}
}

// fillCapacity takes every slot the policy gives the repository and queues
// as many callers as it lets wait, so the next Acquire answers ErrBusy.
func (w *smartWorld) fillCapacity() {
	w.t.Helper()
	for range 8 {
		release, err := w.p.Acquire(w.ctx, w.c.Key)
		if err != nil {
			w.t.Fatal(err)
		}
		w.t.Cleanup(release)
	}
	ctx, cancel := context.WithCancel(w.ctx)
	var waiters sync.WaitGroup
	w.t.Cleanup(func() {
		cancel()
		waiters.Wait()
	})
	for range 256 {
		waiters.Go(func() {
			if release, err := w.p.Acquire(ctx, w.c.Key); err == nil {
				release()
			}
		})
	}
	// An ended context leaves Acquire at once whether or not it queued, so
	// probing with one waits, without sleeping, until every waiter queued.
	ended, end := context.WithCancel(w.ctx)
	end()
	for {
		if _, err := w.p.Acquire(ended, w.c.Key); errors.Is(err, remote.ErrBusy) {
			return
		}
		runtime.Gosched()
	}
}

// Smart's peer calls take the repository's capacity like any other remote
// read. With none left they answer 503 at once, which the queue retries, and
// send nothing.
func TestAccSmartPeerWorkSharesRemoteCapacity(t *testing.T) {
	busy := &source.HTTPError{Status: http.StatusServiceUnavailable, Kind: source.FailureKindHTTP}
	for _, tc := range []struct {
		name    string
		prepare func(*smartWorld) func() error
	}{
		{"discovery", func(w *smartWorld) func() error {
			return func() error {
				_, err := w.s.Discover(w.ctx, w.smartRequest(), w.permit)
				return err
			}
		}},
		{"queued job", func(w *smartWorld) func() error {
			w.smartRead()
			job := w.queued(false)
			return func() error { return w.s.run(w.ctx, job) }
		}},
		{"AQL query", func(w *smartWorld) func() error {
			peer, err := w.s.Discover(w.ctx, w.smartRequest(), w.permit)
			if err != nil {
				w.t.Fatal(err)
			}
			return func() error {
				_, err := w.s.query(w.ctx, smartQueryRequest(w), w.permit, peer)
				return err
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			call := tc.prepare(w)
			before := w.peerCalls()
			w.fillCapacity()
			if err := call(); !failsWith(err, busy) || errors.Is(err, work.ErrPermanent) {
				t.Fatal(err)
			}
			if w.peerCalls() != before {
				t.Fatal("peer called without capacity")
			}
		})
	}
}

// A refresh publishes the peer's properties in one transaction, and only
// over the representation it read them for: a store fault rolls it back for
// a retry, and a representation fenced while the peer answered is left as
// it is for good.
func TestAccSmartRefreshPublishesOnlyWhenItCompletes(t *testing.T) {
	invalidate := func(metadataOnly bool) func(*smartWorld) {
		return func(w *smartWorld) {
			var once sync.Once
			w.mu.Lock()
			defer w.mu.Unlock()
			w.hook = func(r *http.Request) {
				if !r.URL.Query().Has("properties") {
					return
				}
				once.Do(func() {
					if _, err := w.db.RemoteCache().
						Invalidate(w.ctx, w.c.Key, "file.bin", metadataOnly); err != nil {
						w.t.Error(err)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name      string
		fail      func(string) bool
		setup     func(*smartWorld)
		want      error
		text      string
		permanent bool
		color     string
	}{
		{name: "peer removed the property"},
		{name: "representation removed", setup: invalidate(false), want: source.ErrSuperseded, permanent: true, color: "blue"},
		{name: "representation fenced", setup: invalidate(true), want: source.ErrSuperseded, permanent: true, color: "blue"},
		{name: "properties unreadable", fail: failFrom("Items.Properties", 1), want: errStoreFault, color: "blue"},
		{name: "item unreadable", fail: failFrom("Items.GetByID", 1), want: errStoreFault, color: "blue"},
		{name: "properties unwritable", fail: failFrom("Items.PatchProperties", 1), want: errStoreFault, color: "blue"},
		{
			name: "representations unreadable", fail: failFrom("RemoteCache.Exact", 1),
			want: errStoreFault, text: "smart remote property representations", color: "blue",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			result := w.smartRead()
			job := w.queued(false)
			w.mu.Lock()
			w.props = types.Properties{}
			w.mu.Unlock()
			if tc.setup != nil {
				tc.setup(w)
			}
			if tc.fail != nil {
				faults := &faultStore{Store: w.db, fail: tc.fail}
				w.p.Store, w.p.Items.Store = faults, faults
			}
			err := w.s.run(w.ctx, job)
			if !errors.Is(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) ||
				errors.Is(err, work.ErrPermanent) != tc.permanent {
				t.Fatal(err)
			}
			props, err := w.db.Items().Properties(w.ctx, result.Item.ID)
			if err != nil || strings.Join(props["color"], ",") != tc.color {
				t.Fatal(props, err)
			}
		})
	}
}

// The count is claimed before it is sent, so a send that fails after the
// claim leaves it uncertain and ends the job: here the policy held the send
// off, and nothing reached the peer.
func TestAccSmartStatisticsHeldOffStayUncertain(t *testing.T) {
	w := smartFixture(t)
	if err := w.smartRead().Used(w.ctx); err != nil {
		t.Fatal(err)
	}
	job, err := w.db.WorkQueue().
		Get(w.ctx, w.s.kind, smartWorkKey(w.smartEntry().Token.Key, w.permit.Identity, true))
	if err != nil {
		t.Fatal(err)
	}
	w.throttle(time.Minute)
	err = w.s.run(w.ctx, job)
	if !errors.Is(err, errSmartUncertain) || !errors.Is(err, work.ErrPermanent) {
		t.Fatal(err)
	}
	w.mu.Lock()
	sent := len(w.stats)
	w.mu.Unlock()
	if state := w.smartEntry().Smart; sent != 0 || state.Claimed != 1 || state.Uncertain != 1 {
		t.Fatal("held-off statistics", sent, state)
	}
}

// Without property synchronisation a fill leaves no Smart state, and the
// first statistics count or origin check starts it.
func TestAccSmartSynchronisationWithoutPropertySync(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config func(*smartWorld)
		check  func(*smartWorld, source.Result)
	}{
		{"statistics only", func(*smartWorld) {}, func(w *smartWorld, result source.Result) {
			if state := w.smartEntry().Smart; state != nil {
				w.t.Fatal("fill started Smart state", state)
			}
			if err := result.Used(w.ctx); err != nil {
				w.t.Fatal(err)
			}
			if state := w.smartEntry().Smart; state == nil || state.Downloads != 1 {
				w.t.Fatal(state)
			}
		}},
		{"origin absence only", func(w *smartWorld) {
			w.p.Items.Formats = mavenFormats
			w.c.PackageType = repo.Maven
			w.c.ContentSynchronisation.Statistics = &repo.EnabledFlag{}
			w.c.ContentSynchronisation.Source = &repo.ContentSyncSource{OriginAbsenceDetection: true}
		}, func(w *smartWorld, _ source.Result) {
			state := w.smartEntry().Smart
			if state == nil || state.OriginAt.IsZero() || state.OriginAbsent || !state.PropertiesAt.IsZero() {
				w.t.Fatal(state)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.c.ContentSynchronisation.Properties = &repo.EnabledFlag{}
			tc.config(w)
			w.saveConfig()
			w.mu.Lock()
			w.reply = func(_ http.ResponseWriter, r *http.Request) bool {
				if r.URL.Query().Has("properties") {
					t.Error("properties fetched without property synchronisation")
				}
				return false
			}
			w.mu.Unlock()
			tc.check(w, w.smartRead())
		})
	}
}
