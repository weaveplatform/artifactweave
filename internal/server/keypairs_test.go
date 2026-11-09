package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func decodeJSON[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// TestAccKeypairTerraformFlow walks what the artifactory_keypair resource
// does: create (201, an empty object), read back, list, delete, read again.
func TestAccKeypairTerraformFlow(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	k := testKeys()
	rec := do(t, h, http.MethodPost, "/artifactory/api/security/keypair", admin,
		keypairBody("tf-pair", "tf-alias", k.public))
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/artifactory/api/security/keypair/tf-pair", admin, "")
	got := decodeJSON[map[string]any](t, rec.Body.String())
	if rec.Code != http.StatusOK || got["pairName"] != "tf-pair" || got["pairType"] != "RSA" ||
		got["alias"] != "tf-alias" || got["publicKey"] != k.public || len(got) != 4 {
		t.Fatalf("read: %d %v", rec.Code, got)
	}
	rec = do(t, h, http.MethodGet, "/artifactory/api/security/keypair", admin, "")
	if !strings.Contains(rec.Body.String(), `"pairName":"tf-pair"`) ||
		strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatalf("list: %s", rec.Body)
	}
	rec = do(t, h, http.MethodDelete, "/artifactory/api/security/keypair/tf-pair", admin, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/security/keypair/tf-pair",
		admin,
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("read after delete: %d", rec.Code)
	}
}

func TestAccKeypairReferences(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	admin["Content-Type"] = "application/json"
	// A repository may only reference an existing pair, on create and update.
	rec := do(t, h, http.MethodPut, "/artifactory/api/repositories/signing", admin,
		`{"rclass":"local","packageType":"alpine","primaryKeyPairRef":"absent"}`)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "Key pair 'absent' does not exist") {
		t.Fatalf("create with a missing pair: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPut, "/artifactory/api/repositories/signing", admin,
		`{"rclass":"local","packageType":"alpine","primaryKeyPairRef":"kp-used"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create with an existing pair: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/artifactory/api/repositories/signing", admin,
		`{"secondaryKeyPairRef":"absent"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update with a missing pair: %d %s", rec.Code, rec.Body)
	}
	rec = do(
		t,
		h,
		http.MethodPut,
		"/artifactory/api/v2/repositories/batch",
		admin,
		`[{"key":"batch-signed","rclass":"local","packageType":"alpine","primaryKeyPairRef":"absent"}]`,
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("batch create with a missing pair: %d %s", rec.Code, rec.Body)
	}
	// The public key a package client fetches is the primary pair's.
	rec = do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/security/keypair/public/repositories/signing",
		admin,
		"",
	)
	if rec.Code != http.StatusOK || rec.Body.String() != testKeys().public {
		t.Fatalf("public key: %d %s", rec.Code, rec.Body)
	}
	// As an attachment named after the pair's alias, for curl -O -J (#473).
	if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename=used.rsa.pub" {
		t.Fatalf("public key disposition %q", got)
	}
	// Deleting a pair in use succeeds, and clears it from the repositories
	// that used it, which then update as any other (#473).
	rec = do(t, h, http.MethodDelete, "/artifactory/api/security/keypair/kp-used", admin, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
		t.Fatalf("delete a pair in use: %d %s", rec.Code, rec.Body)
	}
	for _, key := range []string{"signing", "signed-local"} {
		rec = do(t, h, http.MethodGet, "/artifactory/api/repositories/"+key, admin, "")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "KeyPairRef") {
			t.Fatalf("%s after the delete: %d %s", key, rec.Code, rec.Body)
		}
	}
	rec = do(t, h, http.MethodPost, "/artifactory/api/repositories/signing", admin,
		`{"description":"unsigned"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update after the delete: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet,
		"/artifactory/api/security/keypair/public/repositories/signing", admin, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public key after the delete: %d %s", rec.Code, rec.Body)
	}
}

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

func TestAccCertificates(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	headers := map[string]string{"Content-Type": "application/text"}
	for k, v := range admin {
		headers[k] = v
	}
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/system/security/certificates/client",
		headers,
		testKeys().bundle,
	)
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Successfully added certificate 'client'") {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	// Adding to the same alias replaces it.
	headers["Content-Type"] = "text/plain"
	if rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/system/security/certificates/client",
		headers,
		testKeys().bundle,
	); rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/artifactory/api/system/security/certificates", admin, "")
	list := decodeJSON[[]map[string]any](t, rec.Body.String())
	var client map[string]any
	for _, c := range list {
		if c["certificateAlias"] == "client" {
			client = c
		}
	}
	if client == nil || client["issuedTo"] != "client.example.com" ||
		!strings.Contains(
			client["fingerPrint"].(string),
			":",
		) || //nolint:forcetypeassert // fixture
		strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatalf("list: %s", rec.Body)
	}
	for _, c := range []struct{ body, expected string }{
		{testKeys().public, "no certificate"},
		{strings.Split(testKeys().bundle, "-----BEGIN EC")[0], "no private key"},
		{strings.Repeat("x", maxCertificateBundle+1), "larger than"},
	} {
		rec := do(
			t,
			h,
			http.MethodPost,
			"/artifactory/api/system/security/certificates/bad",
			headers,
			c.body,
		)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.expected) {
			t.Errorf("add %q: %d %s", c.expected, rec.Code, rec.Body)
		}
	}
	rec = do(
		t,
		h,
		http.MethodDelete,
		"/artifactory/api/system/security/certificates/cert-used",
		admin,
		"",
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "remote-tls") {
		t.Fatalf("delete a certificate in use: %d %s", rec.Code, rec.Body)
	}
}

func TestAccKeypairVaultKeyRefused(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodPost, "/artifactory/api/security/keypair", basic("admin", "pw"),
		`{"pairName":"v","pairType":"RSA","alias":"v","publicKey":"x","vaultKey":"vault/key"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Vault keys") {
		t.Fatalf("vault key: %d %s", rec.Code, rec.Body)
	}
}

