package access_test

// This file holds the access suites' fixtures: adapters, under the names of
// the router guards' fixtures in migration/app, over
// test/server/support/harness and test/server/support, and copies of the
// seeding those fixtures do, which app keeps for its guard tables. Every
// router here is harness.Router, so a change to the router's assembly reaches
// these tests without an edit here.

import (
	"archive/zip"
	"bytes"
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
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

// New is the production router over c.
func New(c Config) http.Handler { return harness.Router(c) }

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db store.Store) {
	t.Helper()
	support.Seed(t, db)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	harness.WantStatus(t, rec, want)
}

// itemWorld is the harness's item world under the fields the tests read.
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

func newItemWorld(t *testing.T, anonymous bool, tune ...harness.Tune) *itemWorld {
	t.Helper()
	w := harness.NewItemWorld(t, anonymous, tune...)
	return &itemWorld{t: w.T, h: w.H, db: w.DB, blobs: w.Blobs}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	world := harness.ItemWorld{T: w.t, H: w.h, DB: w.db, Blobs: w.blobs}
	return world.Do(method, path, as, body, headers...)
}

// The router's paths and limits that the tests name, spelled as the router and
// its handlers spell them, so that a change there fails a test here.
const (
	apiRestrictionPath      = "/artifactory/api/artifactweave/api-restrictions/"
	apiRestrictionAuditPath = "/artifactory/api/artifactweave/api-restriction-audit"
	legacyTokenPath         = "/artifactory/api/security/token"
	maxCertificateBundle    = 1 << 20
)

const (
	permV1  = "/artifactory/api/security/permissions"
	permV2  = "/artifactory/api/v2/security/permissions"
	permAcc = "/access/api/v2/permissions"
)

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

// statusFixture is the world of the root status fixture behind the
// production router: a seeded store with the admin (password "pw"), a user
// "user" (password "pw") in no group, a locked user "locked" (password
// "pw"), the repositories, key pairs, permissions, items and tokens the
// root's status rows use. It leaves out what only the format, build,
// search, overwrite and multipart rows need, which no access test reaches.
func statusFixture(t *testing.T, anonymous bool, tune ...func(*Config)) http.Handler {
	t.Helper()
	h, _ := newStatusFixture(t, anonymous, tune...)
	return h
}

// newStatusFixture is statusFixture with the token secrets the rows refer
// to, which exist only once the fixture's signing key does.
func newStatusFixture(
	t *testing.T,
	anonymous bool,
	tune ...func(*Config),
) (http.Handler, statusSecrets) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(ctx, types.User{Name: "user", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	// A locked account, until securityStatusRows unlocks every user.
	if err := db.Users().Create(ctx, types.User{
		Name: "locked", PasswordHash: cheapHash, Status: types.UserLocked,
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	shared := true
	for _, c := range append([]repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "remote-shared", Class: repo.Remote, URL: "https://up.example", ShareConfiguration: &shared},
		{Key: "remote-private", Class: repo.Remote, URL: "https://up.example"},
		{Key: "virt", Class: repo.Virtual, Repositories: []string{"libs-local"}},
		{Key: "del-one", Class: repo.Local},
		{Key: "del-batch", Class: repo.Local},
		{Key: "upd-batch", Class: repo.Local},
	}, formatRepos()...) {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	seedKeys(t, db)
	seedPermissions(t, db)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For}
	libs, err := db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fixture/get.txt", "fixture/del.txt", "fixture/move.txt"} {
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: libs, Path: p}, Body: strings.NewReader("fixture"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: libs, Path: "fixture/archive.zip"},
		Body:   bytes.NewReader(zipBytes(t, map[string]string{"inner.txt": "inner"})),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Settings().Put(ctx, types.SettingFolderDownload,
		types.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tokens, secrets := seedTokens(t, db)
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Keys:   support.TerraformBackendKeys(t),
		Tokens: tokens,
	}
	for _, f := range tune {
		f(&cfg)
	}
	return New(cfg), secrets
}

