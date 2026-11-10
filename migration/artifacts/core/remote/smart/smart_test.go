package smart

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/upstreamtest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	artmodel "github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/kernel/wire"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

type smartWorld struct {
	*policyWorld
	s            *Smart
	service      *source.Service
	ctx          context.Context
	mu           sync.Mutex
	props        artmodel.Properties
	present      bool
	status       int
	statsStatus  int
	stats        []wire.SmartStatistics
	calls        int
	unauthorized int
	hook         func(*http.Request)
	reply        func(http.ResponseWriter, *http.Request) bool
}

func smartFixture(t *testing.T) *smartWorld {
	t.Helper()
	w := &smartWorld{
		policyWorld: policyFixture(t),
		props:       artmodel.Properties{"color": {"blue"}},
		present:     true,
	}
	router := chi.NewRouter()
	router.HandleFunc("/*", w.servePeer)
	up := httptest.NewServer(router)
	t.Cleanup(up.Close)
	w.c.URL, w.c.Username, w.c.Password = up.URL+"/artifactory/source", "upstream", "configured-source-secret"
	w.c.ContentSynchronisation = &repo.ContentSynchronisation{
		Enabled:    true,
		Properties: &repo.EnabledFlag{Enabled: true},
		Statistics: &repo.EnabledFlag{Enabled: true},
	}
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	w.permit.Identity, _ = w.db.RemoteCache().Source(t.Context(), w.c.Key)
	hash := accesstest.HashPassword(t, "reader-password")
	if err := w.db.Users().
		Create(t.Context(), accesstest.User{Name: "reader", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "read", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Targets: map[string]accesstest.Target{w.c.Key: {}}, Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead}}}}}); err != nil {
		t.Fatal(err)
	}
	kit := accesstest.Over(t, w.db, accesstest.WithNow(w.clock.Now))
	api := kit.API()
	p, err := api.Authenticator.Basic(t.Context(), "reader", "reader-password")
	if err != nil {
		t.Fatal(err)
	}
	w.ctx, err = kit.Authorize(t.Context(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	resolver := upstreamtest.NewResolver(t, w.db, []byte("smart-test-only-credential-revision-key"))
	w.p.Options = resolver.Options
	w.p.CheckCurrent = func(ctx context.Context, _ remote.Store, q source.Request, permit source.Permit, revision string) error {
		current, err := w.db.Repos().Get(ctx, q.Repo.Key)
		if err != nil || port.RemotePolicyChanged(current, q.Repo) {
			return source.ErrSuperseded
		}
		id, err := w.db.RemoteCache().Source(ctx, q.Repo.Key)
		if err != nil || id != permit.Identity {
			return source.ErrSuperseded
		}
		opts, err := resolver.Options(ctx, q.Repo)
		if err != nil || opts.Revision != revision {
			return source.ErrSuperseded
		}
		principal, ok := kernel.FromContext(ctx)
		if !ok {
			return source.ErrDenied
		}
		principal, err = api.Authenticator.Current(ctx, principal)
		if err != nil {
			return source.ErrDenied
		}
		_, d, err := api.Authorizer.Authorize(ctx, principal, principal.Anonymous)
		if err != nil ||
			!d.Allows(
				accessapi.ResourceArtifact,
				accessapi.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
				q.Resource.Path,
				accessapi.ActionRead,
			) {
			return source.ErrDenied
		}
		return generic.CheckDownload(q.Repo, q.Resource.Path, false)
	}
	w.s, err = NewSmart(
		t.Context(),
		w.p,
		w.p.Pool,
		api.Authenticator,
		api.Authorizer,
		"remote.smart",
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Keep time and scheduling deterministic. EnsureFresh still runs persisted
	// work synchronously; tests explicitly drive statistics jobs after delivery.
	w.s.Close()
	w.s.queue, err = work.New(
		w.db,
		work.Options{
			Now:         w.clock.Now,
			MaxAttempts: 3,
			Backoff:     func(int) time.Duration { return 0 },
			Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.s.queue.Handle(w.s.kind, w.s.run); err != nil {
		t.Fatal(err)
	}
	w.service = &source.Service{
		HTTP:  w.p,
		Cache: w.p,
		Resolve: func(ctx context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
			return w.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	return w
}

func (w *smartWorld) servePeer(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	hook := w.hook
	w.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	username, password, ok := r.BasicAuth()
	if !ok || username != "upstream" || password != "configured-source-secret" {
		w.unauthorized++
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	if w.reply != nil && w.reply(rw, r) {
		return
	}
	switch r.URL.Path {
	case "/artifactory/api/system/version":
		_, _ = io.WriteString(rw, `{"version":"7.133.8"}`)
	case "/artifactory/api/repositories/source":
		_, _ = io.WriteString(rw, `{"rclass":"local"}`)
	case "/artifactory/api/storage/source/file.bin":
		if w.status != 0 {
			rw.WriteHeader(w.status)
			return
		}
		if !w.present {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Has("properties") {
			_ = json.NewEncoder(rw).Encode(map[string]any{"properties": w.props})
		} else {
			_, _ = io.WriteString(rw, `{"repo":"source","path":"/file.bin"}`)
		}
	case "/artifactory/api/storage/statistics/source":
		if r.Method != http.MethodPut {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var batch []wire.SmartStatistics
		if json.NewDecoder(r.Body).Decode(&batch) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		w.stats = append(w.stats, batch...)
		if w.statsStatus != 0 {
			rw.WriteHeader(w.statsStatus)
		}
	case "/artifactory/source/file.bin":
		rw.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(rw, "artifact body")
	default:
		rw.WriteHeader(http.StatusNotFound)
	}
}

func (w *smartWorld) smartRequest() source.Request {
	p, _ := kernel.FromContext(w.ctx)
	d, _ := accessapi.DeciderFrom(w.ctx)
	q := w.request("file.bin", source.ResourceKindArtifact)
	q.Subject, q.Access = accessapi.SubjectOf(p), d
	return q
}

func (w *smartWorld) smartRead() source.Result {
	w.t.Helper()
	result, err := w.service.Read(w.ctx, w.smartRequest())
	if err != nil {
		w.t.Fatal(err)
	}
	if w.bytes(result) != "artifact body" {
		w.t.Fatal("wrong content")
	}
	return result
}

func (w *smartWorld) smartEntry() artmodel.RemoteCacheEntry {
	w.t.Helper()
	opts, err := w.p.Options(w.ctx, w.c)
	if err != nil {
		w.t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Read(w.ctx, w.p.CacheKey(w.smartRequest(), opts.Revision))
	if err != nil {
		w.t.Fatal(err)
	}
	return entry
}

func TestAccSmartPropertiesRefreshOwnershipAndOffline(t *testing.T) {
	w := smartFixture(t)
	first := w.smartRead()
	props, err := w.db.Items().Properties(w.ctx, first.Item.ID)
	if err != nil || strings.Join(props["color"], ",") != "blue" {
		t.Fatal(props, err)
	}
	if err = w.p.Items.Annotate(
		w.ctx,
		w.c,
		*first.Item,
		artmodel.Properties{"local": {"keep"}, "color": {"mine"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.props = artmodel.Properties{"color": {"red"}, "new": {"value"}}
	w.mu.Unlock()
	w.clock.Advance(31 * time.Second)
	w.smartRead()
	props, err = w.db.Items().Properties(w.ctx, first.Item.ID)
	if err != nil || strings.Join(props["color"], ",") != "mine" ||
		strings.Join(props["new"], ",") != "value" ||
		strings.Join(props["local"], ",") != "keep" {
		t.Fatal(props, err)
	}
	if err = w.p.Items.Annotate(w.ctx, w.c, *first.Item, nil, []string{"new"}); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(31 * time.Second)
	w.smartRead()
	props, _ = w.db.Items().Properties(w.ctx, first.Item.ID)
	if _, ok := props["new"]; ok {
		t.Fatal("upstream resurrected local deletion")
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	q := w.smartRequest()
	q.CacheOnly = true
	if result, err := w.service.Read(w.ctx, q); err != nil || w.bytes(result) != "artifact body" {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.calls != before || w.unauthorized != 0 {
		t.Fatal("cache alias network or credentials", w.calls, before, w.unauthorized)
	}
}

func TestAccSmartStatisticsClaimedOnceAndUncertainty(t *testing.T) {
	for _, status := range []int{0, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			w := smartFixture(t)
			w.statsStatus = status
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
			if job.Key == "" {
				t.Fatal("no durable statistics job")
			}
			err = w.s.run(w.ctx, job)
			if status == 0 && err != nil || status != 0 && !errors.Is(err, errSmartUncertain) {
				t.Fatal(err)
			}
			_ = w.s.run(w.ctx, job)
			w.mu.Lock()
			sent := len(w.stats)
			count := w.stats[0].DownloadCount
			w.mu.Unlock()
			if sent != 1 || count != 1 {
				t.Fatal("statistics replay", sent, count)
			}
			state := w.smartEntry().Smart
			uncertain := int64(0)
			if status != 0 {
				uncertain = 1
			}
			if state.Downloads != 1 || state.Claimed != 1 || state.Uncertain != uncertain {
				t.Fatal(state)
			}
		})
	}
}

func (w *smartWorld) saveConfig() {
	w.t.Helper()
	if err := w.db.Repos().Update(w.ctx, w.c); err != nil {
		w.t.Fatal(err)
	}
	var err error
	w.permit.Identity, err = w.db.RemoteCache().Source(w.ctx, w.c.Key)
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *smartWorld) peerCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// queued queues file.bin's refresh or statistics job and returns it.
func (w *smartWorld) queued(statistics bool) work.WorkItem {
	w.t.Helper()
	key, err := w.s.enqueue(w.ctx, w.smartRequest(), w.permit, w.smartEntry().Token.Key, statistics)
	if err != nil {
		w.t.Fatal(err)
	}
	job, err := w.db.WorkQueue().Get(w.ctx, w.s.kind, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return job
}

// changeEntry rewrites file.bin's cache entry with change.
func (w *smartWorld) changeEntry(change func(*artmodel.RemoteCacheEntry)) {
	w.t.Helper()
	key := w.smartEntry().Token.Key
	if err := w.db.Tx(w.ctx, func(tx context.Context) error {
		entry, err := w.db.RemoteCache().Read(tx, key)
		if err != nil {
			return err
		}
		change(&entry)
		_, err = w.db.RemoteCache().Put(tx, entry.Token, entry)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// throttle holds the peer off for d, as a 429 with Retry-After does. It
// records the hold under the current credential revision because the policy
// drops one recorded under any other.
func (w *smartWorld) throttle(d time.Duration) {
	w.t.Helper()
	opts, err := w.p.Options(w.ctx, w.c)
	if err != nil {
		w.t.Fatal(err)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil {
		w.t.Fatal(err)
	}
	state.SettingsRevision, state.RetryAfterUntil = opts.Revision, w.clock.Now().Add(d)
	if err = w.db.RemoteCache().PutState(w.ctx, w.c.Key, state); err != nil {
		w.t.Fatal(err)
	}
}

// mavenFormats gives a Maven repository the Maven slice's handler, so a test
// that reads one runs that slice's indexing hooks; policyFixture's nil
// registry would write it through the generic handler. Every other type
// stays generic.
func mavenFormats(t repo.PackageType) format.Handler {
	if t == repo.Maven {
		return maven.Handler{}
	}
	return generic.Handler{}
}

func TestAccSmartOriginAbsenceAndOutage(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Generic, repo.Maven} {
		t.Run(string(kind), func(t *testing.T) {
			w := smartFixture(t)
			w.p.Items.Formats = mavenFormats
			w.c.PackageType = kind
			w.c.ContentSynchronisation.Source = &repo.ContentSyncSource{
				OriginAbsenceDetection: true,
			}
			w.saveConfig()
			w.smartRead()
			w.mu.Lock()
			w.present = false
			w.mu.Unlock()
			w.clock.Advance(31 * time.Second)
			w.smartRead()
			state := w.smartEntry().Smart
			if state.OriginAbsent != (kind == repo.Maven) ||
				state.OriginAt.IsZero() != (kind == repo.Generic) {
				t.Fatal(state)
			}
			w.mu.Lock()
			w.status = http.StatusForbidden
			w.mu.Unlock()
			w.clock.Advance(31 * time.Second)
			entry := w.smartEntry()
			key, err := w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false)
			if err != nil {
				t.Fatal(err)
			}
			job, err := w.db.WorkQueue().Get(w.ctx, w.s.kind, key)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.s.run(w.ctx, job); err == nil {
				t.Fatal("source denial accepted")
			}
			later := w.smartEntry().Smart
			if later.OriginAt != state.OriginAt || later.OriginAbsent != state.OriginAbsent {
				t.Fatal("outage became a confirmed deletion", state, later)
			}
			q := w.smartRequest()
			q.CacheOnly = true
			result, err := w.service.Read(w.ctx, q)
			if err != nil || w.bytes(result) != "artifact body" {
				t.Fatal("origin removal deleted cached bytes", err)
			}
		})
	}
}

func TestAccSmartFlagsAndInitialOnlyProperties(t *testing.T) {
	for _, mode := range []string{"disabled", "initial", "properties", "statistics", "head"} {
		t.Run(mode, func(t *testing.T) {
			w := smartFixture(t)
			w.c.ContentSynchronisation = &repo.ContentSynchronisation{
				Enabled:    mode == "properties" || mode == "statistics" || mode == "head",
				Properties: &repo.EnabledFlag{Enabled: mode == "properties"},
				Statistics: &repo.EnabledFlag{Enabled: mode == "statistics" || mode == "head"},
			}
			w.c.SynchronizeProperties = new(mode == "initial")
			w.saveConfig()
			if mode == "head" {
				w.smartRead()
			}
			q := w.smartRequest()
			if mode == "head" {
				q.Resource.Method = http.MethodHead
			}
			result, err := w.service.Read(w.ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			props, err := w.db.Items().Properties(w.ctx, result.Item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (len(props) > 0) != (mode == "properties" || mode == "initial") {
				t.Fatal(mode, props)
			}
			w.mu.Lock()
			w.props = artmodel.Properties{"color": {"red"}}
			before := w.calls
			w.mu.Unlock()
			w.clock.Advance(31 * time.Second)
			if mode == "initial" || mode == "disabled" {
				w.smartRead()
				props, _ = w.db.Items().Properties(w.ctx, result.Item.ID)
				if mode == "initial" && strings.Join(props["color"], ",") != "blue" {
					t.Fatal("legacy setting refreshed properties", props)
				}
			}
			if mode == "head" {
				if result.Used != nil {
					if err = result.Used(w.ctx); err != nil {
						t.Fatal(err)
					}
				}
				stats, err := w.db.WorkQueue().Stats(w.ctx)
				if err != nil || len(stats) != 0 {
					t.Fatal("HEAD queued statistics", stats, err)
				}
			}
			if mode == "disabled" {
				w.mu.Lock()
				after := w.calls
				w.mu.Unlock()
				if after != before {
					t.Fatal("disabled Smart contacted peer for warm content")
				}
			}
		})
	}
}

func TestAccSmartDurableWorkRechecksAuthority(t *testing.T) {
	for _, change := range []string{"permission", "password", "source", "credential", "offline", "evicted"} {
		t.Run(change, func(t *testing.T) {
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
			switch change {
			case "permission":
				err = w.db.Permissions().Delete(w.ctx, "read")
			case "password":
				u, e := w.db.Users().Get(w.ctx, "reader")
				if e != nil {
					t.Fatal(e)
				}
				u.PasswordHash = []byte("changed")
				err = w.db.Users().Update(w.ctx, u)
			case "source":
				w.c.URL += "-changed"
				w.saveConfig()
			case "credential":
				w.c.Password = "different"
				w.saveConfig()
			case "offline":
				w.c.Offline = new(true)
				w.saveConfig()
			case "evicted":
				_, err = w.db.RemoteCache().Invalidate(w.ctx, w.c.Key, "file.bin", false)
			}
			if err != nil {
				t.Fatal(err)
			}
			w.mu.Lock()
			before := w.calls
			w.mu.Unlock()
			if err = w.s.run(w.ctx, job); err == nil {
				t.Fatal("stale work succeeded")
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.calls != before {
				t.Fatal("stale work contacted origin", w.calls, before)
			}
		})
	}
}

func TestAccSmartRevocationDuringProperties(t *testing.T) {
	w := smartFixture(t)
	var once sync.Once
	w.hook = func(r *http.Request) {
		if r.URL.Query().Has("properties") {
			once.Do(func() {
				if err := w.db.Permissions().Delete(w.ctx, "read"); err != nil {
					t.Error(err)
				}
			})
		}
	}
	if _, err := w.service.Read(w.ctx, w.smartRequest()); !errors.Is(err, source.ErrDenied) {
		t.Fatal(err)
	}
	if _, err := w.db.Items().
		Get(w.ctx, w.c.Key, true, "file.bin"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("revoked properties fetch published content", err)
	}
}

func TestAccSmartStatusIsCurrentBoundedAndOffline(t *testing.T) {
	w := smartFixture(t)
	result := w.smartRead()
	if err := result.Used(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	status, err := w.s.Status(w.ctx, w.c.Key, "file.bin")
	if err != nil || len(status.Representations) != 1 || status.Representations[0].Downloads != 1 ||
		status.Representations[0].OriginAbsent != nil {
		t.Fatal(status, err)
	}
	if other, err := w.s.Status(
		w.ctx,
		w.c.Key,
		"file",
	); err != nil ||
		len(other.Representations) != 0 {
		t.Fatal(other, err)
	}
	w.mu.Lock()
	after := w.calls
	w.mu.Unlock()
	if before != after {
		t.Fatal("diagnostic caused upstream I/O")
	}
	w.c.Password = "changed"
	w.saveConfig()
	status, err = w.s.Status(w.ctx, w.c.Key, "file.bin")
	if err != nil || len(status.Representations) != 0 {
		t.Fatal("stale source status disclosed", status, err)
	}
}

func TestAccSmartStatusOriginObservationsRemainPerRepresentation(t *testing.T) {
	w := smartFixture(t)
	w.p.Items.Formats = mavenFormats
	w.c.PackageType = repo.Maven
	w.c.ContentSynchronisation.Source = &repo.ContentSyncSource{OriginAbsenceDetection: true}
	w.saveConfig()
	w.smartRead()
	q := w.smartRequest()
	q.Resource.Accept = "application/octet-stream"
	result, err := w.service.Read(w.ctx, q)
	if err != nil || w.bytes(result) != "artifact body" {
		t.Fatal("second representation", err)
	}
	w.mu.Lock()
	w.present = false
	w.mu.Unlock()
	w.clock.Advance(31 * time.Second)
	result, err = w.service.Read(w.ctx, q)
	if err != nil || w.bytes(result) != "artifact body" {
		t.Fatal("retained absent representation", err)
	}
	status, err := w.s.Status(w.ctx, w.c.Key, "file.bin")
	if err != nil || len(status.Representations) != 2 {
		t.Fatal(status, err)
	}
	absent, present := 0, 0
	for _, representation := range status.Representations {
		if representation.OriginAbsent == nil || representation.OriginCheckedAt.IsZero() {
			t.Fatal("missing origin observation", representation)
		}
		if *representation.OriginAbsent {
			absent++
		} else {
			present++
		}
	}
	if absent != 1 || present != 1 {
		t.Fatal("observations were incorrectly combined across representations", status)
	}
}

func TestAccSmartEmptySnapshotRemovesOwnedPropertiesOnRefill(t *testing.T) {
	w := smartFixture(t)
	q := w.smartRequest()
	q.Resource.Mutable = true
	first, err := w.service.Read(w.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	props, err := w.db.Items().Properties(w.ctx, first.Item.ID)
	if err != nil || len(props) != 1 {
		t.Fatal(props, err)
	}
	w.mu.Lock()
	w.props = artmodel.Properties{}
	w.mu.Unlock()
	w.clock.Advance(31 * time.Second)
	if _, err = w.service.Read(w.ctx, q); err != nil {
		t.Fatal(err)
	}
	props, err = w.db.Items().Properties(w.ctx, first.Item.ID)
	if err != nil || len(props) != 0 {
		t.Fatal("empty snapshot failed to remove owned properties", props, err)
	}
}

func TestAccSmartPropertyOwnershipAcrossRepresentations(t *testing.T) {
	w := smartFixture(t)
	first := w.smartRead()
	q := w.smartRequest()
	q.Resource.Accept = "application/octet-stream"
	w.mu.Lock()
	w.props = artmodel.Properties{"color": {"red"}}
	w.mu.Unlock()
	if _, err := w.service.Read(w.ctx, q); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.props = artmodel.Properties{"color": {"green"}}
	w.mu.Unlock()
	w.clock.Advance(31 * time.Second)
	w.smartRead()
	props, err := w.db.Items().Properties(w.ctx, first.Item.ID)
	if err != nil || strings.Join(props["color"], ",") != "green" {
		t.Fatal("another representation became a local edit", props, err)
	}
	if err = w.p.Items.Annotate(
		w.ctx,
		w.c,
		*first.Item,
		artmodel.Properties{"color": {"local"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(31 * time.Second)
	if _, err = w.service.Read(w.ctx, q); err != nil {
		t.Fatal(err)
	}
	props, err = w.db.Items().Properties(w.ctx, first.Item.ID)
	if err != nil || strings.Join(props["color"], ",") != "local" {
		t.Fatal("local edit overwritten through alternate representation", props, err)
	}
}

// Status reads in one transaction and answers no representations when any
// read fails: a partial answer would pass for a complete one.
func TestAccSmartStatusFailsOnUnreadableState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		fail  func(string) bool
		setup func(*smartWorld)
		want  error
		text  string
	}{
		{
			name: "repository unreadable", fail: failFrom("Repos.Get", 1),
			want: errStoreFault, text: "smart remote operation: smart test: store fault: Repos.Get",
		},
		{name: "not a remote repository", key: "local", setup: func(w *smartWorld) {
			local := repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Generic})
			if err := w.db.Repos().Create(w.ctx, *local); err != nil {
				w.t.Fatal(err)
			}
		}, want: source.ErrInvalidRequest},
		{
			name: "source unreadable", fail: failFrom("RemoteCache.Source", 1),
			want: errStoreFault, text: "store fault: RemoteCache.Source",
		},
		{
			name: "repository unreadable under its lock", fail: failFrom("Repos.Get", 2),
			want: errStoreFault, text: "smart remote current repository",
		},
		{name: "credentials unresolved", setup: func(w *smartWorld) {
			w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
				return remote.Options{}, remote.ErrConfiguration
			}
		}, want: remote.ErrConfiguration},
		{
			name: "representations unreadable", fail: failFrom("RemoteCache.Exact", 1),
			want: errStoreFault, text: "store fault: RemoteCache.Exact",
		},
		{
			name: "item unreadable", fail: failFrom("Items.GetByID", 1),
			want: errStoreFault, text: "store fault: Items.GetByID",
		},
		{
			name: "work unreadable", fail: failFrom("WorkQueue.Get", 1),
			want: errStoreFault, text: "store fault: WorkQueue.Get",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.smartRead()
			if tc.setup != nil {
				tc.setup(w)
			}
			if tc.fail != nil {
				w.p.Store = &faultStore{Store: w.db, fail: tc.fail}
			}
			status, err := w.s.Status(w.ctx, cmp.Or(tc.key, w.c.Key), "file.bin")
			if !errors.Is(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) ||
				len(status.Representations) != 0 {
				t.Fatal(status, err)
			}
		})
	}
}

// A representation whose item was removed, or replaced by other content,
// describes nothing this repository still serves.
func TestAccSmartStatusOmitsRepresentationsWithoutTheirItem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*smartWorld, artmodel.Item) error
	}{
		{"removed", func(w *smartWorld, _ artmodel.Item) error {
			_, err := w.db.Items().Delete(w.ctx, w.c.Key, true, "file.bin")
			return err
		}},
		{"replaced", func(w *smartWorld, it artmodel.Item) error {
			it.Actual.SHA256 = strings.Repeat("f", 64)
			_, err := w.db.Items().Put(w.ctx, it)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			result := w.smartRead()
			if err := tc.change(w, *result.Item); err != nil {
				t.Fatal(err)
			}
			status, err := w.s.Status(w.ctx, w.c.Key, "file.bin")
			if err != nil || len(status.Representations) != 0 {
				t.Fatal(status, err)
			}
		})
	}
}

// The application serves registry remotes with a second Smart whose jobs
// carry the kind remote.smart.registry (migration/app), so Status reads a
// registry remote's work under that kind.
func TestAccSmartStatusFindsRegistryWorkUnderItsKind(t *testing.T) {
	w := smartFixture(t)
	w.c.PackageType = repo.Docker
	w.saveConfig()
	w.s.kind = "remote.smart.registry"
	if err := w.smartRead().Used(w.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := w.s.Status(w.ctx, w.c.Key, "file.bin")
	want := []SmartWorkStatus{{Operation: "statistics", State: work.WorkPending}}
	if err != nil || len(status.Representations) != 1 ||
		!slices.Equal(status.Representations[0].Work, want) {
		t.Fatal(status, err)
	}
}

// Read resolves the credentials again after discovery and goes on only
// under the revision the peer was discovered with.
func TestAccSmartReadRefusesOptionsThatMovedAfterDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		later func(remote.Options) (remote.Options, error)
		want  error
	}{
		{"resolver fails", func(remote.Options) (remote.Options, error) {
			return remote.Options{}, remote.ErrConfiguration
		}, &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindProtocol}},
		{"revision moved", func(opts remote.Options) (remote.Options, error) {
			opts.Revision += "-moved"
			return opts, nil
		}, source.ErrSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.smartRead()
			resolve := w.p.Options
			var resolved atomic.Int32
			w.p.Options = func(ctx context.Context, c repo.Config) (remote.Options, error) {
				opts, err := resolve(ctx, c)
				if err != nil || resolved.Add(1) == 1 {
					return opts, err
				}
				return tc.later(opts)
			}
			before := w.peerCalls()
			if _, err := w.s.Read(w.ctx, w.smartRequest(), w.permit); !failsWith(err, tc.want) {
				t.Fatal(err)
			}
			// The first resolution is discovery's, which the earlier read
			// left cached; only the second may move.
			if resolved.Load() != 2 || w.peerCalls() != before {
				t.Fatal("resolutions", resolved.Load(), "peer calls", w.peerCalls()-before)
			}
		})
	}
}

// Read checks whether a refresh is due from the representation's Smart
// state, and fails rather than deliver without that check.
func TestAccSmartReadFailsWithoutItsRefreshState(t *testing.T) {
	w := smartFixture(t)
	w.smartRead()
	// The policy reads the entry and then the item it delivers; the read of
	// the Smart state is the next entry read.
	var delivered atomic.Bool
	w.p.Store = &faultStore{Store: w.db, fail: func(call string) bool {
		switch call {
		case "Items.GetByID":
			delivered.Store(true)
		case "RemoteCache.Read":
			return delivered.Load()
		}
		return false
	}}
	_, err := w.s.Read(w.ctx, w.smartRequest(), w.permit)
	if !errors.Is(err, errStoreFault) ||
		!strings.HasPrefix(fmt.Sprint(err), "smart remote read: ") {
		t.Fatal(err)
	}
}

// A due refresh runs before the read delivers. When it fails the read fails
// too, and the job stays queued for a retry.
func TestAccSmartReadFailsWhenItsDueRefreshFails(t *testing.T) {
	w := smartFixture(t)
	w.smartRead()
	w.mu.Lock()
	w.status = http.StatusServiceUnavailable
	w.mu.Unlock()
	w.clock.Advance(31 * time.Second)
	_, err := w.s.Read(w.ctx, w.smartRequest(), w.permit)
	outage := &source.HTTPError{Status: http.StatusServiceUnavailable, Kind: source.FailureKindHTTP}
	if !failsWith(err, outage) || !strings.HasPrefix(fmt.Sprint(err), "smart remote refresh: ") {
		t.Fatal(err)
	}
	job, err := w.db.WorkQueue().
		Get(w.ctx, w.s.kind, smartWorkKey(w.smartEntry().Token.Key, w.permit.Identity, false))
	if err != nil || job.State != work.WorkPending || job.Attempts != 1 {
		t.Fatal(job, err)
	}
}

// A download counts for the peer only once the policy has recorded the use
// of the cached copy, so a use it failed to record counts nothing.
func TestAccSmartUseCountsOnlyWhatThePolicyRecorded(t *testing.T) {
	w := smartFixture(t)
	result := w.smartRead()
	w.p.Store = &faultStore{Store: w.db, fail: failFrom("RemoteCache.Touch", 1)}
	if err := result.Used(w.ctx); !errors.Is(err, errStoreFault) ||
		!strings.Contains(fmt.Sprint(err), "record remote cache use") {
		t.Fatal(err)
	}
	stats, err := w.db.WorkQueue().Stats(w.ctx)
	if err != nil || len(stats) != 0 || w.smartEntry().Smart.Downloads != 0 {
		t.Fatal("unrecorded use was counted", stats, err)
	}
}
