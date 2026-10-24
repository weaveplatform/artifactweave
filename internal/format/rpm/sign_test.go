package rpm

import (
	"bytes"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

func TestSignRepomdBindsMetadata(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("<repomd>known revision</repomd>")
	sig, err := SignRepomd(body, key.Private, "")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader([]byte(key.Public)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring,
		bytes.NewReader(body),
		bytes.NewReader(sig),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring,
		bytes.NewReader([]byte("changed")),
		bytes.NewReader(sig),
		nil,
	); err == nil {
		t.Fatal("signature accepted modified repomd")
	}
}
