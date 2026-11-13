package npmhttp_test

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
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	npmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// keysPath is the keys document npm audit signatures reads.
const keysPath = registry + "-/npm/v1/keys"

// ecdsaPair is a freshly generated P-256 key pair called name, as the
// security API stores one. Keys are generated, never committed.
func ecdsaPair(t *testing.T, name string) (accessapi.Keypair, *ecdsa.PublicKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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
	}, &k.PublicKey
}

// sign makes npm-local's primary and secondary pairs those named, the
// fixture's item service and registry reading them from keys.
func (f *fixture) sign(t *testing.T, keys accessapi.Keys, primary, secondary string) {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	c.PrimaryKeyPairRef, c.SecondaryKeyPairRef = primary, secondary
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.items.Keys = keys
	f.remount(t, npmhttp.Options{KeyPairs: keys})
}

// signedReads are the reads whose dist a signing repository signs, and the
// keys document beside them.
var signedReads = []struct{ path, accept string }{
	{"rich", ""},
	{"rich", abbreviatedAccept},
	{"rich/1.1.0", ""},
	{"rich/beta", ""},
	{"-/package/rich/dist-tags", ""},
	{"-/npm/v1/keys", ""},
}

// answer is what a read answered, byte for byte, with the headers a client
// keys on.
type answer struct {
	Status                  int
	ContentType, ETag, SHA1 string
	Body                    string
	// NotModified is whether the answer's own ETag gets a 304.
	NotModified bool
}

func (f *fixture) answers(t *testing.T) map[string]answer {
	t.Helper()
	out := map[string]answer{}
	for _, read := range signedReads {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := f.do(method, registry+read.path, "reader", "Accept", read.accept)
			a := answer{
				Status: rec.Code, ContentType: rec.Header().Get("Content-Type"),
				ETag: rec.Header().Get("ETag"), SHA1: rec.Header().Get("X-Checksum-Sha1"),
				Body: rec.Body.String(),
			}
			if a.ETag != "" {
				a.NotModified = f.do(method, registry+read.path, "reader", "Accept", read.accept,
					"If-None-Match", a.ETag).Code == http.StatusNotModified
			}
			out[method+" "+read.path+" "+read.accept] = a
		}
	}
	return out
}

// A repository that names no ECDSA key pair answers every read byte for
// byte as it did before signing existed: the packuments, a version's
// manifest, the dist-tags with their ETags and 304s, and the keys path's
// 404. The answers without a key store are those of main's goldens
// (TestAccFullPackument, TestAccAbbreviatedPackument, TestAccVersionManifest
// and TestAccPackumentETag pass unchanged); each configuration below must
// give exactly them.
func TestAccSigningOffAnswersAsBefore(t *testing.T) {
	before := newFixture(t)
	before.deployRich(t)
	want := before.answers(t)
	// main's answer at the keys path: a path under /-/ that names no
	// package.
	keys404 := `{"error":"not found: GET ` + keysPath + `"}`
	wantAnswer(t, before.do(http.MethodGet, keysPath, "reader"), http.StatusNotFound, keys404)
	for k, a := range want {
		if strings.HasPrefix(k, "GET ") && strings.Contains(k, "rich") &&
			a.Status != http.StatusOK {
			t.Fatalf("%s: %+v", k, a)
		}
	}

	gpg := accessapi.Keypair{Name: "gpg", Type: accessapi.KeypairGPG, PublicKey: "armored"}
	ecdsaKey, _ := ecdsaPair(t, "ecdsa")
	keys := &accessapitest.Keys{
		Keypairs: map[string]accessapi.Keypair{"gpg": gpg, "ecdsa": ecdsaKey},
	}
	for name, refs := range map[string][2]string{
		"no pairs":          {"", ""},
		"GPG primary":       {"gpg", ""},
		"GPG pairs":         {"gpg", "gpg"},
		"pair since gone":   {"gone", "gone"},
		"key store unwired": {"ecdsa", "ecdsa"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if name == "key store unwired" {
				f.sign(t, nil, refs[0], refs[1])
			} else {
				f.sign(t, keys, refs[0], refs[1])
			}
			f.deployRich(t)
			if diff := cmp.Diff(want, f.answers(t)); diff != "" {
				t.Fatalf("answers (-before signing +now):\n%s", diff)
			}
		})
	}
}

