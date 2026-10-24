package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // APKINDEX v2 signature verification.
	"io"
	"strings"
	"testing"
)

func TestEncodeIndexSignsExactCompressedIndex(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	body, err := EncodeIndex([]Package{{
		Name: "hello", Version: "1.0-r0", Architecture: "x86_64",
		ArchiveSize: 123, InstalledSize: 5, ControlSHA1: "YWJj",
	}}, "controlled repository", "test@example.rsa.pub", key)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(body)
	signatureSegment, err := readSegment(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	signature := signatureSegment[".SIGN.RSA.test@example.rsa.pub"]
	if len(signature) == 0 {
		t.Fatal("missing signature segment")
	}
	indexCompressed := body[len(body)-r.Len():]
	h := sha1.Sum(indexCompressed) //nolint:gosec // APKINDEX v2 wire format.
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, h[:], signature); err != nil {
		t.Fatalf("signature does not bind the compressed index: %v", err)
	}
	z, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	tr := tar.NewReader(z)
	var index string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "APKINDEX" {
			index = string(data)
		}
	}
	for _, want := range []string{"C:Q1YWJj", "P:hello", "V:1.0-r0", "A:x86_64", "S:123", "I:5"} {
		if !strings.Contains(index, want+"\n") {
			t.Errorf("index misses %q: %q", want, index)
		}
	}
}

func TestDecodeIndexPreservesNativeRecord(t *testing.T) {
	archive, _ := fixture(t)
	pkg, err := ReadPackage(context.Background(), bytes.NewReader(archive),
		int64(len(archive)), "v3.20/main/x86_64/hello-1.0-r0.apk")
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	index, err := EncodeIndex([]Package{pkg}, "controlled repository", "test.rsa.pub", key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeIndex(t.Context(), index, "v3.20/main/x86_64/APKINDEX.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].Path != pkg.Path ||
		decoded[0].IndexRecord() != pkg.IndexRecord() {
		t.Fatalf("decoded index differs from source: %+v", decoded)
	}
}
