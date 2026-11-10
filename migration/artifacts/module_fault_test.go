package artifacts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// errFault is what the call a faultStore names fails with.
var errFault = errors.New("injected store fault")

// faultStore wraps a real store and fails the one call that fail names,
// "Leases.Acquire", "Settings.Get", "Settings.Put" or "RemoteCache.Source",
// with errFault. Every other call reaches the store, so the module runs as
// it would up to the failing call. fail is fixed when the module is built:
// the remote runtimes' workers read the store from their own goroutines.
type faultStore struct {
	*inmem.Store
	fail string
}

func (s faultStore) Leases() work.LeaseStore { return faultLeases{s.Store.Leases(), s.fail} }

func (s faultStore) Settings() settings.SettingsStore {
	return faultSettings{s.Store.Settings(), s.fail}
}

func (s faultStore) RemoteCache() port.RemoteCacheStore {
	return faultRemoteCache{s.Store.RemoteCache(), s.fail}
}

type faultLeases struct {
	work.LeaseStore
	fail string
}

func (l faultLeases) Acquire(ctx context.Context, lease work.Lease, now time.Time) (bool, error) {
	if l.fail == "Leases.Acquire" {
		return false, errFault
	}
	return l.LeaseStore.Acquire(ctx, lease, now)
}

type faultSettings struct {
	settings.SettingsStore
	fail string
}

func (s faultSettings) Get(ctx context.Context, key string, into any) error {
	if s.fail == "Settings.Get" {
		return errFault
	}
	return s.SettingsStore.Get(ctx, key, into)
}

func (s faultSettings) Put(ctx context.Context, key string, v any) error {
	if s.fail == "Settings.Put" {
		return errFault
	}
	return s.SettingsStore.Put(ctx, key, v)
}

type faultRemoteCache struct {
	port.RemoteCacheStore
	fail string
}

func (c faultRemoteCache) Source(ctx context.Context, key string) (model.SourceIdentity, error) {
	if c.fail == "RemoteCache.Source" {
		return model.SourceIdentity{}, errFault
	}
	return c.RemoteCacheStore.Source(ctx, key)
}

