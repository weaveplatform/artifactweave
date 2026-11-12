package npm_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ecdsaPair is a freshly generated ECDSA key pair called name, as the
// security API stores one. Keys are generated, never committed.
func ecdsaPair(t *testing.T, name string, curve elliptic.Curve) (accessapi.Keypair, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return accessapi.Keypair{
		Name: name, Type: accessapi.KeypairECDSA, Alias: name,
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		PublicKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})),
	}, k
}

// signing gives the world's item service keys and makes primary the
// repository's primary key pair.
func (w *world) signing(keys *accessapitest.Keys, primary string) {
	w.t.Helper()
	w.items.Keys = keys
	w.c.PrimaryKeyPairRef = primary
	if err := w.st.Repos().Update(w.ctx, w.c); err != nil {
		w.t.Fatal(err)
	}
}

// verifies reports whether sig is pub's signature of message, as pacote
// checks it: SHA-256 and an ASN.1 DER signature in base64.
func verifies(pub *ecdsa.PublicKey, message string, sig npm.Signature) bool {
	raw, err := base64.StdEncoding.DecodeString(sig.Sig)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(message))
	return ecdsa.VerifyASN1(pub, digest[:], raw)
}

func keyID(t *testing.T, pub *ecdsa.PublicKey) string {
	t.Helper()
	k, err := npm.PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k.KeyID
}

// A version published to a repository whose primary pair is ECDSA carries
// one signature of "<name>@<version>:<integrity>" by that pair, which a
// redeploy of the same content gives again byte for byte (RFC 6979), and a
// dist-tag change keeps.
func TestUnitSignOnPublish(t *testing.T) {
	w := newWorld(t)
	pair, key := ecdsaPair(t, "npm-ecdsa", elliptic.P256())
	w.signing(&accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{pair.Name: pair}}, pair.Name)
	p := "@acme/left-pad/-/@acme/left-pad-1.3.0.tgz"
	w.deploy(p, pkg("@acme/left-pad", "v1.3.0", ""), nil)
	m := w.index()["@acme/left-pad"]["1.3.0"]
	if len(m.Dist.Signatures) != 1 {
		t.Fatalf("signatures %+v", m.Dist.Signatures)
	}
	sig := m.Dist.Signatures[0]
	if sig.KeyID != keyID(t, &key.PublicKey) || !strings.HasPrefix(sig.KeyID, "SHA256:") {
		t.Fatalf("keyid %q", sig.KeyID)
	}
	// The cleaned version, as the version's _id names it.
	if !verifies(&key.PublicKey, "@acme/left-pad@1.3.0:"+m.Dist.Integrity, sig) {
		t.Fatalf("signature %q does not verify", sig.Sig)
	}
	w.deploy(p, pkg("@acme/left-pad", "v1.3.0", ""), nil)
	w.annotate(p, model.Properties{npm.PropDistTag: {"next"}})
	again := w.index()["@acme/left-pad"]["1.3.0"]
	if diff := cmp.Diff(m.Dist, again.Dist); diff != "" {
		t.Fatalf("dist after a redeploy and a dist-tag (-first +now):\n%s", diff)
	}
}