// signatures is the dist.signatures of version in a packument or manifest
// body, with the raw dist so its field order can be checked.
func signatures(t *testing.T, version json.RawMessage) ([]npmfmt.Signature, string) {
	t.Helper()
	var v struct {
		Dist json.RawMessage `json:"dist"`
	}
	if err := json.Unmarshal(version, &v); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Signatures []npmfmt.Signature `json:"signatures"`
	}
	if err := json.Unmarshal(v.Dist, &d); err != nil {
		t.Fatal(err)
	}
	return d.Signatures, string(v.Dist)
}

// checkSigned fails unless version, rich at number, carries one signature
// by pub of "rich@<number>:<integrity>" after the dist's other fields, as
// pacote verifies it.
func checkSigned(t *testing.T, number string, version json.RawMessage, pub *ecdsa.PublicKey) {
	t.Helper()
	sigs, raw := signatures(t, version)
	var d struct {
		Integrity string `json:"integrity"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	want, err := npmfmt.PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 || sigs[0].KeyID != want.KeyID {
		t.Fatalf("%s: signatures %+v, want one by %s", number, sigs, want.KeyID)
	}
	der, err := base64.StdEncoding.DecodeString(sigs[0].Sig)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("rich@" + number + ":" + d.Integrity))
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		t.Fatalf("%s: signature does not verify", number)
	}
	if !strings.HasPrefix(raw, `{"tarball":`) || !strings.Contains(raw, `,"integrity":"`+
		d.Integrity+`","signatures":[{"keyid":"`+want.KeyID+`","sig":"`) {
		t.Fatalf("%s: dist %s", number, raw)
	}
}

// Every version of a repository whose primary pair is ECDSA carries its
// signature in dist.signatures, in the full and the abbreviated packument
// and in a version's manifest, after tarball, shasum and integrity.
func TestAccSignedPackument(t *testing.T) {
	f := newFixture(t)
	pair, pub := ecdsaPair(t, "npm-ecdsa")
	f.sign(t, &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{pair.Name: pair}},
		pair.Name, "")
	f.deployRich(t)
	for _, accept := range []string{"", abbreviatedAccept} {
		rec := f.do(http.MethodGet, registry+"rich", "reader", "Accept", accept)
		wantStatus(t, rec, http.StatusOK)
		var doc struct {
			Versions map[string]json.RawMessage `json:"versions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Versions) != 3 {
			t.Fatalf("versions %v", doc.Versions)
		}
		for number, v := range doc.Versions {
			checkSigned(t, number, v, pub)
		}
	}
	for spec, number := range map[string]string{"1.1.0": "1.1.0", "beta": "2.0.0-beta.1"} {
		rec := f.do(http.MethodGet, registry+"rich/"+spec, "reader")
		wantStatus(t, rec, http.StatusOK)
		checkSigned(t, number, rec.Body.Bytes(), pub)
	}
}

// The keys document lists the primary and then the secondary ECDSA pair as
// npm's registry lists its keys, to a caller that may read .npm/keys.json,
// where 7.x keeps it.
func TestAccKeysDocument(t *testing.T) {
	f := newFixture(t)
	primary, primaryPub := ecdsaPair(t, "primary")
	secondary, secondaryPub := ecdsaPair(t, "secondary")
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{
		"primary": primary, "secondary": secondary,
	}}
	f.sign(t, keys, "primary", "secondary")
	var listed []string
	for _, pub := range []*ecdsa.PublicKey{primaryPub, secondaryPub} {
		k, err := npmfmt.PublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		if k.Key != base64.StdEncoding.EncodeToString(der) ||
			!strings.HasPrefix(k.KeyID, "SHA256:") {
			t.Fatalf("key %+v", k)
		}
		listed = append(listed, `{"expires":null,"keyid":"`+k.KeyID+
			`","keytype":"ecdsa-sha2-nistp256","scheme":"ecdsa-sha2-nistp256","key":"`+k.Key+`"}`)
	}
	want := `{"keys":[` + strings.Join(listed, ",") + `]}`
	for _, user := range []string{"reader", "admin"} {
		rec := f.do(http.MethodGet, keysPath, user)
		wantAnswer(t, rec, http.StatusOK, want)
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type %q", ct)
		}
	}
	rec := f.do(http.MethodHead, keysPath, "reader")
	wantStatus(t, rec, http.StatusOK)
	// Read permission first, then the download policy (DV-057). partial
	// reads only mixed's 1.x tarballs, so not .npm/keys.json.
	for user, status := range map[string]int{
		"nobody": http.StatusForbidden, "partial": http.StatusForbidden,
		kernel.AnonymousUser: http.StatusUnauthorized, "": http.StatusUnauthorized,
	} {
		rec := f.do(http.MethodGet, keysPath, user)
		wantStatus(t, rec, status)
		if decode(t, rec)["error"] == nil {
			t.Errorf("%q: not npm's envelope: %s", user, rec.Body)
		}
	}
	wantAnswer(t, f.do(http.MethodGet, keysPath, "nobody"), http.StatusForbidden,
		`{"error":"Forbidden"}`)
	f.setPolicy(t, "npm-local", true, "", "")
	for _, user := range []string{"reader", "admin"} {
		wantAnswer(t, f.do(http.MethodGet, keysPath, user), http.StatusNotFound,
			blackedOut("npm-local", ".npm/keys.json"))
	}
	wantStatus(t, f.do(http.MethodGet, keysPath, "nobody"), http.StatusForbidden)
	// 7.x's documentation has an include pattern name .npm/keys.json for
	// npm audit signatures to work.
	for _, c := range []struct{ include, exclude string }{
		{"", ".npm/**"},
		{"rich/**", ""},
	} {
		f.setPolicy(t, "npm-local", false, c.include, c.exclude)
		wantAnswer(t, f.do(http.MethodGet, keysPath, "reader"), http.StatusNotFound,
			rejected("npm-local", ".npm/keys.json"))
	}
	f.setPolicy(t, "npm-local", false, "rich/**,.npm/keys.json", "")
	wantAnswer(t, f.do(http.MethodGet, keysPath, "reader"), http.StatusOK, want)
}

