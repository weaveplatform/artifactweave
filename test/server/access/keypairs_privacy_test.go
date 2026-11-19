package access_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type privacyWorld struct {
	rt   *app.Runtime
	h    http.Handler
	logs bytes.Buffer
}

func newPrivacyWorld(t *testing.T, backend string) *privacyWorld {
	t.Helper()
	var dsn string
	if backend == "postgres" {
		dsn = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
	}
	w := &privacyWorld{}
	w.rt = harness.App(t, func(c *config.Config) {
		c.Bootstrap.AdminPassword = "pw"
		c.MasterKey = "0123456789abcdef0123456789abcdef"
		if dsn != "" {
			c.Database = config.Database{Driver: "postgres", DSN: dsn}
		}
	})
	if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "reader", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	w.h = harness.Router(
		harness.Config{Store: w.rt.Store, Blobs: w.rt.Blobs, Auth: w.rt.Auth, Tokens: w.rt.Tokens, Keys: w.rt.Keys, Logger: slog.New(slog.NewJSONHandler(&w.logs, nil))},
	)
	return w
}

func (w *privacyWorld) call(t *testing.T, method, path, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

func privacyBody(t *testing.T, p accessapi.Keypair) string {
	t.Helper()
	body, err := json.Marshal(
		map[string]string{"pairName": p.Name, "pairType": string(p.Type), "alias": p.Alias, "privateKey": p.PrivateKey, "publicKey": p.PublicKey, "passphrase": p.Passphrase},
	)
	if err != nil {
		t.Fatal("cannot encode synthetic key fixture")
	}
	return string(body)
}

func privateMarker(t *testing.T, private string) string {
	t.Helper()
	for _, s := range strings.Split(private, "\n") {
		if len(s) > 40 && !strings.HasPrefix(s, "-----") {
			return s
		}
	}
	t.Fatal("fixture has no private packet marker")
	return ""
}

func privacyStatus(t *testing.T, w *privacyWorld, rec *httptest.ResponseRecorder, want int, private string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("public-material response status%d want%d (body withheld)", rec.Code, want)
	}
	marker := privateMarker(t, private)
	if strings.Contains(rec.Body.String(), marker) || strings.Contains(w.logs.String(), marker) || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatal("public response or logs contain synthetic private material (bytes withheld)")
	}
}

func privacyPairs(t *testing.T) []accessapi.Keypair {
	t.Helper()
	gpg, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	rsa := testKeys()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(typ string, v any) string {
		var b []byte
		var err error
		if typ == "PRIVATE KEY" {
			b, err = x509.MarshalPKCS8PrivateKey(v)
		} else {
			b, err = x509.MarshalPKIXPublicKey(v)
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}))
	}
	return []accessapi.Keypair{
		{Name: "gpg", Alias: "gpg", Type: accessapi.KeypairGPG, PrivateKey: gpg.Private, PublicKey: gpg.Public},
		{Name: "rsa", Alias: "rsa", Type: accessapi.KeypairRSA, PrivateKey: rsa.private, PublicKey: rsa.public},
		{Name: "ecdsa", Alias: "ecdsa", Type: accessapi.KeypairECDSA, PrivateKey: encode("PRIVATE KEY", ec), PublicKey: encode("PUBLIC KEY", &ec.PublicKey)},
		{Name: "ed25519", Alias: "ed25519", Type: accessapi.KeypairEd25519, PrivateKey: encode("PRIVATE KEY", edPriv), PublicKey: encode("PUBLIC KEY", edPub)},
	}
}

