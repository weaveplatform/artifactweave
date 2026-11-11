package smart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestAccSmartDiscoveryRejectsProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, media string
		status                  int
	}{
		{"null", "/api/system/version", "null", "", 200},
		{"html", "/api/system/version", "<html>login</html>", "text/html", 200},
		{"malformed", "/api/system/version", "{", "", 200},
		{"oversized", "/api/system/version", strings.Repeat("x", 1<<20+1), "", 200},
		{"version", "/api/system/version", `{"version":"unknown"}`, "", 200},
		{"old", "/api/system/version", `{"version":"3.9.0"}`, "", 200},
		{"missing", "/api/system/version", "", "", 404},
		{"throttled", "/api/system/version", "", "", 429},
		{"unsolicited304", "/api/system/version", "", "", 304},
		{"virtual", "/api/repositories/source", `{"rclass":"virtual"}`, "", 200},
		{"unknown", "/api/repositories/source", `{"rclass":"invalid"}`, "", 200},
		{"denied", "/api/repositories/source", "", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/artifactory"+tc.path {
					return false
				}
				if tc.media != "" {
					rw.Header().Set("Content-Type", tc.media)
				}
				rw.WriteHeader(tc.status)
				_, _ = io.WriteString(rw, tc.body)
				return true
			}
			if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
				t.Fatal("invalid discovery admitted")
			}
			if _, err := w.db.Items().
				Get(w.ctx, w.c.Key, true, "file.bin"); !errors.Is(
				err,
				kernel.ErrNotFound,
			) {
				t.Fatal("discovery failure published", err)
			}
		})
	}
}

func TestAccSmartSharedDiscoveryAndNoStore(t *testing.T) {
	w := smartFixture(t)
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/artifactory/api/repositories/source":
			rw.WriteHeader(403)
			return true
		case "/artifactory/api/repositories/source/configuration":
			rw.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(rw, `{"rclass":"remote"}`)
			return true
		}
		return false
	}
	w.smartRead()
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || state.Smart != nil {
		t.Fatal("private discovery was persisted", state, err)
	}
	w.smartRead()
	if smartTTL(repo.Config{}) != 2*time.Hour {
		t.Fatal("metadata default changed")
	}
}

func TestAccSmartPropertyDocumentAdmission(t *testing.T) {
	for _, body := range []string{`{}`, `{"properties":null}`, `{"properties":{"bad;key":["x"]}}`, `{"properties":{"empty":[]}}`, `{"properties":{"x":"not-an-array"}}`} {
		t.Run(body, func(t *testing.T) {
			w := smartFixture(t)
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if !r.URL.Query().Has("properties") {
					return false
				}
				_, _ = io.WriteString(rw, body)
				return true
			}
			if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
				t.Fatal("malformed property snapshot admitted")
			}
		})
	}
	for _, fault := range []string{"no-store", "empty-properties", "wrong-item", "missing"} {
		t.Run(fault, func(t *testing.T) {
			w := smartFixture(t)
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if !strings.HasPrefix(r.URL.Path, "/artifactory/api/storage/source/") {
					return false
				}
				if r.URL.Query().Has("properties") {
					if fault == "no-store" {
						rw.Header().Set("Cache-Control", "no-store")
						return false
					}
					rw.WriteHeader(404)
					return true
				}
				if fault == "wrong-item" {
					_, _ = io.WriteString(rw, `{"repo":"other","path":"/file.bin"}`)
					return true
				}
				if fault == "missing" {
					rw.WriteHeader(404)
					return true
				}
				return false
			}
			result, err := w.service.Read(w.ctx, w.smartRequest())
			if fault == "empty-properties" {
				if err != nil || result.Item == nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid item metadata admitted")
			}
		})
	}
}

