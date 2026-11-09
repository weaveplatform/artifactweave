package server

// This file holds the in-package router worlds, request helpers and guard
// table rows that root tests in several files share, so that a root test
// file can leave the package without taking a helper that a file staying
// behind still calls. Suites outside the package build the same worlds
// through test/server/support/harness.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

// itemWorld is a seeded store and blob store behind the router. Every user's
// password is "pw": admin; reader (in readers, so it reads everything);
// deployer (may deploy to libs-local but not delete); user (no rights).
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

// anonymousRemotes is the permission newItemWorld gives anonymous when
// anonymous access is on: read on every remote repository.
const anonymousRemotes = "anonymous-remotes"

// newItemWorld builds the world; tune adjusts the router's configuration,
// for tests that need another blob store or a logger they can read.
func newItemWorld(t *testing.T, anonymous bool, tune ...func(*Config)) *itemWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	hash := cheapHash
	for _, u := range []types.User{
		{Name: "reader", PasswordHash: hash, Groups: []string{authn.ReadersGroup}},
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
			Name: anonymousRemotes,
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
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: storageSigner(fsBlobs), Auth: a,
	}
	for _, f := range tune {
		f(&cfg)
	}
	w := &itemWorld{t: t, db: db, blobs: cfg.Blobs}
	w.h = New(cfg)
	return w
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	h := map[string]string{}
	if as != "" {
		h = basic(as, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return do(w.t, w.h, method, path, h, body)
}

func (w *itemWorld) item(key string, cached bool, p string) types.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(context.Background(), key, cached, p)
	if err != nil {
		w.t.Fatalf("item %s/%s: %v", key, p, err)
	}
	return it
}

// signingBlobs makes a blob store a signer of the routes it lists, with
// the signing options readable back from the URL. err fails every signature.
type signingBlobs struct {
	blob.Store
	routes []blob.Via
	err    error
}

// storageSigner signs storage URLs only, as an object store without a CDN.
func storageSigner(st blob.Store) signingBlobs {
	return signingBlobs{Store: st, routes: []blob.Via{blob.ViaStorage}}
}

func (b signingBlobs) Signs(via blob.Via) bool { return slices.Contains(b.routes, via) }

func (b signingBlobs) SignURL(_ context.Context, sha string, o blob.SignOptions) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	q := url.Values{"ttl": {o.TTL.String()}, "name": {o.Filename}, "type": {o.ContentType}}
	return "https://" + o.Via.String() + ".example/" + sha + "?" + q.Encode(), nil
}

// repoWorld is a seeded store behind the router: admin and "reader" (in the
// readers group, so the seeded Anything permission lets it read every
// repository) both use password "pw".
type repoWorld struct {
	t  *testing.T
	h  http.Handler
	db *inmem.Store
	// fail makes the store's repository index fail the named operations.
	fail       map[string]bool
	remoteHTTP *upstream.Pool
}

func newRepoWorld(t *testing.T) *repoWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	reader := types.User{
		Name:         "reader",
		PasswordHash: cheapHash,
		Groups:       []string{authn.ReadersGroup},
	}
	if err := db.Users().Create(ctx, reader); err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	w := &repoWorld{t: t, db: db, fail: map[string]bool{}}
	w.remoteHTTP = new(upstream.Pool)
	t.Cleanup(w.remoteHTTP.Close)
	w.h = New(Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: faultyStore{Store: db, fail: w.fail}, Auth: a,
		RemoteHTTP: w.remoteHTTP,
	})
	return w
}

