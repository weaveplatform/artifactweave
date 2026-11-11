package accesshttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// TestAccPublicKeyFile: the file name is the alias, the pair's type and pub,
// and the header quotes an alias that is not a plain token (#473).
func TestAccPublicKeyFile(t *testing.T) {
	for k, want := range map[api.Keypair]string{
		{Alias: "alpine", Type: api.KeypairRSA}: "alpine.rsa.pub",
		{Alias: "debian", Type: api.KeypairGPG}: "debian.gpg.pub",
	} {
		if got := publicKeyFile(k); got != want {
			t.Errorf("publicKeyFile(%+v) = %q, want %q", k, got, want)
		}
	}
	// The pair and the repository that signs with it are stored directly:
	// the partial is called without the router, which the create routes
	// need.
	ctx := asAdmin(context.Background())
	db := inmem.New()
	if err := db.Keys().CreateKeypair(ctx, api.Keypair{
		Name: "kp-spaced", Type: api.KeypairRSA, Alias: "my key",
		PrivateKey: "private", PublicKey: "public",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "spaced", Class: repo.Local, PackageType: repo.Alpine,
		PrimaryKeyPairRef: "kp-spaced",
	})); err != nil {
		t.Fatalf("repository: %v", err)
	}
	s := New(Deps{Store: db, Repositories: support.Repositories(db)}).Security
	res, err := s.GetKeyPairPublicKeyPerRepository(
		ctx,
		artgen.GetKeyPairPublicKeyPerRepositoryRequestObject{RepoKey: "spaced"},
	)
	if err != nil {
		t.Fatalf("a spaced alias: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := res.VisitGetKeyPairPublicKeyPerRepositoryResponse(rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Disposition"); rec.Code != http.StatusOK ||
		got != `attachment; filename="my key.rsa.pub"` {
		t.Fatalf("a spaced alias: %d %q", rec.Code, got)
	}
}

// vanishingKeys is a key store whose pair someone else deletes between the
// create that finds it and the update that would replace it.
type vanishingKeys struct {
	port.KeyStore
	creates   int
	updateErr error
}

func (v *vanishingKeys) CreateKeypair(context.Context, api.Keypair) error {
	v.creates++
	if v.creates == 1 {
		return kernel.ErrConflict
	}
	return nil
}

func (v *vanishingKeys) UpdateKeypair(context.Context, api.Keypair) error {
	return v.updateErr
}

func TestAccPutKeypairRaces(t *testing.T) {
	ctx := context.Background()
	gone := &vanishingKeys{updateErr: kernel.ErrNotFound}
	if err := putKeypair(ctx, gone, api.Keypair{Name: "kp"}); err != nil || gone.creates != 2 {
		t.Fatalf("a pair deleted before its update: %v after %d creates", err, gone.creates)
	}
	errDown := errors.New("store down")
	broken := &vanishingKeys{updateErr: errDown}
	if err := putKeypair(ctx, broken, api.Keypair{Name: "kp"}); !errors.Is(err, errDown) {
		t.Fatalf("a failed update: %v", err)
	}
}

// faultyRepos is the artifacts module's Repositories with the reads and
// writes named in fail failing with errBoom.
type faultyRepos struct {
	artifactsapi.Repositories
	fail map[string]bool
}

// Get fails under "get".
func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail["get"] {
		return repo.Config{}, errBoom
	}
	return f.Repositories.Get(ctx, key) //nolint:wrapcheck // test double
}

// Update fails under "update".
func (f faultyRepos) Update(ctx context.Context, c repo.Config) error {
	if f.fail["update"] {
		return errBoom
	}
	return f.Repositories.Update(ctx, c) //nolint:wrapcheck // test double
}

// UsingKeypair fails under "usingKeypair".
func (f faultyRepos) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	if f.fail["usingKeypair"] {
		return nil, errBoom
	}
	return f.Repositories.UsingKeypair(ctx, name) //nolint:wrapcheck // test double
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
		broken := faultyRepos{
			Repositories: support.Repositories(db),
			fail:         map[string]bool{fail: true},
		}
		if err := dropKeypairRefs(ctx, broken, "kp"); !errors.Is(err, errBoom) {
			t.Errorf("%s fails: %v", fail, err)
		}
	}
	used := usedKeypair(t)
	s := New(Deps{Store: used, Repositories: faultyRepos{
		Repositories: support.Repositories(used), fail: map[string]bool{"update": true},
	}}).Security
	admin := asAdmin(ctx)
	_, err := s.DeleteKeyPair(admin, artgen.DeleteKeyPairRequestObject{KeyPairName: "kp-used"})
	if got := errStatus(err); got != http.StatusInternalServerError {
		t.Fatalf("a failed repository update: status %d, want 500: %v", got, err)
	}
	if _, err := s.GetKeyPair(
		admin,
		artgen.GetKeyPairRequestObject{KeyPairName: "kp-used"},
	); err != nil {
		t.Fatalf("the pair after the failed delete: status %d, want 200: %v", errStatus(err), err)
	}
	if err := dropKeypairRefs(ctx, support.Repositories(db), "kp"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][2]string{"both": {"", ""}, "second": {"other", ""}} {
		c, err := db.Repos().Get(ctx, key)
		if err != nil || c.PrimaryKeyPairRef != want[0] || c.SecondaryKeyPairRef != want[1] {
			t.Errorf("%s: %q %q, %v", key, c.PrimaryKeyPairRef, c.SecondaryKeyPairRef, err)
		}
	}
}

// usedKeypair is a store holding the pair kp-used and signed-local, a
// repository that signs with it, as the root's status fixture seeds them.
func usedKeypair(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	if err := db.Keys().CreateKeypair(ctx, api.Keypair{
		Name: "kp-used", Type: api.KeypairRSA, Alias: "used",
		PrivateKey: "private", PublicKey: "public",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "signed-local", Class: repo.Local, PrimaryKeyPairRef: "kp-used",
	})); err != nil {
		t.Fatal(err)
	}
	return db
}

// keyDeleteFails is a store whose key pair delete fails with errBoom.
type keyDeleteFails struct{ *inmem.Store }

func (s keyDeleteFails) Keys() port.KeyStore { return failingKeyDelete{s.Store.Keys()} }

type failingKeyDelete struct{ port.KeyStore }

func (failingKeyDelete) DeleteKeypair(context.Context, string) error { return errBoom }

// TestUnitDeleteKeyPairIsOneTransaction makes the delete of the pair fail
// after the artifacts module's binding cleared the repository's reference
// to it, and finds both as they were: the binding takes part in the
// delete's transaction through its context, so the cleared reference rolls
// back with it.
func TestUnitDeleteKeyPairIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	db := usedKeypair(t)
	st := keyDeleteFails{db}
	s := New(Deps{Store: st, Repositories: support.Repositories(st)}).Security
	_, err := s.DeleteKeyPair(
		asAdmin(ctx),
		artgen.DeleteKeyPairRequestObject{KeyPairName: "kp-used"},
	)
	if got := errStatus(err); got != http.StatusInternalServerError {
		t.Fatalf("a failed delete: status %d, want 500: %v", got, err)
	}
	if c, err := db.Repos().
		Get(ctx, "signed-local"); err != nil ||
		c.PrimaryKeyPairRef != "kp-used" {
		t.Errorf("the reference after the failed delete: %q, %v", c.PrimaryKeyPairRef, err)
	}
	if _, err := db.Keys().Keypair(ctx, "kp-used"); err != nil {
		t.Errorf("the pair after the failed delete: %v", err)
	}
}
