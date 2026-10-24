package debian

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

func TestSignReleaseBindsBothAptSignatureForms(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("Suite: stable\nSHA256:\n deadbeef 1 Packages\n")
	signed, err := SignRelease(body, key.Private, "")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(body), bytes.NewReader(signed.ReleaseGPG), nil,
	); err != nil {
		t.Fatalf("Release.gpg does not verify: %v", err)
	}
	block, rest := clearsign.Decode(signed.InRelease)
	if block == nil || len(rest) != 0 || !bytes.Equal(block.Plaintext, body) {
		t.Fatalf("InRelease does not carry exact Release body: %q", signed.InRelease)
	}
	if _, err := openpgp.CheckDetachedSignature(
		ring, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil,
	); err != nil {
		t.Fatalf("InRelease signature does not verify: %v", err)
	}
	if _, err := SignRelease(body, "not a GPG key", ""); err == nil {
		t.Fatal("invalid signing key accepted")
	}
}