// seedPermissions adds the permissions the status rows change, one per
// state-changing row, so the rows run in any order against one store.
// "user" holds READ and MANAGE on the st-managed* permissions. Every permission the
// rows create or seed targets perm-local, a repository no other row reads:
// a grant on a shared repository would change other rows' answers.
func seedPermissions(t *testing.T, db store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "perm-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	libs := map[string]types.Target{"perm-local": {Include: []string{"**"}}}
	for _, name := range []string{
		"st-del-v1", "st-del-v2", "st-del-access", "st-res-put", "st-res-patch", "st-res-del",
	} {
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {Targets: libs},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"st-managed", "st-managed-put", "st-managed-del", "st-managed-grant", "st-managed-other",
	} {
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"user": types.ActionRead | types.ActionManage,
				}},
				Targets: libs,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// formatRepos are the fixture repositories of the formats P7 and P8 add,
// which both fixtures create and the matrix's principals hold grants on: a
// local, a remote and a virtual repository of each, or a local one alone
// for a package type that is local only.
func formatRepos() []repo.Config {
	return slices.Concat(
		npmRepos,
		goRepos,
		mavenRepos,
		helmRepos,
		support.TerraformBackendRepos(),
		pypiRepos,
		support.TerraformRegistryRepos(),
		support.TerraformProviderRepos(),
		[]repo.Config{{
			Key: "vcs-remote", Class: repo.Remote, PackageType: repo.VCS,
			URL: "https://github.example", VCSGitProvider: repo.GitHub,
		}},
	)
}

// fixtureRepos is a format's fixture repositories, all of package type t:
// <name>-local, <name>-remote, and <name>-virtual over the local one and
// deploying to it.
func fixtureRepos(name string, t repo.PackageType) []repo.Config {
	local := name + "-local"
	return []repo.Config{
		{Key: local, Class: repo.Local, PackageType: t},
		{Key: name + "-remote", Class: repo.Remote, PackageType: t, URL: "https://up.example"},
		{
			Key: name + "-virtual", Class: repo.Virtual, PackageType: t,
			Repositories: []string{local}, DefaultDeploymentRepo: local,
		},
	}
}

// Shared root fixtures still refer to npm repositories and reproducible
// tarballs while their format-specific assertions live in
// test/server/artifacts/npm.
var npmRepos = append(fixtureRepos("npm", repo.NPM), repo.Config{
	Key: "npm-releases", Class: repo.Local, PackageType: repo.NPM, HandleSnapshots: new(bool),
})

// Shared root fixtures still include Go repositories while their protocol
// route assertions run in test/server/artifacts/golang.
var goRepos = fixtureRepos("go", repo.Go)

// Shared root fixtures retain Helm repositories for cross-format assertions;
// classic Helm route assertions run in test/server/artifacts/helm.
var helmRepos = fixtureRepos("helm", repo.Helm)

// Shared root fixtures still include Maven repositories while their route
// assertions run in test/server/artifacts/maven.
var mavenRepos = append(fixtureRepos("maven", repo.Maven), repo.Config{
	Key: "maven-releases", Class: repo.Local, PackageType: repo.Maven, HandleSnapshots: new(bool),
})

// Shared root fixtures retain PyPI repositories for cross-format assertions;
// PyPI route assertions run in test/server/artifacts/pypi.
var pypiRepos = fixtureRepos("pypi", repo.PyPI)

// statusSecrets are the fixture's token values, which only its own signing
// key can produce, by the name a row writes as {name}. These tokens expired
// an hour ago, within the refresh grace:
//   - expired-access and expired-refresh: a refreshable token of admin's;
//   - forged-access: expired-access with its signature altered;
//   - other-refresh: the refresh token of another of admin's tokens;
//   - legacy-access and legacy-refresh: a third of admin's, for the legacy
//     route;
//   - user-refresh and user-2-refresh: refresh tokens of two of user's.
//
// These are admin's, live for an hour, below the revocable threshold:
//   - short-access: neither refreshable nor forced, so not revocable;
//   - refreshable-id and refreshable-2-access: two refreshable ones, which
//     are revocable all the same.
//
// transient-access is an hour-long token an administrator minted for ghost,
// a name that is no stored user, so it authenticates a transient user (#456).
type statusSecrets map[string]string

