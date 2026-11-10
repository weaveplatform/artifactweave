package artifactshttp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// TestAccReadsContent covers what the HTTP tests cannot reach: a file in a
// repository that no longer exists counts for no one, a readable file past
// the first page is still found, and store failures are errors, which
// answer 500 rather than a 404 that would send the client to upload.
func TestAccReadsContent(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	libs := *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})
	if err := db.Repos().Create(ctx, libs); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex("content")
	put := func(key, name string) {
		t.Helper()
		if _, err := db.Items().Put(ctx, model.Item{
			RepoKey: key, Name: name, Actual: kernel.Checksums{SHA256: sum},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for n := range kernel.MaxLimit {
		put("gone", fmt.Sprintf("f%d.bin", n))
	}
	acc, err := itemkit.AccessFor(
		ctx,
		authorizerOver(t, db),
		kernel.Principal{Name: "admin", Admin: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := readsContent(ctx, db, acc, sum, PrivateItem); ok || err != nil {
		t.Fatalf("files of a deleted repository: %v %v", ok, err)
	}
	put("libs-local", "late.bin")
	if ok, err := readsContent(ctx, db, acc, sum, PrivateItem); !ok || err != nil {
		t.Fatalf("a readable file on the second page: %v %v", ok, err)
	}
	for name, st := range map[string]Store{
		"find": buildSearchFaults{Store: db, fail: "find"},
		"repo": faultyStore{Store: db, fail: map[string]bool{"get": true}},
	} {
		if _, err := readsContent(ctx, st, acc, sum, PrivateItem); !errors.Is(err, errBoom) {
			t.Errorf("%s failing: %v", name, err)
		}
	}
}