func TestAccSmartMetadataTargetAndRedirectFence(t *testing.T) {
	for _, redirect := range []string{"", "/artifactory/api/storage/other/private", "/artifactory/api/storage/source/file.bin/../private", "/artifactory/api/storage/source/private"} {
		t.Run(redirect, func(t *testing.T) {
			w := smartFixture(t)
			peer, err := remote.ParseSmartPeer(w.c)
			if err != nil {
				t.Fatal(err)
			}
			target, err := peer.StorageURL("file.bin", url.Values{"list": {""}})
			if err != nil {
				t.Fatal(err)
			}
			q := w.smartRequest()
			q.Resource.Kind = source.ResourceKindMetadata
			q.Resource.RequireSmart = true
			q.Resource.Variant = "smart-listing-v1"
			q.Resource.ExternalURL = target
			q.Resource.CheckURL = func(_ context.Context, u *url.URL) error {
				if u.Path == "/artifactory/api/storage/source/private" {
					return source.ErrDenied
				}
				return nil
			}
			q.Resource.ExpectedContentType = "application/json"
			q.Resource.ValidateMetadata = func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil }
			if redirect != "" {
				w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
					if r.URL.Query().Has("list") {
						rw.Header().Set("Location", redirect)
						rw.WriteHeader(302)
						return true
					}
					return false
				}
			}
			result, err := w.service.Read(w.ctx, q)
			if redirect != "" {
				if err == nil {
					t.Fatal("metadata redirect escaped discovered repository")
				}
				return
			}
			if err != nil || result.Stream == nil {
				t.Fatal(err)
			}
			_ = result.Stream.Body.Close()
			for _, bad := range []string{peer.BaseURL + "/api/storage/other/file", peer.BaseURL + "/api/storage/sourcex/file", "https://other.example/api/storage/source/file", peer.BaseURL + "/api/storage/source/a%252fb", peer.BaseURL + "/api/storage/source/a//b"} {
				q.Resource.ExternalURL = bad
				if _, err = w.service.Read(w.ctx, q); err == nil {
					t.Fatal("metadata authority accepted", bad)
				}
			}
		})
	}
	if smartMetadataTarget(remote.SmartPeer{}, "bad") {
		t.Fatal("invalid peer accepted")
	}
}

