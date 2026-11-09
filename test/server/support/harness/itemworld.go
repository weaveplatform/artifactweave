package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// ItemWorld is the production router over a seeded in-memory store and a
// file-system blob store, built by NewItemWorld for one test. Every user's
// password is "pw": admin; reader (in readers, so it reads everything);
// deployer (may deploy to libs-local but not delete); user (no rights). Do
// sends requests to H; DB and Blobs let a test seed state or check what a
// request left behind.
type ItemWorld struct {
	T     *testing.T
	H     http.Handler
	DB    *inmem.Store
	Blobs blob.Store
}

// SigningBlobs makes a blob store a blob.Signer of the routes listed in
// Routes, with the signing options readable back from the URL, as an object
// store that signs download URLs would be. Err, when set, fails every
// signature.
type SigningBlobs struct {
	blob.Store
	Routes []blob.Via
	Err    error
}

// StorageSigner returns st as SigningBlobs that sign storage URLs only, as an
// object store without a CDN does. NewItemWorld's blob store is one.
func StorageSigner(st blob.Store) SigningBlobs {
	return SigningBlobs{Store: st, Routes: []blob.Via{blob.ViaStorage}}
}

// Signs reports whether via is one of b's Routes.
func (b SigningBlobs) Signs(via blob.Via) bool { return slices.Contains(b.Routes, via) }

// SignURL returns Err when it is set. Otherwise it returns a URL on the host
// "<via>.example", such as storage.example, for the blob sha, with the TTL,
// file name and content type of o in its query, so a test can read back what
// the server asked to sign. It does not check Signs.
func (b SigningBlobs) SignURL(_ context.Context, sha string, o blob.SignOptions) (string, error) {
	if b.Err != nil {
		return "", b.Err
	}
	q := url.Values{"ttl": {o.TTL.String()}, "name": {o.Filename}, "type": {o.ContentType}}
	return "https://" + o.Via.String() + ".example/" + sha + "?" + q.Encode(), nil
}

// AnonymousRemotes names the permission NewItemWorld gives anonymous when
// anonymous access is on: read on every remote repository. The seeded Any
// Remote permission grants anonymous nothing (#383), so anonymous reads remotes
// through this one, and a test that deletes it takes those reads away.
const AnonymousRemotes = "anonymous-remotes"

// NewItemWorld builds an ItemWorld for t. The store is seeded as a new server
// seeds it, then given the users reader, deployer and user; deploy-libs, which
// lets deployer read and write libs-local; when anonymous is true, anonymous
// access and the AnonymousRemotes permission; the local repositories
// libs-local, other-local, server-local (server-generated checksums) and
// redirect-local (download redirect); the remote up; and the virtual
// repositories virt (over missing, other-local, libs-local and up, deploying
// to libs-local), loop (over itself) and virt-nodeploy (over libs-local, with
// no deployment repository). Blobs are files under t's temporary directory
// behind StorageSigner. The router's base URL is https://reg.example and it
// reports Artifactory 7.155.0; tune adjusts that configuration, in order,
// before the router is built, for tests that need another blob store or a
// logger they can read. NewItemWorld fails t when any part cannot be built.
func NewItemWorld(t *testing.T, anonymous bool, tune ...Tune) *ItemWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	support.Seed(t, db)
	hash := support.CheapHash
	for _, u := range []types.User{
		{Name: "reader", PasswordHash: hash, Groups: []string{auth.ReadersGroup}},
		{Name: "deployer", PasswordHash: hash},
		{Name: "user", PasswordHash: hash},
	} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-libs",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
		// The seeded Any Remote grants anonymous nothing (#383), so anonymous
		// reads remotes through a permission of its own, as JFrog advises.
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: AnonymousRemotes,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					kernel.AnonymousUser: types.ActionRead,
				}},
				Targets: map[string]types.Target{types.TargetAnyRemote: {Include: []string{"**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "other-local", Class: repo.Local},
		{Key: "server-local", Class: repo.Local, ChecksumPolicyType: repo.ServerGeneratedChecksums},
		{Key: "redirect-local", Class: repo.Local, DownloadRedirect: repo.Flex(true)},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
		{
			Key: "virt", Class: repo.Virtual, Repositories: []string{"missing", "other-local", "libs-local", "up"},
			DefaultDeploymentRepo: "libs-local",
		},
		{Key: "loop", Class: repo.Virtual, Repositories: []string{"loop"}},
		{Key: "virt-nodeploy", Class: repo.Virtual, Repositories: []string{"libs-local"}},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fsBlobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: support.TestHash})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: StorageSigner(fsBlobs), Auth: a,
	}
	for _, f := range tune {
		f(&cfg)
	}
	w := &ItemWorld{T: t, DB: db, Blobs: cfg.Blobs}
	w.H = Router(cfg)
	return w
}

// Do sends a request to the world's router as the user as, with the password
// "pw" over Basic, or without credentials when as is empty. headers are name
// and value pairs; a trailing name without a value is ignored.
func (w *ItemWorld) Do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.T.Helper()
	h := map[string]string{}
	if as != "" {
		h = support.Basic(as, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return support.Do(w.T, w.H, method, path, h, body)
}

// Item returns the item at the full path p in the repository key, or in that
// remote repository's cache when cached is true. It fails the test when the
// store cannot return the item.
func (w *ItemWorld) Item(key string, cached bool, p string) types.Item {
	w.T.Helper()
	it, err := w.DB.Items().Get(context.Background(), key, cached, p)
	if err != nil {
		w.T.Fatalf("item %s/%s: %v", key, p, err)
	}
	return it
}

// WantStatus fails t, printing the response body, unless rec has the status
// want.
func WantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}