// seedTokens gives a fixture store an instance identity and a token service,
// token records with fixed ids the status table addresses (two of admin's,
// one of user's, and a short-lived non-revocable one of admin's), and the
// statusSecrets.
func seedTokens(t *testing.T, db store.Store) (*token.Service, statusSecrets) {
	t.Helper()
	ctx := context.Background()
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	for _, r := range []types.Token{
		{ID: "admin-token", Subject: subjectOf(tokens, "admin"), Revocable: true},
		{ID: "admin-token-2", Subject: subjectOf(tokens, "admin"), Revocable: true},
		{ID: "user-token", Subject: subjectOf(tokens, "user"), Revocable: true},
		{
			ID: "short-token", Subject: subjectOf(tokens, "admin"),
			ExpiresAt: now.Add(time.Hour),
		},
	} {
		r.Scope, r.IssuedAt, r.AccessDigest = types.UserScope(), now, []byte(r.ID)
		if err := db.Tokens().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// A service two hours behind, over the same key, issues a one-hour token
	// that has expired by now but is well within the refresh grace.
	past, err := token.New(ctx, db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	secrets := statusSecrets{}
	for _, r := range []struct{ name, user string }{
		{"expired", "admin"},
		{"other", "admin"},
		{"legacy", "admin"},
		{"user", "user"},
		{"user-2", "user"},
	} {
		issued, err := past.Issue(ctx, token.Request{
			Username: r.user, Refreshable: true, ExpiresIn: &hour, ByAdmin: true,
			Description: "refreshable " + r.name,
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets[r.name+"-access"], secrets[r.name+"-refresh"] = issued.AccessToken, issued.RefreshToken
	}
	sig := []byte(secrets["expired-access"])
	sig[len(sig)-3] ^= 1
	secrets["forged-access"] = string(sig)
	for _, r := range []struct {
		name        string
		refreshable bool
	}{{"short", false}, {"refreshable", true}, {"refreshable-2", true}} {
		issued, err := tokens.Issue(ctx, token.Request{
			Username: "admin", Refreshable: r.refreshable, ExpiresIn: &hour, ByAdmin: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets[r.name+"-access"], secrets[r.name+"-id"] = issued.AccessToken, issued.Record.ID
	}
	transient, err := tokens.Issue(ctx, token.Request{
		Username: "ghost", ExpiresIn: &hour, ByAdmin: true, Identity: types.IdentityTransient,
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets["transient-access"] = transient.AccessToken
	return tokens, secrets
}

// subjectOf is the subject of name's tokens, as the token API writes it, so
// that the seeded token records belong to the users the tests name.
func subjectOf(svc *token.Service, name string) string {
	return svc.Issuer() + "/users/" + name
}

// lRefresh is a legacy refresh form body.
func lRefresh(access, refresh string) string {
	return "form:grant_type=refresh_token&access_token=" + access + "&refresh_token=" + refresh
}

// Token lifecycle tests need real identities, permissions, signing keys and one
// readable artifact. The route-status fixture also writes every package format,
// which needlessly repeats archive/signature and filesystem sync work here.
func tokenFixture(t *testing.T, tune ...func(*Config)) http.Handler {
	t.Helper()
	h, _ := newTokenFixture(t, tune...)
	return h
}

func newTokenFixture(t *testing.T, tune ...func(*Config)) (http.Handler, statusSecrets) {
	t.Helper()
	var secrets statusSecrets
	options := []func(*Config){func(c *Config) {
		c.Tokens, secrets = seedTokens(t, c.Store)
		var err error
		c.Auth, err = authn.NewAuthenticator(c.Store.Users(), c.Store.Settings(), authn.Options{
			Hash: testHash, Tokens: c.Tokens, APIRestrictions: c.Store.APIRestrictions(),
		})
		if err != nil {
			t.Fatal(err)
		}
		c.PasswordHash = testHash
		libs, err := c.Store.Repos().Get(t.Context(), "libs-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = (&item.Service{
			Store: c.Store, Blobs: c.Blobs, Formats: ecosystem.For,
		}).Deploy(t.Context(), item.Deploy{
			Target: item.Target{
				Repo: libs,
				Path: "fixture/get.txt",
			},
			Body: strings.NewReader("fixture"),
		}); err != nil {
			t.Fatal(err)
		}
	}}
	options = append(options, tune...)
	w := newItemWorld(t, false, options...)
	return w.h, secrets
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

// changeBody is a legacy password change of user from old to next.
func changeBody(user, old, next string) string {
	return fmt.Sprintf(`{"userName":%q,"oldPassword":%q,"newPassword1":%q,"newPassword2":%q}`,
		user, old, next, next)
}

func restrictionBody() string {
	return fmt.Sprintf(
		`{"until":%q,"reason":"Stop expensive polling"}`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	)
}

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