func TestAccSmartLoopBoundAndCapabilityLimits(t *testing.T) {
	for _, trace := range [][]string{{"node"}, {""}, {strings.Repeat("x", 257)}, {"control\n"}, {"1", "2", "3", "4", "5", "6", "7", "8"}} {
		ctx := context.WithValue(t.Context(), smartTraceKey{}, trace)
		if _, err := smartEnter(ctx, "node"); err == nil {
			t.Fatal("recursive or invalid chain accepted", trace)
		}
	}
	called := false
	h := SmartTrace(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		trace, _ := r.Context().Value(smartTraceKey{}).([]string)
		if len(trace) != 8 {
			t.Error("unbounded incoming trace", len(trace))
		}
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(SmartOriginHeader, strings.Repeat("node,", 10000))
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("middleware failed")
	}
	for _, tc := range []struct {
		version string
		kind    repo.PackageType
		class   repo.Class
		ok      bool
	}{
		{"4.1.0", repo.Generic, repo.Local, true}, {"4.2.0", repo.Generic, repo.Local, true}, {"4.3.2", repo.Maven, repo.Local, true}, {"4.3.3", repo.Maven, repo.Local, true}, {"7.133.8", repo.Go, repo.Virtual, true}, {"7.133.8", repo.Go, repo.Local, false}, {"7.133.8", repo.Generic, repo.Virtual, false}, {"7.x.0", repo.Generic, repo.Local, false}, {"7.1", repo.Generic, repo.Local, false}, {"-1.2.0", repo.Generic, repo.Local, false},
	} {
		c := repo.Config{PackageType: tc.kind}
		err := smartCapabilities(c, remote.SmartPeer{Version: tc.version, Class: tc.class})
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
	c := repo.Config{
		PackageType: repo.Maven,
		ContentSynchronisation: &repo.ContentSynchronisation{
			Enabled:    true,
			Statistics: &repo.EnabledFlag{Enabled: true},
			Source:     &repo.ContentSyncSource{OriginAbsenceDetection: true},
		},
	}
	for _, version := range []string{"4.1.0", "4.3.2"} {
		if err := smartCapabilities(
			c,
			remote.SmartPeer{Version: version, Class: repo.Local},
		); err == nil {
			t.Fatal("unsupported capability", version)
		}
	}
}

// errStoreFault is what a faultStore call fails with. The call's name
// follows it in the message, so a test can tell which call failed.
var errStoreFault = errors.New("smart test: store fault")

// faultStore passes every call to the store it wraps, transactions
// included, except the calls fail picks, which answer errStoreFault.
type faultStore struct {
	item.Store
	fail func(call string) bool
}

// failFrom picks call from its nth use on.
func failFrom(call string, nth int64) func(string) bool {
	var seen atomic.Int64
	return func(c string) bool { return c == call && seen.Add(1) >= nth }
}

func (f *faultStore) fault(call string) error {
	if f.fail(call) {
		return fmt.Errorf("%w: %s", errStoreFault, call)
	}
	return nil
}

func (f *faultStore) Repos() port.RepoStore { return faultRepos{f.Store.Repos(), f} }

func (f *faultStore) RemoteCache() port.RemoteCacheStore {
	return faultRemoteCache{f.Store.RemoteCache(), f}
}

func (f *faultStore) Items() port.ItemStore { return faultItems{f.Store.Items(), f} }

func (f *faultStore) WorkQueue() work.WorkQueue { return faultWork{f.Store.WorkQueue(), f} }

type faultRepos struct {
	port.RepoStore
	f *faultStore
}

func (r faultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if err := r.f.fault("Repos.Get"); err != nil {
		return repo.Config{}, err
	}
	return r.RepoStore.Get(ctx, key)
}

type faultRemoteCache struct {
	port.RemoteCacheStore
	f *faultStore
}

func (c faultRemoteCache) Source(ctx context.Context, key string) (model.SourceIdentity, error) {
	if err := c.f.fault("RemoteCache.Source"); err != nil {
		return model.SourceIdentity{}, err
	}
	return c.RemoteCacheStore.Source(ctx, key)
}

func (c faultRemoteCache) Begin(
	ctx context.Context,
	key model.RemoteCacheKey,
) (model.RemoteCacheToken, error) {
	if err := c.f.fault("RemoteCache.Begin"); err != nil {
		return model.RemoteCacheToken{}, err
	}
	return c.RemoteCacheStore.Begin(ctx, key)
}

func (c faultRemoteCache) Read(
	ctx context.Context,
	key model.RemoteCacheKey,
) (model.RemoteCacheEntry, error) {
	if err := c.f.fault("RemoteCache.Read"); err != nil {
		return model.RemoteCacheEntry{}, err
	}
	return c.RemoteCacheStore.Read(ctx, key)
}

func (c faultRemoteCache) Exact(
	ctx context.Context,
	key, path string,
	p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	if err := c.f.fault("RemoteCache.Exact"); err != nil {
		return nil, "", err
	}
	return c.RemoteCacheStore.Exact(ctx, key, path, p)
}

func (c faultRemoteCache) Put(
	ctx context.Context,
	token model.RemoteCacheToken,
	entry model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	if err := c.f.fault("RemoteCache.Put"); err != nil {
		return model.RemoteCacheEntry{}, err
	}
	return c.RemoteCacheStore.Put(ctx, token, entry)
}

func (c faultRemoteCache) Touch(
	ctx context.Context,
	token model.RemoteCacheToken,
	at time.Time,
) error {
	if err := c.f.fault("RemoteCache.Touch"); err != nil {
		return err
	}
	return c.RemoteCacheStore.Touch(ctx, token, at)
}

func (c faultRemoteCache) State(ctx context.Context, key string) (model.RemoteSourceState, error) {
	if err := c.f.fault("RemoteCache.State"); err != nil {
		return model.RemoteSourceState{}, err
	}
	return c.RemoteCacheStore.State(ctx, key)
}

type faultItems struct {
	port.ItemStore
	f *faultStore
}

func (i faultItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if err := i.f.fault("Items.GetByID"); err != nil {
		return model.Item{}, err
	}
	return i.ItemStore.GetByID(ctx, id)
}

func (i faultItems) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if err := i.f.fault("Items.Properties"); err != nil {
		return nil, err
	}
	return i.ItemStore.Properties(ctx, id)
}

