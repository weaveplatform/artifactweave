package nix

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func sampleInfo() Info {
	sum := sha256.Sum256([]byte("fixture"))
	hash := hex.EncodeToString(sum[:])
	return Info{
		StorePath:   "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-fixture",
		URL:         "nar/" + Nix32(sum[:]) + ".nar",
		Compression: "none",
		FileHash:    hash,
		FileSize:    128,
		NarHash:     hash,
		NarSize:     128,
		References:  []string{},
	}
}

func TestUnitNarinfoRoundTripAndTrust(t *testing.T) {
	info := sampleInfo()
	path := strings.TrimPrefix(info.StorePath, "/nix/store/")[:32] + ".narinfo"
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = info.Sign("cache-1", key); err != nil {
		t.Fatal(err)
	}
	if err = info.Sign("cache-1", key); err != nil || len(info.Signatures) != 1 {
		t.Fatalf("duplicate signature: %v", err)
	}
	parsed, err := Parse(info.Encode(info.URL), path)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Verify("cache-1", pub) || parsed.Verify("other", pub) {
		t.Fatal("signature trust")
	}
	parsed.NarSize++
	if parsed.Verify("cache-1", pub) {
		t.Fatal("altered nar size verifies")
	}
	parsed = info
	parsed.References = []string{"0123456789abcdfghijklmnpqrsvwxyz-dependency"}
	if parsed.Verify("cache-1", pub) {
		t.Fatal("altered references verify")
	}
	if err = info.Sign("bad:alias", key); err == nil {
		t.Fatal("unsafe key alias accepted")
	}
	if err = info.Sign("key", nil); err == nil {
		t.Fatal("missing private key accepted")
	}
	info.Signatures = []string{"cache-1:not-base64"}
	if info.Verify("cache-1", pub) {
		t.Fatal("malformed signature verifies")
	}
}

func TestUnitNarinfoRejectsAmbiguousAndUnsafeMetadata(t *testing.T) {
	info := sampleInfo()
	path := strings.TrimPrefix(info.StorePath, "/nix/store/")[:32] + ".narinfo"
	good := string(info.Encode(info.URL))
	cases := []struct{ name, body, path string }{
		{"empty", "", path},
		{"no newline", strings.TrimSuffix(good, "\n"), path},
		{"huge", strings.Repeat("x", MaxMetadataBytes+1), path},
		{"wrong filename", good, strings.Repeat("0", 32) + ".narinfo"},
		{"bad filename", good, "bad.narinfo"},
		{"repeated", good + "StorePath: " + info.StorePath + "\n", path},
		{"unknown", good + "Foo: bar\n", path},
		{"missing colon", good + "no-colon\n", path},
		{"newline control", good + "Deriver: x\r\n", path},
		{"nul", good + "Deriver: x\x00\n", path},
		{"outside store", strings.Replace(good, "/nix/store/", "/tmp/store/", 1), path},
		{"invalid store name", strings.Replace(good, "-fixture", "-/evil", 1), path},
		{"absolute URL", strings.Replace(good, "URL: nar/", "URL: https://evil/", 1), path},
		{"traversal", strings.Replace(good, "URL: nar/", "URL: nar/../", 1), path},
		{"compression", strings.Replace(good, "Compression: none", "Compression: gzip", 1), path},
		{"bad hash", strings.Replace(good, "NarHash: sha256:", "NarHash: sha1:", 1), path},
		{"zero size", strings.Replace(good, "NarSize: 128", "NarSize: 0", 1), path},
		{
			"overflow",
			strings.Replace(good, "NarSize: 128", "NarSize: 999999999999999999999999", 1),
			path,
		},
		{"large size", strings.Replace(good, "NarSize: 128", "NarSize: 3000000000", 1), path},
		{"bad file size", strings.Replace(good, "FileSize: 128", "FileSize: -1", 1), path},
		{
			"wrong file hash",
			strings.Replace(
				good,
				"FileHash: "+nativeHash(info.FileHash),
				"FileHash: sha256:"+strings.Repeat("0", 64),
				1,
			),
			path,
		},
		{"invalid refs", strings.Replace(good, "References: ", "References: ../escape", 1), path},
		{
			"duplicate refs",
			strings.Replace(
				good,
				"References: ",
				"References: "+strings.TrimPrefix(
					info.StorePath,
					"/nix/store/",
				)+" "+strings.TrimPrefix(
					info.StorePath,
					"/nix/store/",
				),
				1,
			),
			path,
		},
		{"invalid deriver", good + "Deriver: ../builder\n", path},
		{"bad signature", good + "Sig: bad\n", path},
		{"bad signature bytes", good + "Sig: cache:eA==\n", path},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.body), tc.path); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
	withCA := good + "CA: fixed:r:sha256:unverified\n"
	parsed, err := Parse([]byte(withCA), path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(parsed.Encode(info.URL), []byte("CA:")) {
		t.Fatal("unverified CA forwarded")
	}
}