// A repository signs nothing, and stores the Metadata it stored before
// signing existed, when its primary pair is absent, of another type, or
// missing from the store, when it is not local or federated, and when the
// item service has no key store.
func TestUnitSignOff(t *testing.T) {
	ecdsaKey, _ := ecdsaPair(t, "ecdsa", elliptic.P256())
	gpg := accessapi.Keypair{Name: "gpg", Type: accessapi.KeypairGPG}
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"ecdsa": ecdsaKey, "gpg": gpg}}
	base := newWorld(t)
	base.deploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", ""), nil)
	want := base.index()
	for name, tc := range map[string]struct {
		noKeys             bool
		primary, secondary string
	}{
		"no primary":     {false, "", ""},
		"GPG primary":    {false, "gpg", ""},
		"missing pair":   {false, "gone", ""},
		"no key store":   {true, "ecdsa", ""},
		"secondary only": {false, "", "ecdsa"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.c.SecondaryKeyPairRef = tc.secondary
			w.signing(keys, tc.primary)
			if tc.noKeys {
				w.items.Keys = nil
			}
			w.deploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", ""), nil)
			if diff := cmp.Diff(want, w.index()); diff != "" {
				t.Fatalf("index (-unsigned +got):\n%s", diff)
			}
		})
	}
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		c := repo.Config{Key: "r", Class: class, PackageType: repo.NPM, PrimaryKeyPairRef: "ecdsa"}
		s, err := npm.SignerFor(context.Background(), keys, c)
		if err != nil || s != nil {
			t.Fatalf("%s: signer %v, %v", class, s, err)
		}
		listed, err := npm.Keys(context.Background(), keys, c)
		if err != nil || listed != nil {
			t.Fatalf("%s: keys %v, %v", class, listed, err)
		}
	}
	var none *npm.Signer
	if sigs, err := none.Sign("a", "1.0.0", "sha512-x"); sigs != nil || err != nil {
		t.Fatalf("nil signer: %v, %v", sigs, err)
	}
}

// A reindex signs every version with the primary pair as it now is: a new
// pair replaces the old signatures, the same pair writes the same bytes,
// and clearing the pair removes them.
func TestUnitReindexSigns(t *testing.T) {
	w := newWorld(t)
	w.deploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", ""), nil)
	w.deploy(canonical("2.0.0"), pkg("left-pad", "2.0.0", ""), nil)
	unsigned := w.index()
	first, firstKey := ecdsaPair(t, "first", elliptic.P256())
	second, secondKey := ecdsaPair(t, "second", elliptic.P384())
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"first": first, "second": second}}
	reindex := func(primary string) map[string]map[string]npm.Metadata {
		t.Helper()
		w.signing(keys, primary)
		if err := w.run(format.KindReindex, ""); err != nil {
			t.Fatal(err)
		}
		return w.index()
	}
	for _, step := range []struct {
		primary string
		key     *ecdsa.PrivateKey
	}{{"first", firstKey}, {"second", secondKey}} {
		got := reindex(step.primary)
		for v, m := range got["left-pad"] {
			if len(m.Dist.Signatures) != 1 || m.Dist.Signatures[0].KeyID != keyID(t, &step.key.PublicKey) ||
				!verifies(&step.key.PublicKey, "left-pad@"+v+":"+m.Dist.Integrity, m.Dist.Signatures[0]) {
				t.Fatalf("%s: %s signatures %+v", step.primary, v, m.Dist.Signatures)
			}
		}
		if diff := cmp.Diff(got, reindex(step.primary)); diff != "" {
			t.Fatalf("%s: a second reindex (-first +second):\n%s", step.primary, diff)
		}
	}
	if diff := cmp.Diff(unsigned, reindex("")); diff != "" {
		t.Fatalf("reindex without a pair (-unsigned +got):\n%s", diff)
	}
}

// A key store that cannot be read, or a stored ECDSA pair whose private key
// no longer opens, fails the write rather than storing an unsigned version
// of a signing repository.
func TestUnitSignFaults(t *testing.T) {
	broken, _ := ecdsaPair(t, "broken", elliptic.P256())
	broken.PrivateKey = "not a key"
	boom := errors.New("boom")
	for name, keys := range map[string]*accessapitest.Keys{
		"store":       {Err: boom},
		"private key": {Keypairs: map[string]accessapi.Keypair{"broken": broken}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.signing(keys, "broken")
			if _, err := w.tryDeploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", ""), nil); err == nil {
				t.Fatal("deploy succeeded")
			}
			if len(w.index()) != 0 {
				t.Fatalf("indexed %v", w.index())
			}
			if err := w.run(format.KindReindex, ""); err == nil {
				t.Fatal("reindex succeeded")
			}
		})
	}
}

