package hex

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

// These are unchanged signed registry bytes and public key from repo.hex.pm.
// They exercise the current wire format independently of our encoder.
func TestAuthenticHexRegistryPackage(t *testing.T) {
	public, err := os.ReadFile(filepath.Join("testdata", "hexpm-public-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParsePublicKey(string(public))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "registry-decimal.bin"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := DecodePackage(body, key)
	if err != nil || reg.Name != "decimal" || reg.Repository != "hexpm" || len(reg.Releases) < 1 {
		t.Fatalf("authentic registry: %+v: %v", reg, err)
	}
	for _, release := range reg.Releases {
		if release.Version == "2.0.0" {
			archive, err := os.ReadFile(filepath.Join("testdata", "decimal-2.0.0.tar"))
			if err != nil {
				t.Fatal(err)
			}
			pkg, err := InspectTarball(bytes.NewReader(archive))
			if err != nil || pkg.InnerChecksum != release.InnerChecksum ||
				pkg.OuterChecksum != release.OuterChecksum {
				t.Fatalf("authentic release checksum: %v", err)
			}
			return
		}
	}
	t.Fatal("decimal 2.0.0 absent")
}

func TestSignedRegistryRoundTrip(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "decimal-2.0.0.tar"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := InspectTarball(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	key := testKey()
	public, err := ParsePublicKey(PublicKeyPEM(key))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := EncodePackage(RegistryPackage{
		Name: "decimal", Repository: "hex-local", Releases: []Package{pkg},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePackage(signed, public)
	if err != nil || decoded.Name != "decimal" || decoded.Repository != "hex-local" ||
		len(decoded.Releases) != 1 || decoded.Releases[0].OuterChecksum != pkg.OuterChecksum {
		t.Fatalf("signed package mismatch: %+v, %v", decoded, err)
	}
	wrong := *public
	wrong.E = 3
	if _, err := DecodePackage(signed, &wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := DecodePackage(signed[:len(signed)-5], public); err == nil {
		t.Fatal("truncated registry accepted")
	}
	if _, err := EncodeNames("hex-local", []string{"decimal"}, key); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeVersions("hex-local", []RegistryPackage{{
		Name: "decimal", Releases: []Package{pkg},
	}}, key); err != nil {
		t.Fatal(err)
	}
}