// TestAccKeypairCreateReplaces: creating a pair under a name in use replaces
// it, as 7.x does (createkeypair), and the repository that refers to the
// pair by name signs with the new material.
func TestAccKeypairCreateReplaces(t *testing.T) {
	var db store.Store
	h := statusFixture(t, false, func(c *Config) { db = c.Store })
	admin := basic("admin", "pw")
	k := testKeys()
	body, err := json.Marshal(map[string]string{
		"pairName": "kp-used", "pairType": "RSA", "alias": "rotated",
		"privateKey": k.otherPrivate, "publicKey": k.otherPublic,
	})
	if err != nil {
		t.Fatal(err)
	}
	const kp = "/artifactory/api/security/keypair"
	rec := do(t, h, http.MethodPost, kp, admin, string(body))
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("create over kp-used: %d %s", rec.Code, rec.Body)
	}
	got := decodeJSON[map[string]any](
		t, do(t, h, http.MethodGet, kp+"/kp-used", admin, "").Body.String(),
	)
	if got["alias"] != "rotated" || got["publicKey"] != k.otherPublic {
		t.Fatalf("read after replace: %v", got)
	}
	stored, err := db.Keys().Keypair(context.Background(), "kp-used")
	if err != nil || stored.PrivateKey != k.otherPrivate {
		t.Fatalf("the stored private key is not the new one: %v", err)
	}
	list := decodeJSON[[]map[string]any](t, do(t, h, http.MethodGet, kp, admin, "").Body.String())
	n := 0
	for _, p := range list {
		if p["pairName"] == "kp-used" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("kp-used is listed %d times: %v", n, list)
	}
	rec = do(t, h, http.MethodGet, kp+"/public/repositories/signed-local", admin, "")
	if rec.Code != http.StatusOK || rec.Body.String() != k.otherPublic ||
		rec.Header().Get("Content-Disposition") != "attachment; filename=rotated.rsa.pub" {
		t.Fatalf("signed-local's public key after replace: %d %v %s",
			rec.Code, rec.Header(), rec.Body)
	}
}

func TestAccKeypairVerifyText(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodPost, "/artifactory/api/security/keypair/verify",
		basic("admin", "pw"), keypairBody("check", "check", testKeys().public))
	if rec.Code != http.StatusOK || rec.Body.String() != "Key was verified." {
		t.Fatalf("verify: %d %q", rec.Code, rec.Body)
	}
}

// TestAccKeypairReadByNonAdmin: a user and anonymous read one pair and a
// repository's public key, and never the private key or the passphrase.
func TestAccKeypairReadByNonAdmin(t *testing.T) {
	var db store.Store
	h := statusFixture(t, true, func(c *Config) { db = c.Store })
	k := testKeys()
	const passphrase = "passphrase-never-read"
	if err := db.Keys().CreateKeypair(context.Background(), types.Keypair{
		Name: "kp-secret", Type: types.KeypairRSA, Alias: "secret",
		PrivateKey: k.private, PublicKey: k.public, Passphrase: passphrase,
	}); err != nil {
		t.Fatal(err)
	}
	for name, headers := range map[string]map[string]string{
		"user": basic("user", "pw"), "anonymous": nil,
	} {
		rec := do(t, h, http.MethodGet, "/artifactory/api/security/keypair/kp-secret", headers, "")
		got := decodeJSON[map[string]any](t, rec.Body.String())
		if rec.Code != http.StatusOK || len(got) != 4 || got["pairName"] != "kp-secret" ||
			got["pairType"] != "RSA" || got["alias"] != "secret" || got["publicKey"] != k.public {
			t.Errorf("%s reads the pair: %d %v", name, rec.Code, got)
		}
		if body := rec.Body.String(); strings.Contains(body, "PRIVATE KEY") ||
			strings.Contains(body, passphrase) {
			t.Errorf("%s reads private material: %s", name, body)
		}
		rec = do(t, h, http.MethodGet,
			"/artifactory/api/security/keypair/public/repositories/signed-local", headers, "")
		if rec.Code != http.StatusOK || rec.Body.String() != k.public {
			t.Errorf("%s reads signed-local's public key: %d %s", name, rec.Code, rec.Body)
		}
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
		if err := dropKeypairRefs(ctx, broken, "kp"); !errors.Is(err, errBoom) {
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
	if err := dropKeypairRefs(ctx, db.Repos(), "kp"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][2]string{"both": {"", ""}, "second": {"other", ""}} {
		c, err := db.Repos().Get(ctx, key)
		if err != nil || c.PrimaryKeyPairRef != want[0] || c.SecondaryKeyPairRef != want[1] {
			t.Errorf("%s: %q %q, %v", key, c.PrimaryKeyPairRef, c.SecondaryKeyPairRef, err)
		}
	}
}
