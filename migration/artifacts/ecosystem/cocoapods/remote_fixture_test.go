package cocoapods

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

type policyWorld struct {
	t      *testing.T
	db     *inmem.Store
	blobs  blob.Store
	clock  *fakeupstream.Clock
	up     *fakeupstream.Server
	p      *remote.Policy
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
	pool := new(upstream.Pool)
	p := &remote.Policy{
		Store: db,
		Blobs: blobs,
		Items: &item.Service{Store: db, Blobs: blobs, Now: clock.Now},
		Pool:  pool,
		Now:   clock.Now,
		Key: []byte(
			"test-only-query-identity",
		),
		Options: func(context.Context, repo.Config) (remote.Options, error) {
			return remote.Options{Revision: "credential-1"}, nil
		},
	}
	t.Cleanup(pool.Close)
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
			Intent:                 source.RetrievalIntentMayFetch,
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

func ptr[T any](v T) *T { return &v }

func resolverFor(t *testing.T, st store.Store, opts upstream.ResolverOptions) *upstream.Resolver {
	t.Helper()
	key, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	// The resolver reads a remote's client certificate only through
	// Certificates, so bind the test store's, as the application binds its
	// own; without it a remote that names one would panic. The access test
	// kit's Keys is the binding access.KeysOf gives, which a test outside
	// the access module reaches only through the kit.
	if opts.Certificates == nil {
		opts.Certificates = accesstest.Over(t, st).API().Keys
	}
	r, err := upstream.NewResolver(st, key, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func saveRepo(t *testing.T, st store.Store, c repo.Config) {
	t.Helper()
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func resolveFor(t *testing.T, r *upstream.Resolver, c repo.Config) remote.Options {
	t.Helper()
	opts, err := r.Options(t.Context(), repo.Masked(c))
	if err != nil {
		t.Fatal(err)
	}
	return opts
}
