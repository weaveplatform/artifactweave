package opkg_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/terraform/terraformtest"
)

func TestAccOpkgVirtualFeedSignsOnlyWithConfiguredGPGKey(t *testing.T) {
	w := newItemWorld(t, false)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "opkg-gpg", Type: api.KeypairGPG,
		PrivateKey: key.Private, PublicKey: key.Public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "opkg-signed", Class: repo.Virtual, PackageType: repo.Opkg, KeyPair: "opkg-gpg"},
		{Key: "opkg-no-key", Class: repo.Virtual, PackageType: repo.Opkg},
		{Key: "opkg-missing-key", Class: repo.Virtual, PackageType: repo.Opkg, KeyPair: "absent"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		key, name string
		status    int
	}{
		{"opkg-signed", "Packages", http.StatusOK},
		{"opkg-signed", "Packages.sig", http.StatusOK},
		{"opkg-signed", "Release", http.StatusOK},
		{"opkg-no-key", "Packages.sig", http.StatusNotFound},
		{"opkg-missing-key", "Packages", http.StatusInternalServerError},
		{"opkg-missing-key", "Packages.sig", http.StatusInternalServerError},
	} {
		url := "/artifactory/" + tc.key + "/feed/" + tc.name
		res := w.do(http.MethodGet, url, "admin", "")
		if res.Code != tc.status {
			t.Errorf("%s returned %d, want %d: %s", url, res.Code, tc.status, res.Body)
		}
	}
	packages := w.do(http.MethodGet, "/artifactory/opkg-signed/feed/Packages", "admin", "")
	signature := w.do(http.MethodGet, "/artifactory/opkg-signed/feed/Packages.sig", "admin", "")
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(packages.Body.Bytes()), bytes.NewReader(signature.Body.Bytes()), nil,
	); err != nil {
		t.Fatalf("virtual Packages signature does not bind feed: %v", err)
	}
}
