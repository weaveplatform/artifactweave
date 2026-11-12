package npm_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

// ecdsaKeyPairBody is the security API's create body for a fresh P-256
// pair called name, its public half publicKey unless that is given.
func ecdsaKeyPairBody(t *testing.T, name, pairType, publicKey string) (string, *ecdsa.PublicKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if publicKey == "" {
		pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		publicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	}
	b, err := json.Marshal(map[string]string{
		"pairName": name, "pairType": pairType, "alias": name,
		"privateKey": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"publicKey":  publicKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b), &k.PublicKey
}

// npmVersionDist is the dist of version in name's full packument.
type npmVersionDist struct {
	Integrity  string `json:"integrity"`
	Signatures []struct {
		KeyID string `json:"keyid"`
		Sig   string `json:"sig"`
	} `json:"signatures"`
}

func (w *itemWorld) npmDist(name, version string) npmVersionDist {
	w.t.Helper()
	rec := w.do("GET", npmBase+"npm-local/"+name, "admin", "")
	wantStatus(w.t, rec, http.StatusOK)
	var doc struct {
		Versions map[string]struct {
			Dist npmVersionDist `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		w.t.Fatal(err)
	}
	return doc.Versions[version].Dist
}

// verified reports whether d carries one signature by pub over what pacote
// verifies, "<name>@<version>:<integrity>".
func verified(d npmVersionDist, pub *ecdsa.PublicKey, name, version string) bool {
	if len(d.Signatures) != 1 {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(d.Signatures[0].Sig)
	digest := sha256.Sum256([]byte(name + "@" + version + ":" + d.Integrity))
	return err == nil && ecdsa.VerifyASN1(pub, digest[:], sig)
}

// An ECDSA pair is created through the security API, and naming it as an
// npm repository's primary pair through the repositories API queues the
// reindex that signs what the repository stored before; a version published
// afterwards is signed at once, and the keys endpoint lists the pair's key.
// It runs on the real router, so it covers the key pair's 201 and 400s, the
// update's queued reindex and the registry's answers together.
func TestAccNpmSigningThroughTheAPIs(t *testing.T) {
	ctx := context.Background()
	w, q := npmOpsWorld(t)
	const keypairs = "/artifactory/api/security/keypair"
	body, pub := ecdsaKeyPairBody(t, "npm-signing", "ECDSA", "")
	rec := w.do("POST", keypairs, "admin", body)
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ pairType, public, text string }{
		{"ECDSA", "nothing", "the public key is not PEM"},
		{"DSA", "", `pairType must be RSA, GPG, Ed25519 or ECDSA, not \"DSA\"`},
	} {
		body, _ := ecdsaKeyPairBody(t, "npm-bad", c.pairType, c.public)
		rec := w.do("POST", keypairs, "admin", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.text) {
			t.Errorf("%s: %d %s", c.pairType, rec.Code, rec.Body)
		}
	}

	wantStatus(t, w.do("PUT", "/artifactory/npm-local/"+npmfmt.CanonicalPath("left-pad", "1.0.0"),
		"admin", string(npmTarballOf("left-pad", "1.0.0"))), http.StatusCreated)
	if d := w.npmDist("left-pad", "1.0.0"); d.Signatures != nil {
		t.Fatalf("signed with no pair named: %+v", d)
	}
	wantStatus(t, w.do("GET", npmBase+"npm-local/-/npm/v1/keys", "admin", ""), http.StatusNotFound)

	rec = w.do("POST", "/artifactory/api/repositories/npm-local", "admin",
		`{"primaryKeyPairRef":"npm-signing"}`)
	wantStatus(t, rec, http.StatusOK)
	if err := q.EnsureFresh(ctx, format.KindReindex, "npm-local"); err != nil {
		t.Fatal(err)
	}
	if !verified(w.npmDist("left-pad", "1.0.0"), pub, "left-pad", "1.0.0") {
		t.Fatalf("not signed by the reindex: %+v", w.npmDist("left-pad", "1.0.0"))
	}
	wantStatus(t, w.do("PUT", "/artifactory/npm-local/"+npmfmt.CanonicalPath("left-pad", "1.0.1"),
		"admin", string(npmTarballOf("left-pad", "1.0.1"))), http.StatusCreated)
	if !verified(w.npmDist("left-pad", "1.0.1"), pub, "left-pad", "1.0.1") {
		t.Fatalf("not signed on publish: %+v", w.npmDist("left-pad", "1.0.1"))
	}

	rec = w.do("GET", npmBase+"npm-local/-/npm/v1/keys", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	want, err := npmfmt.PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Keys []npmfmt.Key }
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 1 || doc.Keys[0] != want ||
		w.npmDist("left-pad", "1.0.1").Signatures[0].KeyID != want.KeyID {
		t.Fatalf("keys %s, want %+v", rec.Body, want)
	}
	// The keys document needs read permission as a packument does: Manage
	// alone, which deployer holds, does not grant it.
	for _, as := range []string{"deployer", "user", ""} {
		keys := w.do("GET", npmBase+"npm-local/-/npm/v1/keys", as, "")
		packument := w.do("GET", npmBase+"npm-local/left-pad", as, "")
		if keys.Code == http.StatusOK || keys.Code != packument.Code {
			t.Errorf("as %q: keys %d, packument %d", as, keys.Code, packument.Code)
		}
	}
}