func (i faultItems) PatchProperties(
	ctx context.Context,
	id int64,
	set model.Properties,
	remove []string,
	at time.Time,
) error {
	if err := i.f.fault("Items.PatchProperties"); err != nil {
		return err
	}
	return i.ItemStore.PatchProperties(ctx, id, set, remove, at)
}

type faultWork struct {
	work.WorkQueue
	f *faultStore
}

func (q faultWork) Get(ctx context.Context, kind, key string) (work.WorkItem, error) {
	if err := q.f.fault("WorkQueue.Get"); err != nil {
		return work.WorkItem{}, err
	}
	return q.WorkQueue.Get(ctx, kind, key)
}

func (q faultWork) Stats(ctx context.Context) ([]work.WorkStats, error) {
	if err := q.f.fault("WorkQueue.Stats"); err != nil {
		return nil, err
	}
	return q.WorkQueue.Stats(ctx)
}

// faultPool hands out the pool's connections with one step of a peer
// request failing: opening the connection, its destination check or its
// request preparation; send, when set, replaces the send.
type faultPool struct {
	remote.Upstream
	connection, checkURL, prepare error
	send                          func(*http.Request) (*http.Response, error)
}

func (p faultPool) Connection(c repo.Config, opts remote.Options) (remote.Connection, error) {
	if p.connection != nil {
		return nil, p.connection
	}
	conn, err := p.Upstream.Connection(c, opts)
	if err != nil {
		return nil, err
	}
	return faultConnection{Connection: conn, pool: p}, nil
}

type faultConnection struct {
	remote.Connection
	pool faultPool
}

func (c faultConnection) CheckURL(ctx context.Context, u *url.URL) error {
	if c.pool.checkURL != nil {
		return c.pool.checkURL
	}
	return c.Connection.CheckURL(ctx, u)
}

func (c faultConnection) Prepare(req *http.Request) error {
	if c.pool.prepare != nil {
		return c.pool.prepare
	}
	return c.Connection.Prepare(req)
}

func (c faultConnection) Send(req *http.Request) (*http.Response, error) {
	if c.pool.send != nil {
		return c.pool.send(req)
	}
	return c.Connection.Send(req)
}

// failsWith reports whether err is want: the same sentinel by errors.Is,
// or, for a *source.HTTPError, one with want's status and kind.
func failsWith(err, want error) bool {
	var typed *source.HTTPError
	if errors.As(want, &typed) {
		var got *source.HTTPError
		return errors.As(err, &got) && got.Status == typed.Status && got.Kind == typed.Kind
	}
	return errors.Is(err, want)
}

// An ordinary request can inherit a context that holds a Smart trace. It
// must reach its upstream with the headers it was given, not this node's
// loop trace.
func TestUnitSmartHeadersOnlyForActiveSmartRequests(t *testing.T) {
	ctx := context.WithValue(t.Context(), smartTraceKey{}, []string{"peer", "node"})
	for _, tc := range []struct {
		name   string
		active bool
		want   []string
	}{
		{"inactive", false, []string{"client"}},
		{"active", true, []string{"peer", "node"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(
				remote.WithSmart(ctx, tc.active), http.MethodGet, "http://peer.example/", nil,
			)
			req.Header.Set(SmartOriginHeader, "client")
			smartHeaders(req)
			if got := req.Header.Values(SmartOriginHeader); !slices.Equal(got, tc.want) {
				t.Fatal(got)
			}
		})
	}
}

// A wrong storage path is not an error the peer reports: it answers 404,
// which reads as an item without properties or as one gone from the origin.
func TestUnitSmartStoragePathFollowsUpstreamPath(t *testing.T) {
	const canonical = "org.acme/lib/1.0/lib-1.0.jar"
	for _, tc := range []struct {
		name     string
		kind     repo.PackageType
		upstream string
		want     string
	}{
		{"mapped layout", repo.Maven, "org/acme/lib%2Bext/1.0/lib-1.0.jar", "org/acme/lib+ext/1.0/lib-1.0.jar"},
		{"undecodable", repo.Maven, "org/acme/lib%zz/1.0/lib-1.0.jar", canonical},
		{"registry route", repo.Docker, "lib/manifests/1.0", canonical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := source.Request{
				Repo:     repo.Config{PackageType: tc.kind},
				Resource: source.Resource{Path: canonical, UpstreamPath: tc.upstream},
			}
			if got := smartStoragePath(q); got != tc.want {
				t.Fatal(got)
			}
		})
	}
}