// The keys endpoint lists the primary and then the secondary pair, once
// each, leaving out pairs that are not ECDSA or no longer exist.
func TestUnitKeys(t *testing.T) {
	first, firstKey := ecdsaPair(t, "first", elliptic.P256())
	second, secondKey := ecdsaPair(t, "second", elliptic.P521())
	gpg := accessapi.Keypair{Name: "gpg", Type: accessapi.KeypairGPG}
	badPublic := first
	badPublic.Name, badPublic.PublicKey = "bad", "not a key"
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{
		"first": first, "second": second, "gpg": gpg, "bad": badPublic,
	}}
	ctx := context.Background()
	want := func(ks ...*ecdsa.PrivateKey) []npm.Key {
		var out []npm.Key
		for _, k := range ks {
			pub, err := npm.PublicKey(&k.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, pub)
		}
		return out
	}
	for name, tc := range map[string]struct {
		class              repo.Class
		primary, secondary string
		want               []npm.Key
	}{
		"primary only":       {repo.Local, "first", "", want(firstKey)},
		"both":               {repo.Federated, "first", "second", want(firstKey, secondKey)},
		"secondary only":     {repo.Local, "", "second", want(secondKey)},
		"same pair twice":    {repo.Local, "first", "first", want(firstKey)},
		"GPG primary":        {repo.Local, "gpg", "second", want(secondKey)},
		"missing secondary":  {repo.Local, "first", "gone", want(firstKey)},
		"none":               {repo.Local, "", "", nil},
		"GPG and missing":    {repo.Local, "gpg", "gone", nil},
		"remote passes none": {repo.Remote, "first", "", nil},
	} {
		t.Run(name, func(t *testing.T) {
			c := repo.Config{
				Key: "n", Class: tc.class, PackageType: repo.NPM,
				PrimaryKeyPairRef: tc.primary, SecondaryKeyPairRef: tc.secondary,
			}
			got, err := npm.Keys(ctx, keys, c)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Fatalf("keys (-want +got):\n%s", diff)
			}
		})
	}
	for name, k := range map[string]*accessapitest.Keys{
		"store": {Err: errors.New("boom")}, "public key": keys,
	} {
		c := repo.Config{Key: "n", Class: repo.Local, PackageType: repo.NPM, PrimaryKeyPairRef: "bad"}
		if _, err := npm.Keys(ctx, k, c); err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got, err := npm.Keys(ctx, nil, repo.Config{
		Class: repo.Local, PackageType: repo.NPM, PrimaryKeyPairRef: "first",
	}); got != nil || err != nil {
		t.Fatalf("no key store: %v, %v", got, err)
	}
}

// npm.PublicKey names a key as registry.npmjs.org names its own: this is
// its current signing key from https://registry.npmjs.org/-/npm/v1/keys,
// fetched on 2026-11-12, whose keyid is the key's OpenSSH SHA-256
// fingerprint.
func TestUnitPublicKeyAsNPMRegistry(t *testing.T) {
	const spki = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEY6Ya7W++7aUPzvMTrezH6Ycx3c+HOKYCcNGybJZSCJq/" +
		"fd7Qa8uuAKtdIkUQtQiEKERhAmE5lMMJhP8OkDOa2g=="
	der, err := base64.StdEncoding.DecodeString(spki)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("key %T", parsed)
	}
	got, err := npm.PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	want := npm.Key{
		KeyID:   "SHA256:DhQ8wR5APBvFHLF/+Tc+AYvPOdTpcIDqOhxsBHRwC7U",
		KeyType: "ecdsa-sha2-nistp256", Scheme: "ecdsa-sha2-nistp256", Key: spki,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("key (-npm registry +got):\n%s", diff)
	}
	if !bytes.Equal(der, mustDecode(t, got.Key)) {
		t.Fatal("key bytes differ")
	}
	// A curve npm's keys document cannot name is refused; the key pair API
	// accepts none (keymaterial).
	_, p224 := ecdsaPair(t, "p224", elliptic.P224())
	if _, err := npm.PublicKey(&p224.PublicKey); err == nil {
		t.Fatal("P-224 key named")
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
