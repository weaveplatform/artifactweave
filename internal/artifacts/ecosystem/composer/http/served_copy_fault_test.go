package composerhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
)

// A Composer remote's package metadata reads the cached copies of the root
// document and the package's p2 document that the source service serves
// it. A failed read of either is the server's storage fault: 500 "internal error", logged once,
// where the route's read answered 502 "invalid upstream Composer metadata"
// (#1462). Once the store reads again the route answers from the kept
// copies, without fetching from the upstream.
func TestAccRemoteComposerAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	up.Set("/packages.json", fakeupstream.Resource{
		Body: []byte(`{"packages":{},"metadata-url":"/p2/%package%.json"}`),
	})
	up.Set("/p2/acme/widget.json", fakeupstream.Resource{Body: []byte(
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0",` +
			`"dist":{"type":"zip","url":"https://up.example/dist.zip","shasum":"` +
			strings.Repeat("a", 40) + `"}}]}}`,
	)})
	key := []byte(strings.Repeat("k", 32))
	policy := &remote.Policy{Key: key}
	f := newFixtureWithSource(t, &source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
			return policy.Store.RemoteCache().Source(ctx, c.Key)
		},
	})
	resolver, err := upstream.NewResolver(f.db, nil, upstream.ResolverOptions{RevisionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	pool := new(upstream.Pool)
	t.Cleanup(pool.Close)
	blobs := &blobtest.ReadFaults{Store: f.items.Blobs}
	f.items.Blobs = blobs
	policy.Store, policy.Blobs, policy.Items, policy.Pool, policy.Options = f.db, blobs, f.items, pool, resolver.Options
	c := f.create("remote", repo.Remote)
	c.URL, c.ComposerRegistryURL = up.URL, up.URL
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	const path = "/artifactory/api/composer/remote/p2/acme/widget.json"
	if r := f.request(http.MethodGet, path); r.Code != http.StatusOK {
		t.Fatalf("first read: %d %s", r.Code, r.Body)
	}
	fetches := func() int {
		n := 0
		for _, r := range up.Requests() {
			if r.Method == http.MethodGet {
				n++
			}
		}
		return n
	}
	fetched := fetches()
	failed := 0
	for spare := 0; spare < 50; spare++ {
		blobs.CutEvery(spare)
		f.faults = nil
		r := f.request(http.MethodGet, path)
		if r.Code == http.StatusOK {
			break
		}
		if r.Code != http.StatusInternalServerError ||
			r.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
			t.Fatalf("%d sound opens: %d %q", spare, r.Code, r.Body)
		}
		if len(f.faults) != 1 || !errors.Is(f.faults[0], blobtest.ErrDropped) {
			t.Fatalf("%d sound opens: faults %v", spare, f.faults)
		}
		failed++
	}
	blobs.Mend()
	if failed < 2 {
		t.Fatalf("only %d opens failed: the route's of each document must", failed)
	}
	if got := fetches(); got != fetched {
		t.Fatalf("the upstream was fetched %d times, want %d", got, fetched)
	}
}
