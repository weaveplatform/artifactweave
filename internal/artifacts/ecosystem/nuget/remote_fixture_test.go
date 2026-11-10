package nuget

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
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

func ptr[T any](v T) *T { return &v }