type peerCall struct {
	q              source.Request
	permit         source.Permit
	opts           remote.Options
	peer           remote.SmartPeer
	method, target string
}

// Every refusal comes before anything is sent. The one send in the table
// is a fake that never reaches the peer either: its timeout comes after the
// source changed, and must not count against the source that replaced it.
func TestAccSmartPeerRequestFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(w *smartWorld, c *peerCall)
		want  error
		text  string
	}{
		{name: "unparsable peer", setup: func(_ *smartWorld, c *peerCall) {
			c.q.Repo.URL += "?mirror=1"
		}, want: source.ErrInvalidRequest},
		{name: "other origin", setup: func(_ *smartWorld, c *peerCall) {
			c.target = "http://elsewhere.example/artifactory/api/system/version"
		}, want: remote.ErrSecurity},
		{name: "statistics for another repository", setup: func(_ *smartWorld, c *peerCall) {
			c.method, c.target = http.MethodPut, c.peer.BaseURL+"/api/storage/statistics/other"
		}, want: remote.ErrSecurity},
		{name: "superseded source", setup: func(_ *smartWorld, c *peerCall) {
			c.permit.Identity.Generation = "superseded"
		}, want: source.ErrSuperseded},
		{name: "availability unreadable", setup: func(w *smartWorld, _ *peerCall) {
			w.p.Store = &faultStore{Store: w.db, fail: failFrom("RemoteCache.State", 1)}
		}, want: errStoreFault, text: "read remote availability: smart test: store fault: RemoteCache.State"},
		{name: "throttled", setup: func(w *smartWorld, _ *peerCall) {
			w.throttle(time.Minute)
		}, want: &source.HTTPError{Status: http.StatusTooManyRequests, Kind: source.FailureKindHTTP}},
		{name: "pool closed", setup: func(w *smartWorld, _ *peerCall) {
			w.s.pool = faultPool{Upstream: w.p.Pool, connection: remote.ErrClosed}
		}, want: source.ErrUnavailable},
		{name: "destination refused", setup: func(w *smartWorld, _ *peerCall) {
			w.s.pool = faultPool{Upstream: w.p.Pool, checkURL: remote.ErrSecurity}
		}, want: &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindSecurity}},
		{name: "credentials unavailable", setup: func(w *smartWorld, _ *peerCall) {
			w.s.pool = faultPool{Upstream: w.p.Pool, prepare: remote.ErrConnection}
		}, want: &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindConnectivity}},
		{name: "timeout after the source changed", setup: func(w *smartWorld, _ *peerCall) {
			w.s.pool = faultPool{Upstream: w.p.Pool, send: func(*http.Request) (*http.Response, error) {
				w.c.URL += "-moved"
				w.saveConfig()
				return nil, context.DeadlineExceeded
			}}
		}, want: source.ErrSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			peer, err := remote.ParseSmartPeer(w.c)
			if err != nil {
				t.Fatal(err)
			}
			opts, err := w.p.Options(w.ctx, w.c)
			if err != nil {
				t.Fatal(err)
			}
			c := peerCall{
				q: w.smartRequest(), permit: w.permit, opts: opts, peer: peer,
				method: http.MethodGet, target: peer.BaseURL + "/api/system/version",
			}
			tc.setup(w, &c)
			res, err := w.s.request(w.ctx, c.q, c.permit, c.opts, c.method, c.target, nil)
			if err == nil {
				_ = res.Body.Close()
			}
			if !failsWith(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) {
				t.Fatal(err)
			}
			if calls := w.peerCalls(); calls != 0 {
				t.Fatal("refused request reached the peer", calls)
			}
		})
	}
}

