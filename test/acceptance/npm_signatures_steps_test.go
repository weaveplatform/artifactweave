package acceptance

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/cucumber/godog"
)

// registerNPMSignatures registers the steps of npm_signatures.feature.
func (w *world) registerNPMSignatures(sc *godog.ScenarioContext) {
	sc.Step(`^I create the ECDSA key pair "([^"]*)" with alias "([^"]*)" as admin$`,
		w.createECDSAKeyPair)
	sc.Step(
		`^I create the ECDSA key pair "([^"]*)" with alias "([^"]*)" from a gpg export as admin$`,
		w.createGPGECDSAKeyPair,
	)
	sc.Step(
		`^the npm signature of "([^"]*)" at "([^"]*)" in "([^"]*)" is replaced by that of "([^"]*)"$`,
		w.tamperNPMSignature,
	)
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

// tamperNPMSignature gives name's version in repo the signature of other's
// same version, as an attacker who rewrote the stored metadata would: a
// signature by a listed key, over another package's message. The registry
// itself never serves one, since every signature is made from the version's
// own integrity and a pair's halves must match, so the step writes the
// running binary's SQLite store directly. The packument is built from that
// row on each read (DV-042), so the next read serves the tampered signature.
func (w *world) tamperNPMSignature(name, version, repo, other string) error {
	path, err := w.sqliteStore()
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(10000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	sigOf := func(pkg string) (string, error) {
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT v.metadata FROM package_versions v
			JOIN packages p ON p.id = v.package_id
			WHERE p.repo_key = ? AND p.name = ? AND v.version = ?`,
			repo, pkg, version).Scan(&raw); err != nil {
			return "", fmt.Errorf("metadata of %s@%s: %w", pkg, version, err)
		}
		var m struct {
			Dist struct {
				Signatures []struct {
					Sig string `json:"sig"`
				} `json:"signatures"`
			} `json:"dist"`
		}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return "", err
		}
		if len(m.Dist.Signatures) != 1 {
			return "", fmt.Errorf("%s@%s has %d signatures", pkg, version, len(m.Dist.Signatures))
		}
		return m.Dist.Signatures[0].Sig, nil
	}
	sig, err := sigOf(name)
	if err != nil {
		return err
	}
	replacement, err := sigOf(other)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE package_versions SET metadata = replace(metadata, ?, ?)
		WHERE version = ? AND package_id = (SELECT id FROM packages WHERE repo_key = ? AND name = ?)`,
		sig, replacement, version, repo, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("tamper %s@%s: %d rows, %v", name, version, n, err)
	}
	return nil
}

// sqliteStore is the path of the running binary's SQLite store. Acceptance
// starts the binary with its default store, SQLite in the data directory,
// but the environment it inherits could name another driver, and opening
// the path would then create an empty file whose missing tables say nothing
// of the cause. So a step that writes the store refuses any other driver by
// name, and a store that is not there.
func (w *world) sqliteStore() (string, error) {
	driver := "sqlite"
	if w.server != nil {
		for _, kv := range w.server.Env {
			if v, ok := strings.CutPrefix(kv, "ARTIFACTWEAVE_DATABASE_DRIVER="); ok && v != "" {
				driver = v
			}
		}
	}
	if driver != "sqlite" {
		return "", fmt.Errorf(
			"this step writes the binary's SQLite store, but the registry runs on database driver %q",
			driver,
		)
	}
	path := filepath.Join(w.dataDir, "artifactweave.db")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("this step writes the binary's SQLite store, which is not at %s: %w",
			path, err)
	}
	return path, nil
}
