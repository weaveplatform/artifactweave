package app

// This file holds the in-package router worlds, request helpers and guard
// table rows that root tests in several files share, so that a root test
// file can leave the package without taking a helper that a file staying
// behind still calls. Suites outside the package build the same worlds
// through test/server/support/harness.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessmodule "github.com/weaveplatform/artifactweave/migration/access"
	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
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
func newItemWorld(t *testing.T, anonymous bool, tune ...func(*RouterConfig)) *itemWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	hash := cheapHash
	for _, u := range []accesstest.User{
		{Name: "reader", PasswordHash: hash, Groups: []string{authn.ReadersGroup}},
		{Name: "deployer", PasswordHash: hash},
		{Name: "user", PasswordHash: hash},
	} {
		if err := db.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "deploy-libs",
		Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
				"deployer": accessapi.ActionRead | accessapi.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{"libs-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
		// The seeded Any Remote grants anonymous nothing (#383), so anonymous
		// reads remotes through a permission of its own, as JFrog advises.
		if err := db.Permissions().Create(ctx, accesstest.Permission{
			Name: anonymousRemotes,
			Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
					kernel.AnonymousUser: accessapi.ActionRead,
				}},
				Targets: map[string]accesstest.Target{
					accessapi.TargetAnyRemote: {Include: []string{"**"}},
				},
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
	cfg := RouterConfig{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: storageSigner(fsBlobs), Auth: a,
	}
	for _, f := range tune {
		f(&cfg)
	}
	w := &itemWorld{t: t, db: db, blobs: cfg.Blobs}
	w.h = NewRouter(cfg)
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

func (w *itemWorld) item(key string, cached bool, p string) artmodel.Item {
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
	reader := accesstest.User{
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
	w.h = NewRouter(RouterConfig{
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
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
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
	h := NewRouter(
		RouterConfig{CompatVersion: "7.155.0", Store: db, Auth: a, PasswordHash: testHash},
	)
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
		sc, err := kernel.ParseScope(scope)
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

// authorizerOver is the access module's Authorizer over st, as NewRouter
// binds it for a RouterConfig whose Access is unset.
func authorizerOver(st *inmem.Store) accessapi.Authorizer {
	return withAccess(RouterConfig{Store: st}).Access.Authorizer
}

func bearerAuth(token string) map[string]string {
	return support.Bearer(token)
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
func seedKeys(t *testing.T, db *inmem.Store) {
	t.Helper()
	ctx := context.Background()
	k := testKeys()
	if err := db.Keys().CreateKeypair(ctx, accessapi.Keypair{
		Name: "kp-used", Type: accessapi.KeypairRSA, Alias: "used",
		PrivateKey: k.private, PublicKey: k.public,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Keys().PutCertificate(ctx, accessapi.Certificate{
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

func sha1Hex(s string) string {
	b := sha1.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

func sha256Hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func md5Hex(s string) string {
	b := md5.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

const aqlURL = "/artifactory/api/search/aql"

// The artifacts handlers' paths that the root's tests and guard tables name,
// spelled as artifactshttp spells them, so that a change there fails a test
// here.
const (
	buildsPath      = "/artifactory/api/build"
	uploadsCreate   = "/artifactory/api/v1/uploads/create"
	uploadsURLPart  = "/artifactory/api/v1/uploads/urlPart"
	uploadsPart     = "/artifactory/api/v1/uploads/part"
	uploadsComplete = "/artifactory/api/v1/uploads/complete"
	uploadsStatus   = "/artifactory/api/v1/uploads/status"
	uploadsAbort    = "/artifactory/api/v1/uploads/abort"
)

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, files[n])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var errBoom = errors.New("boom")

// faultyStore fails the repository operations named in fail with errBoom.
type faultyStore struct {
	*inmem.Store
	fail map[string]bool
}

func (f faultyStore) Repos() artport.RepoStore {
	return faultyRepos{RepoStore: f.Store.Repos(), fail: f.fail}
}

func (f faultyStore) Permissions() accesstest.PermissionStore {
	if f.fail["permissions"] {
		return failingPermissionStore{f.Store.Permissions()}
	}
	return f.Store.Permissions()
}

type failingPermissionStore struct{ accesstest.PermissionStore }

func (failingPermissionStore) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]accesstest.Permission, error) {
	return nil, errBoom
}

type faultyRepos struct {
	artport.RepoStore
	fail map[string]bool
}

func (f faultyRepos) Including(ctx context.Context, key string) ([]string, error) {
	if f.fail["including"] {
		return nil, errBoom
	}
	return f.RepoStore.Including(ctx, key) //nolint:wrapcheck // test double
}

func (f faultyRepos) List(ctx context.Context, rf artport.RepoFilter) ([]repo.Config, error) {
	if f.fail["list"] {
		return nil, errBoom
	}
	return f.RepoStore.List(ctx, rf) //nolint:wrapcheck // test double
}

// Get fails for every key under "get", or for one key under "get:<key>".
func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail["get"] || f.fail["get:"+key] {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

func (f faultyRepos) Create(ctx context.Context, c repo.Config) error {
	if f.fail["create"] {
		return errBoom
	}
	return f.RepoStore.Create(ctx, c) //nolint:wrapcheck // test double
}

func (f faultyRepos) Update(ctx context.Context, c repo.Config) error {
	if f.fail["update"] {
		return errBoom
	}
	return f.RepoStore.Update(ctx, c) //nolint:wrapcheck // test double
}

func (f faultyRepos) Delete(ctx context.Context, key string) error {
	if f.fail["delete"] {
		return errBoom
	}
	return f.RepoStore.Delete(ctx, key) //nolint:wrapcheck // test double
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errBoom }

type sourceBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func sourceIdentity(context.Context, repo.Config) (artmodel.SourceIdentity, error) {
	return artmodel.SourceIdentity{
		Kind:       artmodel.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

type sourceBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *sourceBody) Close() error { b.closes.Add(1); return nil }

// bearerFor issues dev a token with scope.
func bearerFor(t *testing.T, tokens *token.Service, scope string) map[string]string {
	t.Helper()
	sc, err := kernel.ParseScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := tokens.Issue(context.Background(), token.Request{Username: "dev", Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + issued.AccessToken}
}

// buildsAQLWorld is a router over a store with runs app/1, app/2 and
// tool/1, each producing libs-local/app.jar, and a promotion of app/1. dev
// may read libs-local and the builds under app/**; tokens can be issued.
func buildsAQLWorld(t *testing.T, tune ...func(*RouterConfig)) (http.Handler, *token.Service) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(ctx, accesstest.User{Name: "dev", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	dev := accesstest.Grants{Users: map[string]accessapi.Actions{"dev": accessapi.ActionRead}}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "dev-app", Resources: map[accessapi.ResourceType]accesstest.Resource{
			accessapi.ResourceArtifact: {
				Actions: dev,
				Targets: map[string]accesstest.Target{"libs-local": {Include: []string{"**"}}},
			},
			accessapi.ResourceBuild: {
				Actions: dev,
				Targets: map[string]accesstest.Target{
					"artifactory-build-info": {Include: []string{"app/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{
		Store: db, Blobs: blobs, Formats: ecosystem.For,
	}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: "app.jar"}, Body: strings.NewReader("jar"),
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jar := artmodel.BuildFile{Name: "app.jar", Checksums: kernel.Checksums{SHA1: sha1Hex("jar")}}
	for n, run := range [][2]string{{"app", "1"}, {"app", "2"}, {"tool", "1"}} {
		b, err := db.Builds().Put(ctx, artmodel.Build{
			Name: run[0], Number: run[1], Started: base.Add(time.Duration(n) * time.Minute),
			Created: base.Add(time.Hour), CreatedBy: "ci", URL: "https://ci/" + run[0],
			Modules: []artmodel.BuildModule{{ID: run[0], Artifacts: []artmodel.BuildFile{jar}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if _, err := db.Builds().AddPromotion(ctx, artmodel.BuildPromotion{
				BuildID: b.ID, Created: base.Add(2 * time.Hour), CreatedBy: "admin",
				Status: "released", Repo: "libs-release", CIUser: "jenkins",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := RouterConfig{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Tokens: tokens,
	}
	for _, f := range tune {
		f(&cfg)
	}
	return NewRouter(cfg), tokens
}

// searchWorld is an item world holding:
//   - libs-local/org/a/lib-1.0.jar (team=core, tier=gold), deployed with
//     its true MD5 and SHA-1 declared;
//   - libs-local/org/a/lib-1.0.pom (team=core);
//   - other-local/org/b/lib-2.0.jar (team=edge), the same bytes as the jar;
//   - server-local/bad.jar, whose declared MD5 is wrong;
//   - up's cache cached.txt, created an hour ago and never downloaded.
//
// deployer reads libs-local only; anonymous, when on, reads up's cache.
func searchWorld(t *testing.T, anonymous bool, tune ...func(*RouterConfig)) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous, tune...)
	w.put("/artifactory/other-local/org/b/lib-2.0.jar;team=edge", "jar")
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/org/a/lib-1.0.jar;team=core;tier=gold",
		"admin", "jar", "X-Checksum-Md5", md5Hex("jar"), "X-Checksum-Sha1", sha1Hex("jar")),
		http.StatusCreated)
	w.put("/artifactory/libs-local/org/a/lib-1.0.pom;team=core", "pom")
	wantStatus(t, w.do("PUT", "/artifactory/server-local/bad.jar", "admin", "bad",
		"X-Checksum-Md5", md5Hex("other")), http.StatusCreated)
	hour := time.Now().Add(-time.Hour).UTC()
	if _, err := w.db.Items().Put(context.Background(), artmodel.Item{
		RepoKey: "up", Cached: true, Path: kernel.RootPath, Name: "cached.txt", Size: 1,
		Actual:  kernel.Checksums{SHA1: sha1Hex("c"), MD5: md5Hex("c")},
		Created: hour, Modified: hour, Updated: hour,
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// hitsOf returns a search's results, failing unless it answered 200.
func hitsOf(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	wantStatus(t, rec, http.StatusOK)
	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if body.Results == nil {
		t.Fatalf("results missing or null: %s", rec.Body)
	}
	return body.Results
}

// restrictionsOver is the access module's API restriction check over st, the
// store a test's multipart service shares with its server, as the runtime
// binds it.
func restrictionsOver(t *testing.T, st *inmem.Store) accessapi.Restrictions {
	t.Helper()
	m, err := accessmodule.New(accessmodule.Deps{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return m.API().Restrictions
}

// setTrashcan turns the trash can on or off, with Artifactory's retention.
func setTrashcan(t *testing.T, db *inmem.Store, on bool) {
	t.Helper()
	if err := db.Settings().Put(context.Background(), settings.SettingTrashcan,
		artmodel.TrashcanSettings{Enabled: on, RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
}

// overwriter is the status fixture's user who may deploy but not delete:
// in libs-local under overwrite/, where seedOverwriteStatus puts the files
// its rows would replace, and in the formats' repositories their parts
// grant it.
const overwriter = "overwriter:pw"

// seedOverwriteStatus adds overwriter, its grant in libs-local, and the
// files there its rows act on: taken.txt, which they would replace, and
// other.txt, whose content a checksum deploy would put there.
func seedOverwriteStatus(t *testing.T, db *inmem.Store, items *item.Service) {
	t.Helper()
	ctx := context.Background()
	name, _, _ := strings.Cut(overwriter, ":")
	if err := db.Users().
		Create(ctx, accesstest.User{Name: name, PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	grantOverwriter(t, db, "libs-local", "overwrite/")
	libs, err := db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{
		"overwrite/taken.txt": "taken", "overwrite/other.txt": "other content",
	} {
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: libs, Path: p}, Body: strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// searchStatusRows: quick, property, checksum and bad checksum searches
// answer anonymous callers while anonymous access is on; the others need a
// named user. A caller in no group gets 200 and only what it may read.
func searchStatusRows() []statusCase {
	const (
		artifact = "GET /artifactory/api/search/artifact"
		prop     = "GET /artifactory/api/search/prop"
		checksum = "GET /artifactory/api/search/checksum"
		bad      = "GET /artifactory/api/search/badChecksum"
		creation = "GET /artifactory/api/search/creation"
		dates    = "GET /artifactory/api/search/dates"
		usage    = "GET /artifactory/api/search/usage"
		pattern  = "GET /artifactory/api/search/pattern"
		s        = "/artifactory/api/search/"
	)
	sum := s + "checksum?sha1=" + sha1Hex("fixture")
	var rows []statusCase
	for _, op := range []struct{ route, path, invalid string }{
		{artifact, s + "artifact?name=get", s + "artifact"},
		{prop, s + "prop?k=v", s + "prop"},
		{checksum, sum, s + "checksum?sha1=x"},
		{bad, s + "badChecksum?type=sha1", s + "badChecksum?type=crc"},
	} {
		rows = append(rows,
			statusCase{op.route, op.path, "admin", "", 200},
			statusCase{op.route, op.path, "user", "", 200},
			statusCase{op.route, op.path, "anonymous", "", 200},
			statusCase{op.route, op.path, "", "", 401},
			statusCase{op.route, op.invalid, "admin", "", 400},
		)
	}
	for _, op := range []struct{ route, path, invalid string }{
		{creation, s + "creation?from=0", s + "creation?from=9&to=1"},
		{dates, s + "dates?from=0&dateFields=created", s + "dates?from=0&dateFields=x"},
		{usage, s + "usage?notUsedSince=0", s + "usage?notUsedSince=x"},
		{pattern, s + "pattern?pattern=libs-local:fixture/*", s + "pattern?pattern=libs-local:**"},
	} {
		rows = append(rows,
			statusCase{op.route, op.path, "admin", "", 200},
			statusCase{op.route, op.path, "user", "", 200},
			statusCase{op.route, op.path, "anonymous", "", 401},
			statusCase{op.route, op.path, "", "", 401},
			statusCase{op.route, op.invalid, "admin", "", 400},
		)
	}
	rows = append(rows, statusCase{creation, s + "creation?from=0&repos=missing", "admin", "", 404})
	return append(rows, moreSearchStatusRows()...)
}

// searchMatrix: the four searches anonymous may make answer anyone, with
// what each caller may read; the others answer any named user.
func searchMatrix() []matrixRow {
	const s = "/artifactory/api/search/"
	return append([]matrixRow{
		row("GET "+s+"artifact", s+"artifact?name=get", "", anyone, 200),
		row("GET "+s+"prop", s+"prop?k", "", anyone, 200),
		row("GET "+s+"checksum", s+"checksum?sha1="+sha1Hex("matrix"), "", anyone, 200),
		row("GET "+s+"badChecksum", s+"badChecksum?type=md5", "", anyone, 200),
		row("GET "+s+"creation", s+"creation?from=0", "", signedIn, 200),
		row("GET "+s+"dates", s+"dates?from=0&dateFields=created", "", signedIn, 200),
		row("GET "+s+"usage", s+"usage?notUsedSince=0", "", signedIn, 200),
		row("GET "+s+"pattern", s+"pattern?pattern=libs-local:work/*", "", signedIn, 200),
	}, moreSearchMatrix()...)
}

// trashStatusRows run after itemStatusRows, which trashed
// libs-local/fixture/del.txt in the same fixture.
func trashStatusRows() []statusCase {
	const (
		list    = "GET /artifactory/api/storage/auto-trashcan"
		restore = "POST /artifactory/api/trash/restore/{fromPath}"
		clean   = "DELETE /artifactory/api/trash/clean/{repoPath}"
		remove  = "DELETE /artifactory/{repoKey}/{filePath}"
		empty   = "POST /artifactory/api/trash/empty"
		gcOp    = "POST /artifactory/api/system/storage/gc"
		info    = "GET /artifactory/api/storageinfo"
		calc    = "POST /artifactory/api/storageinfo/calculate"
		zap     = "POST /artifactory/api/zap/{repoPath}"
		tc      = "/artifactory/api/storage/auto-trashcan"
		del     = "libs-local/fixture/del.txt"
		rs      = "/artifactory/api/trash/restore/"
		cl      = "/artifactory/api/trash/clean/"
		create  = "PUT /artifactory/api/repositories/{repoKey}"
		drop    = "DELETE /artifactory/api/repositories/{repoKey}"
		deploy  = "PUT /artifactory/{repoKey}/{filePath}"
		gone    = "/artifactory/api/repositories/trashed-repo"
	)
	return []statusCase{
		{list, tc, "admin", "", 200},
		{list, tc + "/" + del, "admin", "", 200},
		{list, tc + "/libs-local/absent", "admin", "", 404},
		{list, tc + "/gone-repo", "admin", "", 404},
		// "user" manages perm-local and holds nothing in libs-local (#439).
		{list, tc, "user", "", 200},
		{list, tc + "/perm-local", "user", "", 404},
		{list, tc + "/" + del, "user", "", 403},
		{list, tc + "/libs-local/absent", "user", "", 403},
		{list, tc + "/gone-repo", "user", "", 403},
		{list, tc, "anonymous", "", 401},
		{list, tc, "", "", 401},
		// The storage API's views, with the same filter (#455).
		{
			list,
			tc + "?list&deep=1&listFolders=1&mdTimestamps=1&includeRootPath=1",
			"admin",
			"",
			200,
		},
		{list, tc + "/libs-local?list&depth=1", "admin", "", 200},
		{list, tc + "/" + del + "?list", "admin", "", 400},
		{list, tc + "/libs-local/absent?list", "admin", "", 404},
		{list, tc + "/" + del + "?properties", "admin", "", 200},
		{list, tc + "/" + del + "?properties=absent", "admin", "", 404},
		{list, tc + "/libs-local/fixture?properties", "admin", "", 404},
		{list, tc + "/" + del + "?stats", "admin", "", 200},
		{list, tc + "/libs-local/fixture?stats", "admin", "", 400},
		{list, tc + "/" + del + "?lastModified", "admin", "", 200},
		{list, tc + "/libs-local?lastModified", "admin", "", 200},
		{list, tc + "/" + del + "?permissions", "admin", "", 400},
		{list, tc + "?list&stats", "admin", "", 400},
		{list, tc + "?depth=1", "admin", "", 400},
		{list, tc + "?list&deep=1", "user", "", 200},
		{list, tc + "/libs-local?list", "user", "", 403},
		{list, tc + "/" + del + "?properties", "user", "", 403},
		{list, tc + "/" + del + "?stats", "user", "", 403},
		{list, tc + "/libs-local?lastModified", "user", "", 403},
		{list, tc + "/" + del + "?permissions", "user", "", 403},
		{list, tc + "?list", "", "", 401},

		{restore, rs + del + "?to=virt", "admin", "", 400},
		{restore, rs + del, "admin", "", 400},
		{restore, rs + del + "?to=missing-repo", "admin", "", 400},
		{restore, rs + del + "?to=libs-local&transaction-size=0", "admin", "", 400},
		{restore, rs + del + "?to=libs-local", "user", "", 400},
		{restore, rs + del + "?to=libs-local", "", "", 401},
		{restore, rs + del + "?to=libs-local&transaction-size=10", "admin", "", 202},
		{restore, rs + del + "?to=libs-local", "admin", "", 404},
		{restore, rs + "libs-local?to=libs-local", "admin", "", 404},
		// An admin may restore from a repository that is gone (#380).
		{restore, rs + "gone-repo/x?to=libs-local", "admin", "", 404},
		// A repository delete keeps the content in the trash can under the
		// repository's key, which only an admin sees or restores, into a
		// repository that exists (#504).
		{create, gone, "admin", `{"rclass":"local"}`, 200},
		{deploy, "/artifactory/trashed-repo/kept/file.txt", "admin", "kept", 201},
		{drop, gone, "admin", "", 200},
		{list, tc + "/trashed-repo/kept/file.txt", "admin", "", 200},
		{list, tc + "/trashed-repo", "user", "", 403},
		{restore, rs + "trashed-repo?to=libs-local/from-trashed-repo", "user", "", 400},
		{restore, rs + "trashed-repo?to=trashed-repo", "admin", "", 400},
		{restore, rs + "trashed-repo?to=libs-local/from-trashed-repo", "admin", "", 202},
		{list, tc + "/trashed-repo", "admin", "", 404},

		{remove, "/artifactory/" + del, "admin", "", 204},
		{clean, cl + del, "user", "", 400},
		{clean, cl + del, "", "", 401},
		{clean, cl + "gone-repo/x", "user", "", 400},
		{clean, cl + del, "admin", "", 204},
		{clean, cl + "libs-local", "admin", "", 204},

		{empty, "/artifactory/api/trash/empty", "user", "", 400},
		{empty, "/artifactory/api/trash/empty", "anonymous", "", 401},
		{empty, "/artifactory/api/trash/empty", "", "", 401},
		{empty, "/artifactory/api/trash/empty", "admin", "", 202},

		{gcOp, "/artifactory/api/system/storage/gc", "admin", "", 200},
		{gcOp, "/artifactory/api/system/storage/gc", "user", "", 403},
		{gcOp, "/artifactory/api/system/storage/gc", "", "", 401},

		{info, "/artifactory/api/storageinfo", "admin", "", 200},
		{info, "/artifactory/api/storageinfo", "admin-token", "", 200},
		{info, "/artifactory/api/storageinfo", "user", "", 403},
		{info, "/artifactory/api/storageinfo", "user-token", "", 403},
		{info, "/artifactory/api/storageinfo", "anonymous", "", 401},
		{info, "/artifactory/api/storageinfo", "", "", 401},
		{calc, "/artifactory/api/storageinfo/calculate", "admin", "", 202},
		{calc, "/artifactory/api/storageinfo/calculate", "user", "", 403},

		{zap, "/artifactory/api/zap/remote-private-cache", "admin", "", 200},
		{zap, "/artifactory/api/zap/remote-private-cache/absent", "admin", "", 404},
		{zap, "/artifactory/api/zap/libs-local/x", "admin", "", 400},
		{zap, "/artifactory/api/zap/missing-cache", "admin", "", 404},
		{zap, "/artifactory/api/zap/remote-private-cache", "user", "", 403},
		{zap, "/artifactory/api/zap/remote-private-cache", "", "", 401},
	}
}

// repositoryTrashed seeds a local repository named key, {p} the principal's
// name, with a file at kept/{p}.txt, and then deletes the repository, which
// moves the file to the trash can under key (#504).
func repositoryTrashed(key string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		ctx := context.Background()
		key := strings.ReplaceAll(key, "{p}", p)
		c := repo.Config{Key: key, Class: repo.Local}
		if err := f.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		f.deploy(t, key, "kept/"+p+".txt", []byte(p))
		if _, _, err := repos.Remove(ctx, f.db, key, "admin"); err != nil {
			t.Fatal(err)
		}
	}
}

// trashedThenOff seeds what trashed does and then turns the trash can off,
// so the row runs against what was trashed before it was turned off (#490).
// The seed runs once per principal on one fixture, so the trash can is
// turned on again for each principal's delete to reach it.
func trashedThenOff(repoKey, path string) seeder {
	seed := trashed(repoKey, path)
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		setTrashcan(t, f.db, true)
		seed(t, f, p)
		setTrashcan(t, f.db, false)
	}
}

func (w *itemWorld) put(p, body string) {
	w.t.Helper()
	wantStatus(w.t, w.do("PUT", p, "admin", body), http.StatusCreated)
}

// grantOverwriter lets overwriter read and deploy, but not delete, under
// the prefix in repository key.
func grantOverwriter(t *testing.T, db *inmem.Store, key, under string) {
	t.Helper()
	name, _, _ := strings.Cut(overwriter, ":")
	if err := db.Permissions().Create(context.Background(), accesstest.Permission{
		Name: "st-overwrite-" + key,
		Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
				name: accessapi.ActionRead | accessapi.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{key: {Include: []string{under + "**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

const vendorJSON = "application/vnd.org.jfrog.artifactory+json"

// startedAt is testStarted moved on by n seconds.
func startedAt(n int) string {
	t, err := build.ParseStarted(testStarted)
	if err != nil {
		panic(err)
	}
	return build.FormatStarted(t.Add(time.Duration(n) * time.Second))
}

// artifactDoc is a build-info document whose one module produced content,
// with fields the spec does not list, which must survive.
func artifactDoc(name, number, started, content string) string {
	return fmt.Sprintf(`{"name":%q,"number":%q,"started":%q,`+
		`"agent":{"name":"jfrog-cli-go","version":"2.80.0"},"vcs":[{"revision":"abc"}],`+
		`"properties":{"buildInfo.env.CI":"true","count":3},`+
		`"modules":[{"id":"m1","type":"generic","artifacts":[{"name":"a.bin","path":"app/a.bin",`+
		`"sha1":%q,"sha256":%q,"md5":%q,"localPath":"/tmp/a.bin"}]}]}`,
		name, number, started, sha1Hex(content), sha256Hex(content), md5Hex(content))
}

func (w *itemWorld) publish(doc string) {
	w.t.Helper()
	wantStatus(w.t, w.do(http.MethodPut, buildsPath, "admin", doc, "Content-Type", vendorJSON), 204)
}

// buildSearchFaults fails the build or item store operation named fail.
type buildSearchFaults struct {
	*inmem.Store
	fail string
}

func (f buildSearchFaults) Builds() artport.BuildStore {
	return faultyBuildSearch{f.Store.Builds(), f.fail}
}

func (f buildSearchFaults) Items() artport.ItemStore {
	return faultyChecksumSearch{f.Store.Items(), f.fail}
}

type faultyBuildSearch struct {
	artport.BuildStore
	fail string
}

func (f faultyBuildSearch) FindDependencies(
	ctx context.Context,
	c kernel.Checksums,
) ([]artmodel.BuildFileRef, error) {
	if f.fail == "dependencies" {
		return nil, errBoom
	}
	return f.BuildStore.FindDependencies(ctx, c)
}

func (f faultyBuildSearch) Runs(
	ctx context.Context,
	project, name string,
	p kernel.Page,
) ([]artmodel.BuildRun, string, error) {
	if f.fail == "runs" {
		return nil, "", errBoom
	}
	return f.BuildStore.Runs(ctx, project, name, p)
}

type faultyChecksumSearch struct {
	artport.ItemStore
	fail string
}

func (f faultyChecksumSearch) FindByChecksum(
	ctx context.Context,
	c kernel.Checksums,
	p kernel.Page,
) ([]artmodel.Item, string, error) {
	if f.fail == "find" {
		return nil, "", errBoom
	}
	return f.ItemStore.FindByChecksum(ctx, c, p)
}

const (
	p2ExternalComposite          = `<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="https://child.example/site"/></children></repository>`
	p2ExternalArtifactsComposite = `<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.artifact.repository.CompositeArtifactRepository"><children size="1"><child location="https://child.example/site"/></children></repository>`
)

const (
	cranLegacyPath  = "src/contrib/Archive/assertthat/0.2.1/assertthat_0.2.1.tar.gz"
	cranArchivePath = "src/contrib/Archive/assertthat/assertthat_0.2.1.tar.gz"
	cranMoveURL     = "/artifactory/api/cran/cran-migrate/move-archives"
)

const (
	xetFixtureHash = "4f7ccc9872630143f6a4a26904b5255fef96e6c75cf78f0449114d77a94fd197"
	xetFixtureFile = "869fb7b9851f0c53b04e946381db72f24d74462c24cbe4737856e41992386def"
)

type xetTestToken struct {
	CAS   string `json:"casUrl"`
	Token string `json:"accessToken"`
}

func xetFixture(t *testing.T, name string) []byte {
	t.Helper()
	return repopath.Read(t, "migration/artifacts/ecosystem/huggingface/xet/testdata/"+name)
}

func newXetWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(
		t,
		false,
		func(c *RouterConfig) { c.HuggingFaceXetEnabled = true; c.Keys = support.TerraformBackendKeys(t) },
	)
	for _, c := range []repo.Config{{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML}, {Key: "hf-f", Class: repo.Federated, PackageType: repo.HuggingFaceML}, {Key: "hf-v", Class: repo.Virtual, PackageType: repo.HuggingFaceML, Repositories: []string{"hf"}, DefaultDeploymentRepo: "hf"}} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func xetTokenFor(t *testing.T, w *itemWorld, key, identity, mode, as string) xetTestToken {
	t.Helper()
	r := w.do(
		"GET",
		"/artifactory/api/huggingfaceml/"+key+"/api/models/"+identity+"/xet-"+mode+"-token/main",
		as,
		"",
	)
	wantStatus(t, r, 200)
	var token xetTestToken
	if e := json.Unmarshal(r.Body.Bytes(), &token); e != nil {
		t.Fatal(e)
	}
	return token
}

func xetCall(
	w *itemWorld,
	token xetTestToken,
	method, path string,
	body []byte,
) *httptest.ResponseRecorder {
	return w.do(method, token.CAS+path, "", string(body), "Authorization", "Bearer "+token.Token)
}

func publishXetFixture(t *testing.T, w *itemWorld, key string) (xetTestToken, string) {
	t.Helper()
	write := xetTokenFor(t, w, key, "acme/demo", "write", "admin")
	wantStatus(
		t,
		xetCall(w, write, "POST", "/v1/xorbs/default/"+xetFixtureHash, xetFixture(t, "large.xorb")),
		200,
	)
	wantStatus(t, xetCall(w, write, "POST", "/v2/shards", xetFixture(t, "large.shard")), 200)
	data := make([]byte, 1280000)
	for i := range data {
		data[i] = byte(i)
	}
	sum := sha256.Sum256(data)
	body := fmt.Sprintf(
		"{\"key\":\"header\",\"value\":{\"summary\":\"native Xet\"}}\n{\"key\":\"lfsFile\",\"value\":{\"path\":\"weights.bin\",\"algo\":\"sha256\",\"oid\":\"%x\",\"size\":%d}}\n",
		sum,
		len(data),
	)
	commit := w.do(
		"POST",
		"/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/main",
		"admin",
		body,
		"Content-Type",
		"application/x-ndjson",
	)
	wantStatus(t, commit, 200)
	var result struct {
		OID string `json:"commitOid"`
	}
	if e := json.Unmarshal(commit.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return write, result.OID
}

// testHash is the Argon2id cost these tests hash with. Verification reads
// the cost from each stored hash, so a cheap hash checks like a real one;
// the production 64 MiB hash, once per seed and once per authenticator,
// made this package take minutes under the race detector.
var testHash = support.TestHash

// cheapHash is a cheap hash of "pw", the password of every test principal.
var cheapHash = support.CheapHash

// cheapSeed seeds db with the built-in principals, admin's password "pw".
func cheapSeed(t *testing.T, db *inmem.Store) {
	t.Helper()
	support.Seed(t, db)
}