// The keys document of a repository whose primary pair is ECDSA, alone or
// with no secondary, lists that one key; a key store that cannot be read
// is a fault.
func TestAccKeysDocumentFaults(t *testing.T) {
	f := newFixture(t)
	pair, _ := ecdsaPair(t, "only")
	f.sign(t, &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"only": pair}}, "", "only")
	var doc struct {
		Keys []npmfmt.Key `json:"keys"`
	}
	rec := f.do(http.MethodGet, keysPath, "reader")
	wantStatus(t, rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Keys) != 1 {
		t.Fatalf("keys %s: %v", rec.Body, err)
	}
	broken := errors.New("key store down")
	f.sign(t, &accessapitest.Keys{Err: broken}, "only", "")
	f.faults = nil
	wantStatus(t, f.do(http.MethodGet, keysPath, "reader"), http.StatusInternalServerError)
	if len(f.faults) != 1 || !errors.Is(f.faults[0], broken) {
		t.Fatalf("faults %v", f.faults)
	}
	// A remote repository relays its upstream's signatures and keys
	// (remote_keys.go): the pairs it names change nothing.
	c, err := f.db.Repos().Get(context.Background(), "npm-remote")
	if err != nil {
		t.Fatal(err)
	}
	remoteKeys := "/artifactory/api/npm/npm-remote/-/npm/v1/keys"
	unsigned := f.do(http.MethodGet, remoteKeys, "reader")
	c.PrimaryKeyPairRef = "only"
	if err := f.db.Repos().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	f.sign(t, &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"only": pair}}, "", "")
	signed := f.do(http.MethodGet, remoteKeys, "reader")
	if signed.Code != unsigned.Code || signed.Body.String() != unsigned.Body.String() {
		t.Fatalf("remote keys path: %d %s, was %d %s",
			signed.Code, signed.Body, unsigned.Code, unsigned.Body)
	}
}