func TestUnitHashAndPaths(t *testing.T) {
	for _, data := range [][]byte{make([]byte, 32), bytes.Repeat([]byte{255}, 32), []byte("0123456789abcdef0123456789abcdef")} {
		got, err := parseHash("sha256:" + Nix32(data))
		if err != nil || got != hex.EncodeToString(data) {
			t.Fatalf("hash round trip: %s %v", got, err)
		}
	}
	for _, bad := range []string{"sha1:abc", "sha256:abc", "sha256:" + strings.Repeat("z", 64), "sha256:" + strings.Repeat("z", 52), "sha256:0" + strings.Repeat("e", 51)} {
		if _, err := parseHash(bad); err == nil {
			t.Errorf("invalid hash %s accepted", bad)
		}
	}
	for _, suffix := range []string{"", ".xz", ".gz", ".bz2", ".zst"} {
		if _, ok := ArchivePath("nar/" + strings.Repeat("0", 52) + ".nar" + suffix); !ok {
			t.Fatalf("valid suffix %s", suffix)
		}
	}
	for _, bad := range []string{"nar/x.nar", "nar/../x.nar", "nar/%2e.nar", "x/" + strings.Repeat("0", 52) + ".nar", "nar/" + strings.Repeat("0", 52) + ".nar.zip", "nar/" + strings.Repeat("0", 52)} {
		if _, ok := ArchivePath(bad); ok {
			t.Fatalf("bad path %s", bad)
		}
	}
}

func TestUnitNarinfoOptionalFieldsAndBounds(t *testing.T) {
	info := sampleInfo()
	info.Deriver = "unknown-deriver"
	path := strings.TrimPrefix(info.StorePath, "/nix/store/")[:32] + ".narinfo"
	if parsed, err := Parse(
		info.Encode(info.URL),
		path,
	); err != nil ||
		parsed.Deriver != info.Deriver {
		t.Fatalf("deriver: %v", err)
	}
	if info.Verify("cache", nil) {
		t.Fatal("nil public key trusted")
	}
	info.URL += ".bz2"
	info.Compression = "bzip2"
	if _, err := Parse(
		bytes.ReplaceAll(info.Encode(info.URL), []byte("Compression: bzip2\n"), nil),
		path,
	); err != nil {
		t.Fatal(err)
	}
	info.References = make([]string, 4097)
	for i := range info.References {
		info.References[i] = "0123456789abcdfghijklmnpqrsvwxyz-ref"
	}
	if _, err := Parse(info.Encode(info.URL), path); err == nil {
		t.Fatal("unbounded references")
	}
	info.References = nil
	if _, err := Parse(
		bytes.ReplaceAll(
			info.Encode(info.URL),
			[]byte("FileHash: sha256:"),
			[]byte("FileHash: bad:"),
		),
		path,
	); err == nil {
		t.Fatal("invalid transport hash")
	}
}
