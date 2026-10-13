package repos

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The repositories API's tests in internal/server cover the ordinary paths;
// these cover the failures a store can only produce on purpose.

func TestMergeRefusesMalformedUpdates(t *testing.T) {
	stored := *repo.Defaults(&repo.Config{Key: "k", Class: repo.Local})
	for name, patch := range map[string]string{
		"key not a string":     `{"key": 5}`,
		"field of wrong shape": `{"includesPattern": 5}`,
	} {
		var p map[string]json.RawMessage
		if err := json.Unmarshal([]byte(patch), &p); err != nil {
			t.Fatal(err)
		}
		if _, err := Merge(stored, p); err == nil {
			t.Errorf("%s: merged", name)
		}
	}
}

var errBoom = errors.New("boom")

// faulty fails the operation named in fail.
type faulty struct {
	*inmem.Store
	fail string
}

func (f faulty) Items() store.ItemStore       { return faultyItems{f.Store.Items(), f.fail} }
func (f faulty) Blobs() store.BlobIndex       { return faultyBlobs{f.Store.Blobs(), f.fail} }
func (f faulty) Packages() store.PackageStore { return faultyPackages{f.Store.Packages(), f.fail} }
func (f faulty) Repos() store.RepoStore       { return faultyRepos{f.Store.Repos(), f.fail} }

type faultyItems struct {
	store.ItemStore
	fail string
}

func (f faultyItems) Delete(
	ctx context.Context,
	repoKey string,
	cached bool,
	path string,
) (store.DeleteResult, error) {
	if f.fail == "items" {
		return store.DeleteResult{}, errBoom
	}
	return f.ItemStore.Delete(ctx, repoKey, cached, path) //nolint:wrapcheck // test double
}

type faultyBlobs struct {
	store.BlobIndex
	fail string
}

func (f faultyBlobs) Unref(ctx context.Context, sha string, n int64) (types.Blob, error) {
	if f.fail == "unref" {
		return types.Blob{}, errBoom
	}
	return f.BlobIndex.Unref(ctx, sha, n) //nolint:wrapcheck // test double
}

type faultyPackages struct {
	store.PackageStore
	fail string
}

func (f faultyPackages) List(
	ctx context.Context,
	repoKey string,
	p store.Page,
) ([]types.Package, string, error) {
	if f.fail == "list packages" {
		return nil, "", errBoom
	}
	return f.PackageStore.List(ctx, repoKey, p) //nolint:wrapcheck // test double
}

func (f faultyPackages) Delete(ctx context.Context, id int64) error {
	if f.fail == "delete package" {
		return errBoom
	}
	return f.PackageStore.Delete(ctx, id) //nolint:wrapcheck // test double
}

type faultyRepos struct {
	store.RepoStore
	fail string
}

func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail == "get virtual" && key == "virt" {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

func (f faultyRepos) Update(ctx context.Context, c repo.Config) error {
	if f.fail == "update virtual" {
		return errBoom
	}
	return f.RepoStore.Update(ctx, c) //nolint:wrapcheck // test double
}

// world is a local repository with an item, its blob reference and a
// package, listed by a virtual repository.
func world(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "libs", Class: repo.Local},
		{Key: "virt", Class: repo.Virtual, Repositories: []string{"libs"}, DefaultDeploymentRepo: "libs"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	sha := strings.Repeat("ab", 32)
	if _, err := db.Blobs().
		Ref(ctx, types.Blob{Checksums: types.Checksums{SHA256: sha}, Size: 3}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Items().Put(ctx, types.Item{
		RepoKey: "libs", Name: "f.txt", Size: 3, Actual: types.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Packages().
		Ensure(ctx, types.Package{RepoKey: "libs", Type: repo.Generic, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	files, found, err := Remove(ctx, db, "libs")
	if err != nil || !found || files != 1 {
		t.Fatalf("Remove = %d, %v, %v", files, found, err)
	}
	if v, _ := db.Repos().
		Get(ctx, "virt"); len(v.Repositories) != 0 ||
		v.DefaultDeploymentRepo != "" {
		t.Fatalf("virtual still lists the deleted repository: %+v", v)
	}
	if _, found, err := Remove(ctx, db, "libs"); found || err != nil {
		t.Fatalf("second Remove = %v, %v", found, err)
	}
}

func TestRemoveFailures(t *testing.T) {
	for _, fail := range []string{
		"items", "unref", "list packages", "delete package", "get virtual", "update virtual",
	} {
		db := world(t)
		if _, _, err := Remove(
			context.Background(),
			faulty{db, fail},
			"libs",
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("%s: %v", fail, err)
		}
		// The transaction rolled back: the repository is still there.
		if _, err := db.Repos().Get(context.Background(), "libs"); err != nil {
			t.Errorf("%s: the failed delete was not rolled back: %v", fail, err)
		}
	}
}

// failingKeys is a key store whose reads fail.
type failingKeys struct{ store.KeyStore }

func (failingKeys) Keypair(context.Context, string) (types.Keypair, error) {
	return types.Keypair{}, errors.New("boom")
}

func TestCheckKeypairs(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if err := db.Keys().CreateKeypair(ctx, types.Keypair{Name: "kp"}); err != nil {
		t.Fatal(err)
	}
	ok := repo.Config{Key: "r", PrimaryKeyPairRef: "kp", SecondaryKeyPairRef: "kp"}
	if err := CheckKeypairs(ctx, db.Keys(), ok); err != nil {
		t.Fatalf("existing pairs: %v", err)
	}
	if err := CheckKeypairs(ctx, db.Keys(), repo.Config{Key: "r"}); err != nil {
		t.Fatalf("no references: %v", err)
	}
	err := CheckKeypairs(ctx, db.Keys(), repo.Config{Key: "r", SecondaryKeyPairRef: "absent"})
	if err == nil || !strings.Contains(err.Error(), "Key pair 'absent' does not exist") {
		t.Fatalf("missing pair: %v", err)
	}
	if err := CheckKeypairs(ctx, failingKeys{db.Keys()}, ok); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("store failure: %v", err)
	}
}