// A discovery refused before its first request keeps nothing, so the next
// one starts over.
func TestAccSmartDiscoverFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(w *smartWorld, permit *source.Permit)
		want  error
		text  string
	}{
		{name: "cache-only permit", setup: func(_ *smartWorld, permit *source.Permit) {
			permit.Intent = source.RetrievalIntentCacheOnly
		}, want: source.ErrDenied},
		{name: "resolver fails", setup: func(w *smartWorld, _ *source.Permit) {
			w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
				return remote.Options{}, remote.ErrConfiguration
			}
		}, want: &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindProtocol}},
		{name: "superseded source", setup: func(_ *smartWorld, permit *source.Permit) {
			permit.Identity.Generation = "superseded"
		}, want: source.ErrSuperseded},
		{name: "availability unreadable", setup: func(w *smartWorld, _ *source.Permit) {
			w.p.Store = &faultStore{Store: w.db, fail: failFrom("RemoteCache.State", 1)}
		}, want: errStoreFault, text: "read remote availability: smart test: store fault: RemoteCache.State"},
		{name: "pool closed", setup: func(w *smartWorld, _ *source.Permit) {
			w.s.pool = faultPool{Upstream: w.p.Pool, connection: remote.ErrClosed}
		}, want: source.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			permit := w.permit
			tc.setup(w, &permit)
			_, err := w.s.Discover(w.ctx, w.smartRequest(), permit)
			if !failsWith(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) {
				t.Fatal(err)
			}
			state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
			if err != nil || state.Smart != nil || w.peerCalls() != 0 {
				t.Fatal("refused discovery reached the peer or was kept", state.Smart, err)
			}
		})
	}
}

// Discover keeps a peer in a transaction that rechecks the caller's
// authority and then reads the availability state. The transactions that
// record each peer response read that state too but recheck nothing, so the
// state reads after a recheck in a transaction are the keeping one's.
func TestAccSmartDiscoveryKeptOnlyAfterItsRecheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke bool
		want   error
	}{
		{"authority revoked", true, source.ErrDenied},
		{"availability unreadable", false, errStoreFault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			var rechecked atomic.Bool
			check := w.p.CheckCurrent
			w.p.CheckCurrent = func(
				ctx context.Context, st remote.Store, q source.Request, permit source.Permit, revision string,
			) error {
				if w.db.InTx(ctx) {
					rechecked.Store(true)
					if tc.revoke {
						return source.ErrDenied
					}
				}
				return check(ctx, st, q, permit, revision)
			}
			w.p.Store = &faultStore{Store: w.db, fail: func(call string) bool {
				return call == "RemoteCache.State" && rechecked.Load()
			}}
			if _, err := w.s.Discover(w.ctx, w.smartRequest(), w.permit); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
			if err != nil || state.Smart != nil || !rechecked.Load() {
				t.Fatal("discovery kept without its recheck", state.Smart, err)
			}
		})
	}
}

func TestAccSmartDiscoveryReadsClassFromType(t *testing.T) {
	w := smartFixture(t)
	w.mu.Lock()
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/artifactory/api/repositories/source" {
			return false
		}
		_, _ = io.WriteString(rw, `{"type":"local"}`)
		return true
	}
	w.mu.Unlock()
	peer, err := w.s.Discover(w.ctx, w.smartRequest(), w.permit)
	if err != nil || peer.Class != repo.Local {
		t.Fatal(peer, err)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || state.Smart == nil || state.Smart.Class != string(repo.Local) {
		t.Fatal(state.Smart, err)
	}
}