func (w *repoWorld) create(cs ...repo.Config) {
	w.t.Helper()
	for _, c := range cs {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *repoWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	h := map[string]string{}
	if as != "" {
		h = basic(as, "pw")
	}
	if body != "" {
		h["Content-Type"] = "application/json"
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return do(w.t, w.h, method, path, h, body)
}

func (w *repoWorld) stored(key string) repo.Config {
	w.t.Helper()
	c, err := w.db.Repos().Get(context.Background(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// identityWorld is a seeded store (admin "pw"), a token service, and a server
// that hashes cheaply.
type identityWorld struct {
	h      http.Handler
	db     *inmem.Store
	tokens *token.Service
}

func newIdentityWorld(t *testing.T) identityWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: testHash, Tokens: tokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{CompatVersion: "7.155.0", Store: db, Auth: a, PasswordHash: testHash})
	return identityWorld{h: h, db: db, tokens: tokens}
}

// call sends body as JSON with the given credentials and decodes a JSON
// answer into out when out is not nil.
func (w identityWorld) call(
	t *testing.T,
	method, path string,
	creds map[string]string,
	body string,
	want int,
	out any,
) {
	t.Helper()
	headers := map[string]string{}
	for k, v := range creds {
		headers[k] = v
	}
	if body != "" {
		headers["Content-Type"] = "application/json"
	}
	rec := do(t, w.h, method, path, headers, body)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, rec.Body)
		}
	}
}

func (w identityWorld) bearer(t *testing.T, user string, scope string) map[string]string {
	t.Helper()
	req := token.Request{Username: user, ByAdmin: true}
	if scope != "" {
		sc, err := types.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		req.Scope = sc
	}
	issued, err := w.tokens.Issue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + issued.AccessToken}
}

var admin = basic("admin", "pw")

// authorizerOver is the access module's Authorizer over st, as New binds it
// for a Config whose Access is unset.
func authorizerOver(st store.Store) accessapi.Authorizer {
	return withAccess(Config{Store: st}).Access.Authorizer
}

func bearerAuth(token string) map[string]string {
	return support.Bearer(token)
}

func formAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d: %s", what, rec.Code, want, rec.Body)
	}
}

func ptr[T any](v T) *T { return &v }

// keyMaterial is generated once per test binary: an RSA pair, another RSA
// pair, and a self-signed client certificate bundle. Keys are never
// committed, which would also trip secret scanning.
type keyMaterial struct {
	private, public           string
	otherPrivate, otherPublic string
	bundle                    string
}

var testKeys = sync.OnceValue(func() keyMaterial {
	pub := func(k *rsa.PrivateKey) string {
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			panic(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "client.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &ec.PublicKey, ec)
	if err != nil {
		panic(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		panic(err)
	}
	priv := func(k *rsa.PrivateKey) string {
		return string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k),
		}))
	}
	return keyMaterial{
		private:      priv(k),
		public:       pub(k),
		otherPrivate: priv(other),
		otherPublic:  pub(other),
		bundle: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})) +
			string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})),
	}
})

