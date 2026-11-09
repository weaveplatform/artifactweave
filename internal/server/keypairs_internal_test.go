package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestAccPublicKeyFile: the file name is the alias, the pair's type and pub,
// and the header quotes an alias that is not a plain token (#473).
func TestAccPublicKeyFile(t *testing.T) {
	for k, want := range map[types.Keypair]string{
		{Alias: "alpine", Type: types.KeypairRSA}: "alpine.rsa.pub",
		{Alias: "debian", Type: types.KeypairGPG}: "debian.gpg.pub",
	} {
		if got := publicKeyFile(k); got != want {
			t.Errorf("publicKeyFile(%+v) = %q, want %q", k, got, want)
		}
	}
	h := statusFixture(t, false)
	admin := jsonAs(basic("admin", "pw"))
	mustStatus(t, do(t, h, http.MethodPost, "/artifactory/api/security/keypair", admin,
		keypairBody("kp-spaced", "my key", testKeys().public)), http.StatusCreated, "create")
	mustStatus(t, do(t, h, http.MethodPut, "/artifactory/api/repositories/spaced", admin,
		`{"rclass":"local","packageType":"alpine","primaryKeyPairRef":"kp-spaced"}`),
		http.StatusOK, "repository")
	rec := do(t, h, http.MethodGet,
		"/artifactory/api/security/keypair/public/repositories/spaced", admin, "")
	if got := rec.Header().Get("Content-Disposition"); rec.Code != http.StatusOK ||
		got != `attachment; filename="my key.rsa.pub"` {
		t.Fatalf("a spaced alias: %d %q", rec.Code, got)
	}
}

// vanishingKeys is a key store whose pair someone else deletes between the
// create that finds it and the update that would replace it.
type vanishingKeys struct {
	store.KeyStore
	creates   int
	updateErr error
}

func (v *vanishingKeys) CreateKeypair(context.Context, types.Keypair) error {
	v.creates++
	if v.creates == 1 {
		return store.ErrConflict
	}
	return nil
}

func (v *vanishingKeys) UpdateKeypair(context.Context, types.Keypair) error {
	return v.updateErr
}

func TestAccPutKeypairRaces(t *testing.T) {
	ctx := context.Background()
	gone := &vanishingKeys{updateErr: store.ErrNotFound}
	if err := putKeypair(ctx, gone, types.Keypair{Name: "kp"}); err != nil || gone.creates != 2 {
		t.Fatalf("a pair deleted before its update: %v after %d creates", err, gone.creates)
	}
	errDown := errors.New("store down")
	broken := &vanishingKeys{updateErr: errDown}
	if err := putKeypair(ctx, broken, types.Keypair{Name: "kp"}); !errors.Is(err, errDown) {
		t.Fatalf("a failed update: %v", err)
	}
}

// UsingKeypair fails under "usingKeypair".
func (f faultyRepos) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	if f.fail["usingKeypair"] {
		return nil, errBoom
	}
	return f.RepoStore.UsingKeypair(ctx, name) //nolint:wrapcheck // test double
}

// repoStoreRepositories serves the Repositories methods that the key pair
// code calls from a repository store, so that a test can fail any one of
// them. The other methods are the nil interface's, which panic if called.
type repoStoreRepositories struct {
	artifactsapi.Repositories
	repos store.RepoStore
}

func (r repoStoreRepositories) Get(ctx context.Context, key string) (repo.Config, error) {
	return r.repos.Get(ctx, key) //nolint:wrapcheck // test double
}

func (r repoStoreRepositories) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	return r.repos.UsingKeypair(ctx, name) //nolint:wrapcheck // test double
}

func (r repoStoreRepositories) Update(ctx context.Context, c repo.Config) error {
	return r.repos.Update(ctx, c) //nolint:wrapcheck // test double
}

// TestAccDropKeypairRefs clears a pair from the primary and the secondary
// references that name it, leaves other pairs alone, and passes on a
// failure at any step, which the delete answers 500 and undoes (#473).
func TestAccDropKeypairRefs(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "both", Class: repo.Local, PrimaryKeyPairRef: "kp", SecondaryKeyPairRef: "kp"},
		{Key: "second", Class: repo.Local, PrimaryKeyPairRef: "other", SecondaryKeyPairRef: "kp"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, fail := range []string{"usingKeypair", "get", "update"} {
		broken := faultyRepos{RepoStore: db.Repos(), fail: map[string]bool{fail: true}}
		if err := dropKeypairRefs(
			ctx,
			repoStoreRepositories{repos: broken},
			"kp",
		); !errors.Is(err, errBoom) {
			t.Errorf("%s fails: %v", fail, err)
		}
	}
	h := statusFixture(t, false, func(c *Config) {
		c.Store = faultyStore{Store: c.Store.(*inmem.Store), fail: map[string]bool{"update": true}}
	})
	mustStatus(t, do(t, h, http.MethodDelete, "/artifactory/api/security/keypair/kp-used",
		basic("admin", "pw"), ""), http.StatusInternalServerError, "a failed repository update")
	mustStatus(t, do(t, h, http.MethodGet, "/artifactory/api/security/keypair/kp-used",
		basic("admin", "pw"), ""), http.StatusOK, "the pair after the failed delete")
	if err := dropKeypairRefs(ctx, repoStoreRepositories{repos: db.Repos()}, "kp"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][2]string{"both": {"", ""}, "second": {"other", ""}} {
		c, err := db.Repos().Get(ctx, key)
		if err != nil || c.PrimaryKeyPairRef != want[0] || c.SecondaryKeyPairRef != want[1] {
			t.Errorf("%s: %q %q, %v", key, c.PrimaryKeyPairRef, c.SecondaryKeyPairRef, err)
		}
	}
}
