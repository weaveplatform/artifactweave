package acceptance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/cucumber/godog"
)

// registerNPMSignatures registers the steps of npm_signatures.feature.
func (w *world) registerNPMSignatures(sc *godog.ScenarioContext) {
	sc.Step(`^I create the ECDSA key pair "([^"]*)" with alias "([^"]*)" as admin$`,
		w.createECDSAKeyPair)
	sc.Step(`^I create the ECDSA key pair "([^"]*)" with alias "([^"]*)" from a gpg export as admin$`,
		w.createGPGECDSAKeyPair)
}

// createECDSAKeyPair posts a freshly generated P-256 pair as PEM.
func (w *world) createECDSAKeyPair(name, alias string) error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return err
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return err
	}
	return w.postECDSAKeyPair(name, alias,
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})))
}

// createGPGECDSAKeyPair posts a freshly generated NIST P-256 OpenPGP key as
// gpg's ASCII-armored exports, the material 7.x's npm documentation has
// gpg make for the ECDSA pair.
func (w *world) createGPGECDSAKeyPair(name, alias string) error {
	e, err := openpgp.NewEntity("npm signing", "", "npm@example.com", &packet.Config{
		Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveNistP256,
	})
	if err != nil {
		return err
	}
	priv, err := armored(openpgp.PrivateKeyType, func(b io.Writer) error {
		return e.SerializePrivate(b, nil)
	})
	if err != nil {
		return err
	}
	pub, err := armored(openpgp.PublicKeyType, e.Serialize)
	if err != nil {
		return err
	}
	return w.postECDSAKeyPair(name, alias, priv, pub)
}

func (w *world) postECDSAKeyPair(name, alias, private, public string) error {
	body, err := json.Marshal(map[string]string{
		"pairName": name, "pairType": "ECDSA", "alias": alias,
		"privateKey": private, "publicKey": public,
	})
	if err != nil {
		return err
	}
	return w.send(http.MethodPost, "/artifactory/api/security/keypair", string(body),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

// armored is what write serializes, in an OpenPGP armor block of typ.
func armored(typ string, write func(io.Writer) error) (string, error) {
	var raw, out bytes.Buffer
	if err := write(&raw); err != nil {
		return "", err
	}
	a, err := armor.Encode(&out, typ, nil)
	if err != nil {
		return "", err
	}
	if _, err := a.Write(raw.Bytes()); err != nil {
		return "", err
	}
	if err := a.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}