func TestAccKeypairPublicMaterialPrivacy(t *testing.T) {
	pairs := privacyPairs(t)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newPrivacyWorld(t, backend)
			for _, good := range pairs {
				t.Run(good.Name, func(t *testing.T) {
					const route = "/artifactory/api/security/keypair"
					rec := w.call(t, "POST", route, "admin", privacyBody(t, good))
					privacyStatus(t, w, rec, 201, good.PrivateKey)
					if good.Type == accessapi.KeypairGPG {
						privacyStatus(t, w, w.call(t, "GET", route, "admin", ""), 200, good.PrivateKey)
					}
					values := []struct{ name, value string }{{"trailing-private", good.PublicKey + good.PrivateKey}}
					if good.Type == accessapi.KeypairRSA {
						values = append(values, struct{ name, value string }{"nested-frame", "-----BEGIN PUBLIC KEY-----\n" + pairs[0].PrivateKey + "\n" + good.PublicKey})
					}
					if good.Type == accessapi.KeypairGPG {
						values = append(
							values,
							struct{ name, value string }{"private-armor", good.PrivateKey},
							struct{ name, value string }{"mislabeled-private", strings.ReplaceAll(good.PrivateKey, "PGP PRIVATE KEY BLOCK", "PGP PUBLIC KEY BLOCK")},
						)
					}
					for _, variant := range values {
						t.Run(variant.name, func(t *testing.T) {
							bad := good
							bad.PublicKey = variant.value
							for _, op := range []struct{ method, path string }{{"POST", route}, {"PUT", route}, {"POST", route + "/verify"}} {
								rec := w.call(t, op.method, op.path, "admin", privacyBody(t, bad))
								privacyStatus(t, w, rec, 400, good.PrivateKey)
								if variant.name == "nested-frame" {
									privacyStatus(t, w, rec, 400, pairs[0].PrivateKey)
								}
							}
						})
					}
					read := w.call(t, "GET", route+"/"+good.Name, "reader", "")
					privacyStatus(t, w, read, 200, good.PrivateKey)
					var projection struct {
						Public string `json:"publicKey"`
					}
					if json.Unmarshal(read.Body.Bytes(), &projection) != nil || projection.Public != good.PublicKey {
						t.Fatal("valid named public bytes changed after refused writes")
					}
					legacy := good
					legacy.Name = "legacy-" + good.Name
					legacy.Alias = legacy.Name
					legacy.PublicKey = good.PublicKey + good.PrivateKey
					if err := w.rt.Store.Keys().CreateKeypair(t.Context(), legacy); err != nil {
						t.Fatal(err)
					}
					for _, user := range []string{"admin", "reader"} {
						privacyStatus(t, w, w.call(t, "GET", route+"/"+legacy.Name, user, ""), 500, good.PrivateKey)
					}
					privacyStatus(t, w, w.call(t, "GET", route, "admin", ""), 500, good.PrivateKey)
					privacyStatus(t, w, w.call(t, "GET", route+"/"+legacy.Name, "", ""), 401, good.PrivateKey)
					if err := w.rt.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
						t.Fatal(err)
					}
					privacyStatus(t, w, w.call(t, "GET", route+"/"+legacy.Name, "", ""), 500, good.PrivateKey)
					privacyStatus(t, w, w.call(t, "GET", route+"/"+good.Name, "", ""), 200, good.PrivateKey)
					for _, kind := range []repo.PackageType{repo.Debian, repo.RPM, repo.Opkg} {
						name := legacy.Name + "-" + string(kind)
						c := *repo.Defaults(&repo.Config{Key: name, Class: repo.Local, PackageType: kind, PrimaryKeyPairRef: legacy.Name})
						if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
							t.Fatal(err)
						}
						for _, user := range []string{"reader", ""} {
							privacyStatus(t, w, w.call(t, "GET", route+"/public/repositories/"+name, user, ""), 500, good.PrivateKey)
						}
						if good.Type == accessapi.KeypairGPG {
							valid := c
							valid.Key = "valid-" + string(kind)
							valid.PrimaryKeyPairRef = good.Name
							if err := w.rt.Store.Repos().Create(t.Context(), valid); err != nil {
								t.Fatal(err)
							}
							for _, user := range []string{"reader", ""} {
								r := w.call(t, "GET", route+"/public/repositories/"+valid.Key, user, "")
								privacyStatus(t, w, r, 200, good.PrivateKey)
								if r.Body.String() != good.PublicKey {
									t.Fatal("valid repository public bytes changed")
								}
							}
						}
					}
					if err := w.rt.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, false); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestAccRPMHistoricalPublicArtifactPrivacy(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newPrivacyWorld(t, backend)
			for _, c := range []repo.Config{{Key: "legacy-rpm", Class: repo.Local, PackageType: repo.RPM}, {Key: "generic-view", Class: repo.Virtual, PackageType: repo.Generic, Repositories: []string{"legacy-rpm"}}} {
				if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
					t.Fatal(err)
				}
			}
			store := func(body string) {
				t.Helper()
				u, err := w.rt.Blobs.BeginUpload(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err = u.Append(t.Context(), strings.NewReader(body)); err != nil {
					t.Fatal(err)
				}
				info, err := u.Commit(t.Context(), blob.Digests{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.rt.Store.Items().
					Put(t.Context(), model.Item{RepoKey: "legacy-rpm", Name: "repodata/repomd.xml.key", Actual: kernel.Checksums{SHA256: info.Digests.SHA256}, Size: info.Size}); err != nil {
					t.Fatal(err)
				}
			}
			store(key.Public)
			for _, path := range []string{"/artifactory/legacy-rpm/repodata/repomd.xml.key", "/artifactory/generic-view/repodata/repomd.xml.key"} {
				rec := w.call(t, "GET", path, "admin", "")
				privacyStatus(t, w, rec, 200, key.Private)
				if rec.Body.String() != key.Public {
					t.Fatal("valid historical public artifact bytes changed")
				}
			}
			store(key.Public + key.Private)
			for _, path := range []string{"/artifactory/legacy-rpm/repodata/repomd.xml.key", "/artifactory/generic-view/repodata/repomd.xml.key"} {
				for _, method := range []string{"GET", "HEAD"} {
					privacyStatus(t, w, w.call(t, method, path, "admin", ""), 500, key.Private)
				}
			}
		})
	}
}