// RegisterWork returns the item service's error unwrapped, as its callers
// are told, and stops at it: a start that has already failed writes no
// cache maintenance schedule. The failure here is a zero snapshot cleanup
// interval, which reaches the queue because New makes both cleanups
// periodic whatever Settings hold, and the queue refuses it.
func TestUnitRegisterWorkStopsAtTheItemServiceError(t *testing.T) {
	ctx := t.Context()
	register := func(s Settings) (*inmem.Store, error) {
		st := inmem.New()
		q, err := work.New(st, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		m, err := New(Deps{Store: st, Work: q, Settings: s, Log: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		return st, m.RegisterWork(ctx)
	}
	scheduled := func(st *inmem.Store) bool {
		_, err := st.WorkQueue().Get(ctx, item.KindCacheCleanupSchedule, "scan")
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}

	// The control: with both intervals set, the schedule is written, so its
	// absence below comes from the failure.
	st, err := register(Settings{
		MavenSnapshotCleanupInterval: 5 * time.Minute,
		NuGetSnapshotCleanupInterval: 5 * time.Minute,
	})
	if err != nil || !scheduled(st) {
		t.Fatalf("RegisterWork with both intervals = %v, cache scan scheduled %v; want nil, true",
			err, scheduled(st))
	}

	st, err = register(Settings{})
	if !errors.Is(err, work.ErrOptions) || !strings.HasPrefix(err.Error(), "item: register ") {
		t.Errorf("RegisterWork with zero intervals = %v, want the item service's unwrapped "+
			"\"item: register\" error over work.ErrOptions", err)
	}
	if scheduled(st) {
		t.Error("RegisterWork scheduled cache maintenance after the item service's error")
	}
}

// assertNothingOpened fails t when m holds a remote runtime or read service.
// The failures it follows all come before the first one opens, so none may
// be left for a route to read through or for Close to stop.
func assertNothingOpened(t *testing.T, m *Module) {
	t.Helper()
	if m.RemoteSources() != nil || m.RegistrySources() != nil || m.MavenEager() != nil ||
		m.smartRemote != nil || m.smartRegistry != nil {
		t.Error("the failed OpenRemoteSources left a remote runtime or read service open")
	}
}

// Without a master key the remote policy key lives in the store, and every
// remote cache identity derives from it. A start that cannot read, lock or
// persist it must fail before any runtime starts, and leave the key and the
// lease as it found them: a key written by a failed start, or one minted
// over a key that could not be read, would change every cache identity, and
// a lease left held would refuse the next start for a minute.
func TestUnitOpenRemoteSourcesPolicyKeyFailures(t *testing.T) {
	stored := bytes.Repeat([]byte{9}, 32)
	for _, tc := range []struct {
		name string
		fail string
		// prepare leaves the store as the failing start finds it.
		prepare func(*testing.T, *inmem.Store)
		want    error
		// text is the whole error, where the module wraps the cause.
		text string
	}{
		{
			name: "lease store fails",
			fail: "Leases.Acquire",
			want: errFault,
			text: "remote policy key: lock remote key: injected store fault",
		},
		{
			name: "another node holds the lease",
			prepare: func(t *testing.T, st *inmem.Store) {
				now := time.Now().UTC()
				held, err := st.Leases().Acquire(t.Context(), work.Lease{
					Name: remotePolicyKeySetting, Holder: "another-node",
					Acquired: now, Expires: now.Add(time.Hour),
				}, now)
				if err != nil || !held {
					t.Fatalf("another node's lease: held %v, %v", held, err)
				}
			},
			want: source.ErrUnavailable,
			text: "remote policy key: source: source unavailable",
		},
		{
			// A read error is not a missing key: minting one here would
			// replace the stored key.
			name: "stored key unreadable",
			fail: "Settings.Get",
			prepare: func(t *testing.T, st *inmem.Store) {
				if err := st.Settings().Put(t.Context(), remotePolicyKeySetting, stored); err != nil {
					t.Fatal(err)
				}
			},
			want: errFault,
		},
		{
			name: "new key not persisted",
			fail: "Settings.Put",
			want: errFault,
			text: "remote policy key: persist remote key: injected store fault",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			inner := inmem.New()
			if tc.prepare != nil {
				tc.prepare(t, inner)
			}
			key, leases := storedPolicyKey(t, inner), allLeases(t, inner)
			m, err := New(Deps{
				Store: faultStore{Store: inner, fail: tc.fail},
				Log:   slog.New(slog.DiscardHandler),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(m.Close)

			err = m.OpenRemoteSources(ctx)
			if !errors.Is(err, tc.want) || tc.text != "" && err.Error() != tc.text {
				t.Errorf("OpenRemoteSources = %v, want %v (text %q)", err, tc.want, tc.text)
			}
			assertNothingOpened(t, m)
			if got := storedPolicyKey(t, inner); !bytes.Equal(got, key) {
				t.Errorf("stored policy key after the failed start = %x, want %x", got, key)
			}
			if got := allLeases(t, inner); !slices.Equal(got, leases) {
				t.Errorf("leases after the failed start = %+v, want %+v", got, leases)
			}
		})
	}
}

// storedPolicyKey returns the remote policy key st holds, nil when none.
func storedPolicyKey(t *testing.T, st *inmem.Store) []byte {
	t.Helper()
	var key []byte
	err := st.Settings().Get(t.Context(), remotePolicyKeySetting, &key)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// allLeases returns every lease st holds.
func allLeases(t *testing.T, st *inmem.Store) []work.Lease {
	t.Helper()
	leases, err := st.Leases().List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return leases
}

// remote.allowed_private_origins names origins, so one the resolver cannot
// parse is a configuration error, refused when the remote sources open
// rather than on each remote read that would consult it.
func TestUnitOpenRemoteSourcesRefusesABadPrivateOrigin(t *testing.T) {
	m, err := New(Deps{
		Store:    inmem.New(),
		Settings: Settings{AllowedPrivateOrigins: []string{"ftp://mirror.internal"}},
		Log:      slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	err = m.OpenRemoteSources(t.Context())
	if !errors.Is(err, remote.ErrConfiguration) ||
		err.Error() != "remote resolver: remote: invalid transport configuration" {
		t.Errorf("OpenRemoteSources = %v, want the resolver's ErrConfiguration", err)
	}
	assertNothingOpened(t, m)
}

// remoteFixture is a module with its remote sources open and one generic
// remote repository stored.
type remoteFixture struct {
	m    *Module
	repo repo.Config
	// id is the repository's source identity, which a permit for it holds.
	id model.SourceIdentity
}

// newRemoteFixture builds the module over a faultStore failing the call that
// fail names, if any, and opens its remote sources. Its access binding finds
// every principal unchanged since it was verified, and evaluates "admin" as
// an administrator; evaluating anyone else fails, as it does when the
// permissions cannot be read.
func newRemoteFixture(t *testing.T, fail string) remoteFixture {
	t.Helper()
	ctx := t.Context()
	inner := inmem.New()
	m, err := New(Deps{
		Store: faultStore{Store: inner, fail: fail},
		Access: accessapi.API{
			Authenticator: &accessapitest.Authenticator{},
			Authorizer: &accessapitest.Authorizer{
				DecideFunc: func(
					_ context.Context,
					s accessapi.Subject,
					_ bool,
				) (accessapi.Decider, error) {
					if s.Name != "admin" {
						return nil, errFault
					}
					return &accessapitest.Decider{Admin: true}, nil
				},
			},
		},
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if err := m.OpenRemoteSources(ctx); err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.Generic,
		URL: "https://repo.example.com/files/",
	})
	if err := inner.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	id, err := inner.RemoteCache().Source(ctx, c.Key)
	if err != nil {
		t.Fatal(err)
	}
	return remoteFixture{m: m, repo: c, id: id}
}

// Resolve gives the source identity of the configuration a read started
// with, so the read can be fenced against a change made while it runs. A
// repository deleted since, or one whose remote policy changed, supersedes
// the read; an edit only to its description does not.
func TestUnitRemoteResolveSupersedesAChangedRepository(t *testing.T) {
	f := newRemoteFixture(t, "")
	ctx := t.Context()
	described := f.repo
	described.Description = "a cosmetic edit"
	if id, err := f.m.remoteSources.Resolve(ctx, described); err != nil || id != f.id {
		t.Errorf("Resolve after a description edit = %+v, %v; want %+v", id, err, f.id)
	}
	moved := f.repo
	moved.URL = "https://mirror.example.com/files/"
	for name, c := range map[string]repo.Config{
		"deleted":        {Key: "deleted-remote", Class: repo.Remote},
		"policy changed": moved,
	} {
		if _, err := f.m.remoteSources.Resolve(ctx, c); !errors.Is(err, source.ErrSuperseded) {
			t.Errorf("Resolve of a %s repository = %v, want ErrSuperseded", name, err)
		}
	}
}

// A source identity the store cannot read is not a source that is unchanged:
// both Resolve and the recheck before delivery fail with the store's error,
// which the source boundary turns into its own safe error.
func TestUnitRemoteSourceIdentityFailure(t *testing.T) {
	f := newRemoteFixture(t, "RemoteCache.Source")
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin"})
	if _, err := f.m.remoteSources.Resolve(ctx, f.repo); !errors.Is(err, errFault) ||
		err.Error() != "remote identity: injected store fault" {
		t.Errorf("Resolve = %v, want the store's error under \"remote identity\"", err)
	}
	q := source.Request{Repo: f.repo, Resource: source.Resource{Path: "a/b.txt"}}
	err := f.m.remoteSources.Revalidate(ctx, q, source.Permit{Identity: f.id})
	if !errors.Is(err, errFault) {
		t.Errorf("Revalidate = %v, want the store's error", err)
	}
}

// Revalidate runs before a lookup and again before delivery, so a read
// that no longer stands stops there: its repository deleted, its source
// replaced since the permit was issued, no caller to read for, or a caller
// whose permissions cannot be evaluated now.
func TestUnitRemoteRevalidateRechecksTheRead(t *testing.T) {
	f := newRemoteFixture(t, "")
	ctx := t.Context()
	admin := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin"})
	q := source.Request{Repo: f.repo, Resource: source.Resource{Path: "a/b.txt"}}
	permit := source.Permit{Identity: f.id}
	// The control: each case below changes one thing from this read.
	if err := f.m.remoteSources.Revalidate(admin, q, permit); err != nil {
		t.Fatalf("Revalidate of a current read = %v, want nil", err)
	}
	replaced := f.id
	replaced.Generation += "-replaced"
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		q      source.Request
		permit source.Permit
		want   error
	}{
		{
			name:   "repository deleted",
			ctx:    admin,
			q:      source.Request{Repo: repo.Config{Key: "deleted-remote", Class: repo.Remote}},
			permit: permit,
			want:   source.ErrSuperseded,
		},
		{
			name:   "source replaced since the permit",
			ctx:    admin,
			q:      q,
			permit: source.Permit{Identity: replaced},
			want:   source.ErrSuperseded,
		},
		{name: "no caller", ctx: ctx, q: q, permit: permit, want: source.ErrDenied},
		{
			name:   "permissions cannot be evaluated",
			ctx:    kernel.WithPrincipal(ctx, kernel.Principal{Name: "ann"}),
			q:      q,
			permit: permit,
			want:   source.ErrUnavailable,
		},
	} {
		if err := f.m.remoteSources.Revalidate(tc.ctx, tc.q, tc.permit); !errors.Is(err, tc.want) {
			t.Errorf("%s: Revalidate = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// CheckCurrent is the last fence before a fetched copy is stored: the read
// must still stand, and the remote's credential revision must be the one
// the copy was fetched under, or a copy fetched with credentials since
// replaced would be kept as current. Both policies hold the fence.
func TestUnitRemoteCheckCurrentFencesTheRevision(t *testing.T) {
	f := newRemoteFixture(t, "")
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin"})
	q := source.Request{Repo: f.repo, Resource: source.Resource{Path: "a/b.txt"}}
	permit := source.Permit{Identity: f.id}
	for name, p := range map[string]*remote.Policy{
		"remote":   f.m.remoteSources.HTTP.(*maven.MavenBackend).Policy,
		"registry": f.m.registrySources.HTTP.(*docker.RegistryContent).Policy,
	} {
		opts, err := p.Options(ctx, f.repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.CheckCurrent(ctx, nil, q, permit, opts.Revision); err != nil {
			t.Errorf("%s policy: CheckCurrent at the current revision = %v, want nil", name, err)
		}
		err = p.CheckCurrent(ctx, nil, q, permit, "an-earlier-revision")
		if !errors.Is(err, source.ErrSuperseded) {
			t.Errorf("%s policy: CheckCurrent at an earlier revision = %v, want ErrSuperseded",
				name, err)
		}
	}
}