// A storage path must name the item and nothing else, so one with its own
// query is refused before anything is sent. An outage stays an error: read
// as absence, it would mark the item gone from the origin.
func TestAccSmartStorageRequestsFailClosed(t *testing.T) {
	properties := func(w *smartWorld, q source.Request, opts remote.Options, peer remote.SmartPeer) error {
		_, _, err := w.s.properties(w.ctx, q, w.permit, opts, peer)
		return err
	}
	present := func(w *smartWorld, q source.Request, opts remote.Options, peer remote.SmartPeer) error {
		_, err := w.s.present(w.ctx, q, w.permit, opts, peer)
		return err
	}
	for _, tc := range []struct {
		name   string
		call   func(*smartWorld, source.Request, remote.Options, remote.SmartPeer) error
		path   string
		status int
		want   error
		sent   int
	}{
		{"properties of a path with a query", properties, "file.bin?list", 0, source.ErrInvalidRequest, 0},
		{"presence of a path with a query", present, "file.bin?list", 0, source.ErrInvalidRequest, 0},
		{
			"presence during an outage", present, "file.bin", http.StatusServiceUnavailable,
			&source.HTTPError{Status: http.StatusServiceUnavailable, Kind: source.FailureKindHTTP}, 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			peer, err := w.s.Discover(w.ctx, w.smartRequest(), w.permit)
			if err != nil {
				t.Fatal(err)
			}
			opts, err := w.p.Options(w.ctx, w.c)
			if err != nil {
				t.Fatal(err)
			}
			w.mu.Lock()
			w.status = tc.status
			w.mu.Unlock()
			q := w.smartRequest()
			q.Resource.Path = tc.path
			before := w.peerCalls()
			if err = tc.call(w, q, opts, peer); !failsWith(err, tc.want) {
				t.Fatal(err)
			}
			if sent := w.peerCalls() - before; sent != tc.sent {
				t.Fatal("storage requests sent", sent)
			}
		})
	}
}

// The peer's system namespaces describe its own storage and formats; copied
// in, they would pose as properties this repository set itself.
func TestAccSmartPeerSystemPropertiesNotCopied(t *testing.T) {
	w := smartFixture(t)
	w.mu.Lock()
	w.props = model.Properties{
		"color":                {"blue"},
		"artifactory.licenses": {"MIT"},
		"docker.manifest.type": {"v2"},
	}
	w.mu.Unlock()
	result := w.smartRead()
	props, err := w.db.Items().Properties(w.ctx, result.Item.ID)
	if err != nil || len(props) != 1 || strings.Join(props["color"], ",") != "blue" {
		t.Fatal(props, err)
	}
}

// An item of a property-synchronised repository is published only with its
// peer properties. A fill that did not come through Smart's read has no
// peer to take them from, and one that cannot read the item's earlier
// properties cannot tell whether it needs them.
func TestAccSmartUploadPropertiesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(w *smartWorld, ctx *context.Context, q *source.Request)
		want  error
		text  string
	}{
		{name: "outside a Smart read", want: source.ErrUnavailable},
		{name: "initial-only properties unreadable", setup: func(w *smartWorld, ctx *context.Context, q *source.Request) {
			cs := *q.Repo.ContentSynchronisation
			cs.Properties = &repo.EnabledFlag{}
			q.Repo.ContentSynchronisation, q.Repo.SynchronizeProperties = &cs, new(true)
			peer, err := w.s.Discover(w.ctx, w.smartRequest(), w.permit)
			if err != nil {
				w.t.Fatal(err)
			}
			opts, err := w.p.Options(w.ctx, w.c)
			if err != nil {
				w.t.Fatal(err)
			}
			*ctx = context.WithValue(w.ctx, smartSessionKey{}, smartSession{peer: peer, opts: opts})
			w.p.Items.Store = &faultStore{Store: w.db, fail: failFrom("RemoteCache.Exact", 1)}
		}, want: errStoreFault, text: "smart remote initial properties: cache property representations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.smartRead()
			ctx, q := w.ctx, w.smartRequest()
			if tc.setup != nil {
				tc.setup(w, &ctx, &q)
			}
			opts, err := w.p.Options(w.ctx, w.c)
			if err != nil {
				t.Fatal(err)
			}
			key := w.p.CacheKey(q, opts.Revision)
			u := &item.CacheUpload{Token: model.RemoteCacheToken{Key: key}}
			before := w.peerCalls()
			err = w.s.prepareUpload(ctx, q, w.permit, opts, u)
			if !errors.Is(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.text) {
				t.Fatal(err)
			}
			if u.SmartProperties != nil || w.peerCalls() != before {
				t.Fatal("upload properties fetched after refusal")
			}
		})
	}
}
