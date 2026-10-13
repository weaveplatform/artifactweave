package acceptance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"time"
)

// Key material is generated per scenario rather than written into feature
// files: a committed private key would trip secret scanning.

// createKeyPair posts a freshly generated RSA key pair.
func (w *world) createKeyPair(name, alias string) error {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"pairName": name, "pairType": "RSA", "alias": alias,
		"privateKey": string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k),
		})),
		"publicKey": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})),
	})
	if err != nil {
		return err
	}
	return w.send(http.MethodPost, "/artifactory/api/security/keypair", string(body),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

// addCertificate posts a freshly generated self-signed client certificate
// and its key as the PEM bundle the API takes.
func (w *world) addCertificate(alias string) error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: alias},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	bundle := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	return w.send(http.MethodPost, "/artifactory/api/system/security/certificates/"+alias, bundle,
		func(r *http.Request) {
			r.SetBasicAuth("admin", testAdminPassword)
			r.Header.Set("Content-Type", "text/plain")
		})
}
