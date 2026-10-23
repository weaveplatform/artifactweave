package server

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const npmBase = "/artifactory/api/npm/"

// Shared root fixtures still refer to npm repositories and reproducible
// tarballs while their format-specific assertions live in test/server/npm.
var npmRepos = append(fixtureRepos("npm", repo.NPM), repo.Config{
	Key: "npm-releases", Class: repo.Local, PackageType: repo.NPM, HandleSnapshots: new(bool),
})

func npmPublishBody(name, version string) string { return support.NPMPublishBody(name, version) }
func npmTarballOf(name, version string) []byte   { return support.NPMTarballOf(name, version) }

// park is shared by root reindex tests after the npm operation suite moves.
func park(t *testing.T, st store.Store, kind, key string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	q := st.WorkQueue()
	if err := q.Enqueue(ctx, kind, key, nil, now); err != nil {
		t.Fatal(err)
	}
	it, took, err := q.LeaseKey(ctx, kind, key, store.WorkLease{
		Holder: "test", Until: now.Add(time.Minute), Now: now,
	})
	if err != nil || !took {
		t.Fatalf("lease %v %v", took, err)
	}
	if err := q.Fail(ctx, kind, key, it.Generation, store.WorkFailure{
		Error: "spent", Park: true, RetryAt: now, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}
