package hex

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
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

func TestSignedRegistryRetirementAndAdvisoryRoundTrip(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "decimal-2.0.0.tar"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := InspectTarball(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var retirement []byte
	retirement = protowire.AppendTag(retirement, 1, protowire.VarintType)
	retirement = protowire.AppendVarint(retirement, 2)
	retirement = appendString(retirement, 2, "withdrawn")
	var published []byte
	published = protowire.AppendTag(published, 1, protowire.VarintType)
	published = protowire.AppendVarint(published, 1700000000)
	published = protowire.AppendTag(published, 2, protowire.VarintType)
	published = protowire.AppendVarint(published, 0)
	advisory := appendString(nil, 1, "GHSA-example")
	advisory = appendString(advisory, 2, "Example advisory")
	advisory = appendString(advisory, 3, "https://example.test/advisory")
	advisory = appendString(advisory, 6, "https://example.test/api")
	pkg.Retirement = retirement
	pkg.PublishedAt = published
	pkg.AdvisoryIndexes = []uint64{0}
	key := testKey()
	signed, err := EncodePackage(RegistryPackage{
		Name: "decimal", Repository: "hexpm", Releases: []Package{pkg},
		Advisories: [][]byte{advisory},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePackage(signed, &key.PublicKey)
	if err != nil || len(decoded.Releases) != 1 || len(decoded.Advisories) != 1 ||
		!bytes.Equal(decoded.Releases[0].Retirement, retirement) ||
		!bytes.Equal(decoded.Releases[0].PublishedAt, published) ||
		!bytes.Equal(decoded.Advisories[0], advisory) ||
		len(decoded.Releases[0].AdvisoryIndexes) != 1 ||
		decoded.Releases[0].AdvisoryIndexes[0] != 0 {
		t.Fatalf("registry annotations lost: %+v %v", decoded, err)
	}
}

func TestSignedNamesAndVersionsRejectDuplicateAndMalformedEntries(t *testing.T) {
	key := testKey()
	signedNames, err := EncodeNames("hexpm", []string{"telemetry", "decimal"}, key)
	if err != nil {
		t.Fatal(err)
	}
	repository, names, err := DecodeNames(signedNames, &key.PublicKey)
	if err != nil || repository != "hexpm" || len(names) != 2 || names[0] != "decimal" {
		t.Fatalf("names: %s %v %v", repository, names, err)
	}
	item := appendString(nil, 1, "decimal")
	for _, payload := range [][]byte{
		appendBytes(appendBytes(appendString(nil, 2, "hexpm"), 1, item), 1, item),
		appendBytes(appendString(nil, 2, "hexpm"), 1, appendString(nil, 1, "../bad")),
		appendBytes(nil, 1, item),
	} {
		signed, err := sign(payload, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := DecodeNames(signed, &key.PublicKey); err == nil {
			t.Fatal("accepted invalid names registry")
		}
	}
	versionItem := appendString(nil, 1, "decimal")
	versionItem = appendString(versionItem, 2, "2.0.0")
	for _, payload := range [][]byte{
		appendBytes(appendBytes(appendString(nil, 2, "hexpm"), 1, versionItem), 1, versionItem),
		appendBytes(appendString(nil, 2, "hexpm"), 1, appendString(versionItem, 2, "2.0.0")),
		appendBytes(appendString(nil, 2, "hexpm"), 1, appendString(nil, 1, "bad/name")),
	} {
		signed, err := sign(payload, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := DecodeVersions(signed, &key.PublicKey); err == nil {
			t.Fatal("accepted invalid versions registry")
		}
	}
}

func TestRegistryRejectsInvalidPublicKeyAndReleaseFields(t *testing.T) {
	for _, pem := range []string{"", "not a PEM key", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----"} {
		if _, err := ParsePublicKey(pem); err == nil {
			t.Fatalf("accepted invalid public key: %q", pem)
		}
	}
	key := testKey()
	for _, release := range [][]byte{
		appendString(nil, 1, "not-semver"),
		appendBytes(appendString(nil, 1, "1.0.0"), 2, []byte("short")),
	} {
		payload := appendBytes(nil, 1, release)
		payload = appendString(payload, 2, "decimal")
		payload = appendString(payload, 3, "hexpm")
		signed, err := sign(payload, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePackage(signed, &key.PublicKey); err == nil {
			t.Fatal("accepted incomplete or malformed release")
		}
	}
}

func TestRegistryRejectsMalformedDependencyWireFields(t *testing.T) {
	key := testKey()
	badOptional := appendString(nil, 1, "decimal")
	badOptional = appendString(badOptional, 2, "~> 2.0")
	badOptional = protowire.AppendTag(badOptional, 3, protowire.VarintType)
	badOptional = protowire.AppendVarint(badOptional, 2)
	for _, dependency := range [][]byte{
		nil,
		appendString(nil, 1, "../bad"),
		appendString(nil, 1, "decimal"),
		badOptional,
		appendString(appendString(appendString(nil, 1, "decimal"), 2, "~> 2.0"), 4, "../bad"),
	} {
		release := appendString(nil, 1, "1.0.0")
		release = appendBytes(release, 2, bytes.Repeat([]byte{1}, 32))
		release = appendBytes(release, 3, dependency)
		release = appendBytes(release, 5, bytes.Repeat([]byte{2}, 32))
		payload := appendBytes(nil, 1, release)
		payload = appendString(payload, 2, "example")
		payload = appendString(payload, 3, "hexpm")
		signed, err := sign(payload, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePackage(signed, &key.PublicKey); err == nil {
			t.Fatal("accepted malformed signed dependency")
		}
	}
}

func TestRegistryEncoderRejectsInvalidIdentityAndChecksums(t *testing.T) {
	archive, err := os.ReadFile(filepath.Join("testdata", "decimal-2.0.0.tar"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := InspectTarball(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	key := testKey()
	for _, reg := range []RegistryPackage{
		{Name: "../bad", Repository: "hexpm", Releases: []Package{pkg}},
		{Name: "decimal", Releases: []Package{pkg}},
		{Name: "decimal", Repository: "hexpm", Releases: []Package{{Name: "other", Version: "2.0.0"}}},
		{Name: "decimal", Repository: "hexpm", Releases: []Package{{Name: "decimal", Version: "bad"}}},
	} {
		if _, err := EncodePackage(reg, key); err == nil {
			t.Fatalf("accepted invalid package metadata: %+v", reg)
		}
	}
	if _, err := EncodePackage(RegistryPackage{
		Name: "decimal", Repository: "hexpm", Releases: []Package{pkg},
	}, nil); err == nil {
		t.Fatal("signed with no key")
	}
	if _, err := EncodeNames("", []string{"decimal"}, key); err == nil {
		t.Fatal("accepted empty names repository")
	}
	if _, err := EncodeNames("hexpm", []string{"decimal", "decimal"}, key); err == nil {
		t.Fatal("accepted duplicate names")
	}
	if _, err := EncodeVersions("hexpm", []RegistryPackage{
		{Name: "decimal", Releases: []Package{{Version: "bad"}}},
	}, key); err == nil {
		t.Fatal("accepted invalid versions release")
	}
}