// keypairBody is a key pair request body named name.
func keypairBody(name, alias, public string) string {
	k := testKeys()
	b, err := json.Marshal(map[string]string{
		"pairName": name, "pairType": "RSA", "alias": alias,
		"privateKey": k.private, "publicKey": public,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// seedKeys adds what the key pair and certificate rows need to a status
// fixture: a pair a repository signs with and a certificate a remote
// repository presents, neither of which can be deleted.
func seedKeys(t *testing.T, db store.Store) {
	t.Helper()
	ctx := context.Background()
	k := testKeys()
	if err := db.Keys().CreateKeypair(ctx, types.Keypair{
		Name: "kp-used", Type: types.KeypairRSA, Alias: "used",
		PrivateKey: k.private, PublicKey: k.public,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Keys().PutCertificate(ctx, types.Certificate{
		Alias: "cert-used", PEM: k.bundle,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "signed-local", Class: repo.Local, PrimaryKeyPairRef: "kp-used"},
		{Key: "remote-tls", Class: repo.Remote, URL: "https://up.example", ClientTLSCertificate: "cert-used"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
}

// keypairStatusRows covers the key pair and certificate routes. Rows run
// in order against one fixture, so the create rows feed the later ones.
func keypairStatusRows() []statusCase {
	const (
		list    = "GET /artifactory/api/security/keypair"
		create  = "POST /artifactory/api/security/keypair"
		update  = "PUT /artifactory/api/security/keypair"
		verify  = "POST /artifactory/api/security/keypair/verify"
		get     = "GET /artifactory/api/security/keypair/{keyPairName}"
		del     = "DELETE /artifactory/api/security/keypair/{keyPairName}"
		public  = "GET /artifactory/api/security/keypair/public/repositories/{repoKey}"
		certs   = "GET /artifactory/api/system/security/certificates"
		addCert = "POST /artifactory/api/system/security/certificates/{certificateAlias}"
		delCert = "DELETE /artifactory/api/system/security/certificates/{certificateAlias}"
		kp      = "/artifactory/api/security/keypair"
		pk      = "/artifactory/api/security/keypair/public/repositories/"
		cert    = "/artifactory/api/system/security/certificates"
	)
	k := testKeys()
	good, other := keypairBody(
		"kp-new",
		"new",
		k.public,
	), keypairBody(
		"kp-new",
		"new",
		k.otherPublic,
	)
	return []statusCase{
		{list, kp, "admin", "", 200},
		{list, kp, "user", "", 403},
		{list, kp, "", "", 401},
		{list, kp, "anonymous", "", 401},

		{create, kp, "admin", good, 201},
		// The Terraform provider posts to the collection with a trailing slash.
		{create, kp + "/", "admin", keypairBody("kp-slash", "slash", k.public), 201},
		// A name in use is replaced, not refused (createkeypair).
		{create, kp, "admin", keypairBody("kp-slash", "again", k.public), 201},
		{create, kp, "admin", keypairBody("kp-bad", "x", k.otherPublic), 400},
		{create, kp, "admin", `{"pairName":"kp-empty"}`, 400},
		{create, kp, "user", good, 403},
		{create, kp, "", good, 401},

		{update, kp, "admin", keypairBody("kp-new", "renamed", k.public), 200},
		{update, kp, "admin", keypairBody("kp-absent", "x", k.public), 404},
		{update, kp, "admin", other, 400},
		{update, kp, "user", good, 403},

		{verify, kp + "/verify", "admin", good, 200},
		{verify, kp + "/verify", "admin", other, 400},
		{verify, kp + "/verify", "user", good, 403},

		// Any authenticated caller reads one pair, and anonymous while
		// anonymous access is on (getkeypair).
		{get, kp + "/kp-new", "admin", "", 200},
		{get, kp + "/kp-absent", "admin", "", 404},
		{get, kp + "/kp-new", "user", "", 200},
		{get, kp + "/kp-absent", "user", "", 404},
		{get, kp + "/kp-used", "anonymous", "", 200},
		{get, kp + "/kp-used", "", "", 401},

		// No read permission on the repository is needed
		// (getkeypairpublickeyperrepository).
		{public, pk + "signed-local", "admin", "", 200},
		{public, pk + "libs-local", "admin", "", 404},
		{public, pk + "missing", "admin", "", 404},
		{public, pk + "signed-local", "user", "", 200},
		{public, pk + "missing", "user", "", 404},
		{public, pk + "signed-local", "", "", 401},
		{public, pk + "signed-local", "anonymous", "", 200},

		// A pair in use is deleted all the same, and its repository then has
		// no pair (#473).
		{del, kp + "/kp-used", "admin", "", 200},
		{public, pk + "signed-local", "admin", "", 404},
		{del, kp + "/kp-new", "user", "", 403},
		{del, kp + "/kp-new", "admin", "", 200},
		{del, kp + "/kp-new", "admin", "", 404},

		{certs, cert, "admin", "", 200},
		{certs, cert, "user", "", 403},
		{certs, cert, "", "", 401},

		{addCert, cert + "/cert-new", "admin", k.bundle, 200},
		{addCert, cert + "/cert-new", "admin", "not a bundle", 400},
		{addCert, cert + "/cert-new", "user", k.bundle, 403},
		{addCert, cert + "/cert-new", "", k.bundle, 401},

		{delCert, cert + "/cert-used", "admin", "", 400},
		{delCert, cert + "/cert-new", "user", "", 403},
		{delCert, cert + "/cert-new", "admin", "", 200},
		{delCert, cert + "/cert-new", "admin", "", 404},
	}
}

// roleStatusRows covers global environments and roles. Rows run in order
// against one fixture: QA is created, renamed to STAGE and deleted; ci is
// created, replaced and deleted.
func roleStatusRows() []statusCase {
	const (
		envs    = "GET /access/api/v1/environments"
		envNew  = "POST /access/api/v1/environments"
		envMove = "POST /access/api/v1/environments/{environmentName}/rename"
		envDel  = "DELETE /access/api/v1/environments/{environmentName}"
		roles   = "GET /access/api/v1/roles"
		roleNew = "POST /access/api/v1/roles"
		roleGet = "GET /access/api/v1/roles/{role}"
		rolePut = "PUT /access/api/v1/roles/{role}"
		roleDel = "DELETE /access/api/v1/roles/{role}"
		envPath = "/access/api/v1/environments"
		rolPath = "/access/api/v1/roles"
		ci      = `{"name":"ci","type":"CUSTOM_GLOBAL","actions":["READ_REPOSITORY"],"environments":["dev"]}`
	)
	return []statusCase{
		{envs, envPath, "admin", "", 200},
		{envs, envPath, "user", "", 403},
		{envs, envPath, "anonymous", "", 401},
		{envs, envPath, "", "", 401},

		{envNew, envPath, "admin", `{"name":"QA"}`, 201},
		{envNew, envPath, "admin", `{"name":"qa"}`, 409},
		{envNew, envPath, "admin", `{"name":"Dev"}`, 409},
		{envNew, envPath, "admin", `{"name":"no spaces"}`, 400},
		{envNew, envPath, "user", `{"name":"X"}`, 403},
		{envNew, envPath, "", `{"name":"X"}`, 401},

		{envMove, envPath + "/qa/rename", "admin", `{"new_name":"STAGE"}`, 200},
		{envMove, envPath + "/DEV/rename", "admin", `{"new_name":"DEVELOP"}`, 400},
		{envMove, envPath + "/STAGE/rename", "admin", `{"new_name":"prod"}`, 400},
		{envMove, envPath + "/STAGE/rename", "admin", `{"new_name":"bad name"}`, 400},
		{envMove, envPath + "/MISSING/rename", "admin", `{"new_name":"Y"}`, 404},
		{envMove, envPath + "/STAGE/rename", "user", `{"new_name":"Y"}`, 403},
		{envMove, envPath + "/STAGE/rename", "", `{"new_name":"Y"}`, 401},

		{roles, rolPath, "admin", "", 200},
		{roles, rolPath, "user", "", 403},
		{roles, rolPath, "", "", 401},

		{roleNew, rolPath, "admin", ci, 201},
		{roleNew, rolPath, "admin", ci, 409},
		{roleNew, rolPath, "admin", `{"name":"Viewer","type":"CUSTOM_GLOBAL"}`, 409},
		{roleNew, rolPath, "admin", `{"name":"x","type":"CUSTOM_GLOBAL","actions":["FLY"]}`, 400},
		{
			roleNew,
			rolPath,
			"admin",
			`{"name":"x","type":"CUSTOM_GLOBAL","environments":["NOPE"]}`,
			400,
		},
		{roleNew, rolPath, "admin", `{"name":"x","type":"ADMIN"}`, 400},
		{roleNew, rolPath, "user", ci, 403},
		{roleNew, rolPath, "", ci, 401},

		{roleGet, rolPath + "/ci", "admin", "", 200},
		{roleGet, rolPath + "/Project%20Admin", "admin", "", 200},
		{roleGet, rolPath + "/missing", "admin", "", 404},
		{roleGet, rolPath + "/ci", "user", "", 403},
		{roleGet, rolPath + "/ci", "", "", 401},

		{
			rolePut,
			rolPath + "/ci",
			"admin",
			`{"name":"ci","type":"CUSTOM_GLOBAL","actions":["READ_BUILD"]}`,
			200,
		},
		{rolePut, rolPath + "/ci", "admin", `{"name":"other","type":"CUSTOM_GLOBAL"}`, 400},
		{
			rolePut,
			rolPath + "/Viewer",
			"admin",
			`{"name":"Viewer","type":"PREDEFINED","actions":["READ_BUILD"]}`,
			200,
		},
		{rolePut, rolPath + "/Viewer", "admin", `{"name":"Viewer","type":"CUSTOM_GLOBAL"}`, 400},
		{rolePut, rolPath + "/Viewer", "admin", `{"name":"Watcher","type":"PREDEFINED"}`, 400},
		{
			rolePut,
			rolPath + "/Project%20Admin",
			"admin",
			`{"name":"Project Admin","type":"ADMIN"}`,
			400,
		},
		{rolePut, rolPath + "/missing", "admin", `{"name":"missing","type":"CUSTOM_GLOBAL"}`, 404},
		{rolePut, rolPath + "/ci", "user", `{"name":"ci","type":"CUSTOM_GLOBAL"}`, 403},
		{rolePut, rolPath + "/ci", "", `{"name":"ci","type":"CUSTOM_GLOBAL"}`, 401},

		{roleDel, rolPath + "/Viewer", "admin", "", 400},
		{roleDel, rolPath + "/ci", "user", "", 403},
		{roleDel, rolPath + "/ci", "", "", 401},
		{roleDel, rolPath + "/ci", "admin", "", 204},
		{roleDel, rolPath + "/ci", "admin", "", 404},

		{envDel, envPath + "/PROD", "admin", "", 400},
		{envDel, envPath + "/STAGE", "user", "", 403},
		{envDel, envPath + "/STAGE", "", "", 401},
		{envDel, envPath + "/stage", "admin", "", 204},
		{envDel, envPath + "/STAGE", "admin", "", 404},
	}
}

const (
	secUsers    = "/artifactory/api/security/users"
	secGroups   = "/artifactory/api/security/groups"
	secLockPol  = "/artifactory/api/security/userLockPolicy"
	secExpiry   = "/artifactory/api/security/configuration/passwordExpirationPolicy"
	secExpire   = "/artifactory/api/security/users/authorization/expirePassword"
	secUnexpire = "/artifactory/api/security/users/authorization/unexpirePassword/"
	secChangePw = "/artifactory/api/security/users/authorization/changePassword"
	secAPIKey   = "/artifactory/api/security/apiKey"
)

// securityStatusRows covers the legacy security API. The rows create the
// principals they change (sec-user, del-user, sec-group) so the shared
// fixture's admin and user keep working for every other row.
func securityStatusRows() []statusCase {
	const (
		users     = "GET " + secUsers
		user      = "GET " + secUsers + "/{userName}"
		putUser   = "PUT " + secUsers + "/{userName}"
		postUser  = "POST " + secUsers + "/{userName}"
		delUser   = "DELETE " + secUsers + "/{userName}"
		groups    = "GET " + secGroups
		group     = "GET " + secGroups + "/{groupName}"
		putGroup  = "PUT " + secGroups + "/{groupName}"
		postGroup = "POST " + secGroups + "/{groupName}"
		delGroup  = "DELETE " + secGroups + "/{groupName}"
		locked    = "GET /artifactory/api/security/lockedUsers"
		unlockAll = "POST /artifactory/api/security/unlockAllUsers"
		unlock    = "POST /artifactory/api/security/unlockUsers"
		unlockOne = "POST /artifactory/api/security/unlockUsers/{userName}"
		getLock   = "GET " + secLockPol
		putLock   = "PUT " + secLockPol
		getExpiry = "GET " + secExpiry
		putExpiry = "PUT " + secExpiry
		expire    = "POST " + secExpire
		unexpire  = "POST " + secUnexpire + "{userName}"
		expireOne = "POST " + secExpire + "/{userName}"
		expireAll = "POST " + secExpire + "ForAllUsers"
		changePw  = "POST " + secChangePw
		newKey    = "POST " + secAPIKey
		rotateKey = "PUT " + secAPIKey
		getKey    = "GET " + secAPIKey
		revokeKey = "DELETE " + secAPIKey
		revokeFor = "DELETE " + secAPIKey + "/{username}"
		newSec    = `{"name":"sec-user","email":"s@example.com","password":"Sec-pass1"}`
	)
	return []statusCase{
		{users, secUsers, "admin", "", 200},
		{users, secUsers, "user", "", 403},
		{users, secUsers, "anonymous", "", 401},
		{users, secUsers, "", "", 401},

		{putUser, secUsers + "/sec-user", "admin", newSec, 201},
		{putUser, secUsers + "/sec-user", "admin", newSec, 201},
		{
			putUser, secUsers + "/del-user", "admin",
			`{"name":"del-user","email":"d@example.com","password":"Del-pass1"}`, 201,
		},
		{putUser, secUsers + "/bad", "admin", `{"name":"bad","password":"x"}`, 400},
		{putUser, secUsers + "/bad", "admin", `{"name":"other","email":"e","password":"x"}`, 400},
		{putUser, secUsers + "/bad", "admin", `{"name":"bad","email":"e"}`, 400},
		{
			putUser, secUsers + "/bad", "admin",
			`{"name":"bad","email":"e","password":"x","groups":["nope"]}`, 400,
		},
		{putUser, secUsers + "/bad", "user", newSec, 403},
		{putUser, secUsers + "/bad", "", newSec, 401},

		{user, secUsers + "/sec-user", "admin", "", 200},
		{user, secUsers + "/nobody", "admin", "", 404},
		{user, secUsers + "/sec-user", "user", "", 403},

		{postUser, secUsers + "/sec-user", "admin", `{"email":"t@example.com"}`, 200},
		{postUser, secUsers + "/sec-user", "admin", `{"groups":["nope"]}`, 400},
		{postUser, secUsers + "/nobody", "admin", `{"email":"t@example.com"}`, 404},
		{postUser, secUsers + "/sec-user", "user", `{"email":"t@example.com"}`, 403},

		{delUser, secUsers + "/del-user", "admin", "", 200},
		{delUser, secUsers + "/admin", "admin", "", 400},
		{delUser, secUsers + "/nobody", "admin", "", 404},
		{delUser, secUsers + "/sec-user", "user", "", 403},

		{groups, secGroups, "admin", "", 200},
		{groups, secGroups, "user", "", 403},
		{
			putGroup,
			secGroups + "/sec-group",
			"admin",
			`{"name":"sec-group","userNames":["sec-user"]}`,
			201,
		},
		{putGroup, secGroups + "/bad", "admin", `{"userNames":["nobody"]}`, 400},
		{putGroup, secGroups + "/bad", "user", `{}`, 403},
		{group, secGroups + "/sec-group?includeUsers=true", "admin", "", 200},
		{group, secGroups + "/nobody", "admin", "", 404},
		{group, secGroups + "/sec-group", "user", "", 403},
		{postGroup, secGroups + "/sec-group", "admin", `{"description":"d"}`, 200},
		{postGroup, secGroups + "/nobody", "admin", `{"description":"d"}`, 404},
		{postGroup, secGroups + "/sec-group", "user", `{"description":"d"}`, 403},
		{delGroup, secGroups + "/sec-group", "admin", "", 200},
		{delGroup, secGroups + "/nobody", "admin", "", 404},
		{delGroup, secGroups + "/readers", "user", "", 403},

		{locked, "/artifactory/api/security/lockedUsers", "admin", "", 200},
		{locked, "/artifactory/api/security/lockedUsers", "user", "", 403},
		// A locked account answers 403 whatever password it sends, until it
		// is unlocked; a signed-in user reads its absent API key as 404
		// (#475).
		{getKey, secAPIKey, "locked:pw", "", 403},
		{getKey, secAPIKey, "locked:wrong", "", 403},
		{unlockAll, "/artifactory/api/security/unlockAllUsers", "admin", "", 200},
		{getKey, secAPIKey, "locked:pw", "", 404},
		{getKey, secAPIKey, "locked:wrong", "", 401},
		{unlockAll, "/artifactory/api/security/unlockAllUsers", "user", "", 403},
		{unlock, "/artifactory/api/security/unlockUsers", "admin", `["sec-user","ghost"]`, 200},
		{unlock, "/artifactory/api/security/unlockUsers", "admin", `[]`, 400},
		{unlock, "/artifactory/api/security/unlockUsers", "user", `["sec-user"]`, 403},
		{unlockOne, "/artifactory/api/security/unlockUsers/sec-user", "admin", "", 200},
		{unlockOne, "/artifactory/api/security/unlockUsers/nobody", "admin", "", 404},
		{unlockOne, "/artifactory/api/security/unlockUsers/sec-user", "user", "", 403},

		{getLock, secLockPol, "admin", "", 200},
		{getLock, secLockPol, "user", "", 403},
		{putLock, secLockPol, "admin", `{"enabled":"false","loginAttempts":"3"}`, 200},
		{putLock, secLockPol, "admin", `{"enabled":true,"loginAttempts":0}`, 400},
		{putLock, secLockPol, "user", `{"enabled":false}`, 403},
		{getExpiry, secExpiry, "admin", "", 200},
		{getExpiry, secExpiry, "user", "", 403},
		{putExpiry, secExpiry, "admin", `{"enabled":"false","passwordMaxAge":"30"}`, 200},
		{putExpiry, secExpiry, "admin", `{"passwordMaxAge":0}`, 400},
		{putExpiry, secExpiry, "user", `{"enabled":false}`, 403},
		{expire, secExpire, "admin", `["sec-user","ghost"]`, 200},
		{expire, secExpire, "admin", `[]`, 400},
		{expire, secExpire, "user", `["sec-user"]`, 403},
		{unexpire, secUnexpire + "sec-user", "admin", "", 200},
		{unexpire, secUnexpire + "nobody", "admin", "", 404},
		{unexpire, secUnexpire + "sec-user", "user", "", 403},
		{expireOne, secExpire + "/sec-user", "admin", "", 200},
		{expireOne, secExpire + "/nobody", "admin", "", 404},
		{expireOne, secExpire + "/sec-user", "user", "", 403},
		{expireOne, secExpire + "/sec-user", "", "", 401},
		// Expiring every password, the caller's included, would lock the
		// later rows out; TestAccLegacyExpireEveryPassword covers the 200.
		{expireAll, secExpire + "ForAllUsers", "user", "", 403},
		{expireAll, secExpire + "ForAllUsers", "", "", 401},
		{changePw, secChangePw, "admin", changeBody("sec-user", "Sec-pass1", "Sec-pass2"), 200},
		{changePw, secChangePw, "admin", changeBody("sec-user", "wrong", "Sec-pass3"), 400},
		{changePw, secChangePw, "admin", changeBody("nobody", "x", "Nobody-1"), 404},
		{changePw, secChangePw, "admin", `{"oldPassword":"x"}`, 400},
		{changePw, secChangePw, "user", changeBody("sec-user", "Sec-pass2", "Sec-pass4"), 403},
		// The user's own change, refused by the policy as a reuse, so the
		// fixture's password stays.
		{changePw, secChangePw, "user", changeBody("user", "pw", "pw"), 400},
		{changePw, secChangePw, "user-token", changeBody("user", "pw", "pw"), 400},
		{changePw, secChangePw, "anonymous", changeBody("anonymous", "", "Anon-1"), 401},
		{changePw, secChangePw, "", changeBody("user", "pw", "User-pass2"), 401},

		{newKey, secAPIKey, "user", "", 400},
		{newKey, secAPIKey, "", "", 401},
		{rotateKey, secAPIKey, "user", "", 400},
		{rotateKey, secAPIKey, "anonymous", "", 401},
		{getKey, secAPIKey, "user", "", 404},
		{getKey, secAPIKey, "", "", 401},
		{revokeKey, secAPIKey, "user", "", 200},
		{revokeKey, secAPIKey + "?deleteAll=1", "user", "", 403},
		{revokeKey, secAPIKey + "?deleteAll=1", "admin", "", 200},
		{revokeKey, secAPIKey, "anonymous", "", 401},
		{revokeFor, secAPIKey + "/user", "admin", "", 200},
		{revokeFor, secAPIKey + "/nobody", "admin", "", 404},
		{revokeFor, secAPIKey + "/user", "user", "", 403},
	}
}

// changeBody is a legacy password change of user from old to next.
func changeBody(user, old, next string) string {
	return fmt.Sprintf(`{"userName":%q,"oldPassword":%q,"newPassword1":%q,"newPassword2":%q}`,
		user, old, next, next)
}