func TestAccTerraformStoredPublicKeyPrivacy(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newPrivacyWorld(t, backend)
			pair := accessapi.Keypair{Name: "provider-key", Type: accessapi.KeypairGPG, PrivateKey: key.Private, PublicKey: key.Public}
			if err := w.rt.Store.Keys().CreateKeypair(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			c := *repo.Defaults(&repo.Config{Key: "provider", Class: repo.Local, PackageType: repo.Terraform, TerraformType: repo.TerraformProvider, PrimaryKeyPairRef: pair.Name})
			if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			release := terraformtest.Release{Type: "null", Version: "3.3.2", Platforms: []string{"linux_amd64"}, Protocols: []string{"5.0"}}
			for name, body := range release.Files(key) {
				r := w.call(t, "PUT", "/artifactory/provider/nested/HashiCorp/Null/3.3.2/"+name, "admin", string(body))
				if r.Code != 201 {
					t.Fatalf("synthetic provider deploy status%d (body withheld)", r.Code)
				}
			}
			const path = "/artifactory/api/terraform/v1/providers/provider-rt-ns-hashicorp/null/3.3.2/download/linux/amd64"
			r := w.call(t, "GET", path, "admin", "")
			privacyStatus(t, w, r, 200, key.Private)
			var doc struct {
				Signing struct {
					Keys []struct {
						Armor string `json:"ascii_armor"`
					} `json:"gpg_public_keys"`
				} `json:"signing_keys"`
			}
			if json.Unmarshal(r.Body.Bytes(), &doc) != nil || len(doc.Signing.Keys) != 1 || doc.Signing.Keys[0].Armor != key.Public {
				t.Fatal("valid provider public bytes changed")
			}
			pair.PublicKey = key.Public + key.Private
			if err := w.rt.Store.Keys().UpdateKeypair(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			privacyStatus(t, w, w.call(t, "GET", path, "admin", ""), 409, key.Private)
			pair.PublicKey = key.Public
			if err := w.rt.Store.Keys().UpdateKeypair(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			pair.Name = "provider-secondary"
			pair.PublicKey = key.Private
			if err := w.rt.Store.Keys().CreateKeypair(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			c.SecondaryKeyPairRef = pair.Name
			if err := w.rt.Store.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			privacyStatus(t, w, w.call(t, "GET", path, "admin", ""), 409, key.Private)
		})
	}
}
