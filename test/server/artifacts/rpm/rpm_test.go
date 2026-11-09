package rpm

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccRPMVirtualMetadataUsesConfiguredGPGKey(t *testing.T) {
	w := newItemWorld(t, false)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "rpm-gpg", Type: types.KeypairGPG,
		PrivateKey: key.Private, PublicKey: key.Public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "rpm-signed-virtual", Class: repo.Virtual, PackageType: repo.RPM, KeyPair: "rpm-gpg"},
		{Key: "rpm-missing-key", Class: repo.Virtual, PackageType: repo.RPM, KeyPair: "absent"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := "/artifactory/rpm-signed-virtual/repodata/repomd.xml"
	manifest := w.do(http.MethodGet, base, "admin", "")
	signature := w.do(http.MethodGet, base+".asc", "admin", "")
	public := w.do(http.MethodGet, base+".key", "admin", "")
	if manifest.Code != http.StatusOK || signature.Code != http.StatusOK ||
		public.Code != http.StatusOK || public.Body.String() != key.Public {
		t.Fatalf("signed virtual metadata status: manifest=%d signature=%d key=%d",
			manifest.Code, signature.Code, public.Code)
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(public.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(manifest.Body.Bytes()), bytes.NewReader(signature.Body.Bytes()), nil,
	); err != nil {
		t.Fatalf("dnf-compatible virtual metadata signature failed verification: %v", err)
	}
	if res := w.do(
		http.MethodGet,
		"/artifactory/rpm-missing-key/repodata/repomd.xml",
		"admin",
		"",
	); res.Code != http.StatusInternalServerError {
		t.Errorf("virtual metadata with a missing signing key returned %d", res.Code)
	}
}
