package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

type smartWorld struct {
	*policyWorld
	s            *SmartRemote
	service      *source.Service
	ctx          context.Context
	mu           sync.Mutex
	props        types.Properties
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
		props:       types.Properties{"color": {"blue"}},
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
	hash, err := auth.HashPasswordWith(
		auth.HashParams{Time: 1, MemoryKiB: 8, Threads: 1},
		"reader-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.Users().
		Create(t.Context(), types.User{Name: "reader", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err = w.db.Permissions().
		Create(t.Context(), types.Permission{Name: "read", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Targets: map[string]types.Target{w.c.Key: {}}, Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}}}}); err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(
		w.db.Users(),
		w.db.Settings(),
		auth.Options{Hash: auth.HashParams{Time: 1, MemoryKiB: 8, Threads: 1}, Now: w.clock.Now},
	)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Basic(t.Context(), "reader", "reader-password")
	if err != nil {
		t.Fatal(err)
	}
	p, err = auth.Authorize(
		t.Context(),
		authz.Sources{Permissions: w.db.Permissions(), Groups: w.db.Groups()},
		p,
	)
	if err != nil {
		t.Fatal(err)
	}
	w.ctx = auth.WithPrincipal(t.Context(), p)
	resolver, err := NewResolver(
		w.db,
		nil,
		ResolverOptions{RevisionKey: []byte("smart-test-only-credential-revision-key")},
	)
	if err != nil {
		t.Fatal(err)
	}
	w.p.Options = resolver.Options
	w.p.CheckCurrent = func(ctx context.Context, _ store.Store, q source.Request, permit source.Permit, revision string) error {
		current, err := w.db.Repos().Get(ctx, q.Repo.Key)
		if err != nil || store.RemotePolicyChanged(current, q.Repo) {
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
		principal, ok := auth.FromContext(ctx)
		if !ok {
			return source.ErrDenied
		}
		principal, err = a.Current(ctx, principal, w.db.Tokens())
		if err != nil {
			return source.ErrDenied
		}
		principal, err = auth.Authorize(
			ctx,
			authz.Sources{
				Permissions:      w.db.Permissions(),
				Groups:           w.db.Groups(),
				AnonymousEnabled: principal.Anonymous,
			},
			principal,
		)
		if err != nil ||
			!principal.Access().
				Allows(types.ResourceArtifact, authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)}, q.Resource.Path, types.ActionRead) {
			return source.ErrDenied
		}
		return generic.CheckDownload(q.Repo, q.Resource.Path, false)
	}
	w.s, err = NewSmartRemote(
		t.Context(),
		w.p,
		w.p.Pool,
		a,
		"remote.smart",
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
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
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
	p, _ := auth.FromContext(w.ctx)
	q := w.request("file.bin", source.Artifact)
	q.Subject, q.Access = p.Subject(), p.Access()
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

func (w *smartWorld) smartEntry() types.RemoteCacheEntry {
	w.t.Helper()
	opts, err := w.p.Options(w.ctx, w.c)
	if err != nil {
		w.t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Read(w.ctx, w.p.cacheKey(w.smartRequest(), opts.Revision))
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
		types.Properties{"local": {"keep"}, "color": {"mine"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.props = types.Properties{"color": {"red"}, "new": {"value"}}
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

func TestAccSmartOriginAbsenceAndOutage(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Generic, repo.Maven} {
		t.Run(string(kind), func(t *testing.T) {
			w := smartFixture(t)
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
			w.props = types.Properties{"color": {"red"}}
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
		store.ErrNotFound,
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
	w.props = types.Properties{}
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
	w.props = types.Properties{"color": {"red"}}
	w.mu.Unlock()
	if _, err := w.service.Read(w.ctx, q); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.props = types.Properties{"color": {"green"}}
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
		types.Properties{"color": {"local"}},
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
