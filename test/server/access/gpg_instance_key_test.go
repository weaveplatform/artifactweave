package access_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

const (
	gpgPrivateRoute    = "/artifactory/api/gpg/key/private"
	gpgPublicRoute     = "/artifactory/api/gpg/key/public"
	gpgPassphraseRoute = "/artifactory/api/gpg/passphrase"
	repoPublicRoute    = "/artifactory/api/security/keypair/public/repositories/"
)

// passphrase sets the instance key's passphrase as user, which travels in
// the X-GPG-PASSPHRASE header with no body (setgpgpassphrase).
func (w *privacyWorld) passphrase(t *testing.T, user, value string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, gpgPassphraseRoute, nil)
	r.Header.Set("X-GPG-PASSPHRASE", value)
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

func wantText(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || rec.Body.String() != body {
		t.Fatalf("answered %d %q, want %d %q", rec.Code, rec.Body, status, body)
	}
}

// TestAccInstanceGPGKeyOperations walks the four /api/gpg operations on
// SQLite and PostgreSQL (#1654). Each write is admin-only and sets one
// field: the public read answers 404 until a public half is set, the
// private half alone included, and never returns the private half or the
// passphrase. Private material is refused as the public half (#1727), a
// public block as the private half, and a pair of the same name of another
// type is not given a GPG half. The pair is listed by the key pair API.
func TestAccInstanceGPGKeyOperations(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newPrivacyWorld(t, backend)
			privacyStatus(t, w, w.call(t, "GET", gpgPublicRoute, "reader", ""), 404, key.Private)
			privacyStatus(t, w, w.call(t, "GET", gpgPublicRoute, "", ""), 401, key.Private)

			privacyStatus(t, w, w.call(t, "PUT", gpgPrivateRoute, "", key.Private), 401, key.Private)
			privacyStatus(t, w, w.call(t, "PUT", gpgPrivateRoute, "reader", key.Private), 403, key.Private)
			privacyStatus(t, w, w.call(t, "PUT", gpgPrivateRoute, "admin", key.Public), 400, key.Private)
			privacyStatus(t, w, w.call(t, "PUT", gpgPrivateRoute, "admin", "not a key"), 400, key.Private)
			wantText(t, w.call(t, "PUT", gpgPrivateRoute, "admin", key.Private), 200, "Private key is installed")
			// A private half never makes a public answer.
			privacyStatus(t, w, w.call(t, "GET", gpgPublicRoute, "reader", ""), 404, key.Private)
			privacyStatus(t, w, w.call(t, "GET", "/artifactory/api/security/keypair", "admin", ""), 200, key.Private)

			for _, bad := range []string{key.Private, key.Public + key.Private, "not a key"} {
				privacyStatus(t, w, w.call(t, "PUT", gpgPublicRoute, "admin", bad), 400, key.Private)
			}
			privacyStatus(t, w, w.call(t, "PUT", gpgPublicRoute, "reader", key.Public), 403, key.Private)
			wantText(t, w.call(t, "PUT", gpgPublicRoute, "admin", key.Public), 200, "Public key is installed")
			wantText(t, w.call(t, "GET", gpgPublicRoute, "reader", ""), 200, key.Public)
			if got := w.call(t, "GET", gpgPublicRoute, "reader", "").Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
				t.Errorf("public key content type %q", got)
			}

			privacyStatus(t, w, w.passphrase(t, "", "secret"), 401, key.Private)
			privacyStatus(t, w, w.passphrase(t, "reader", "secret"), 403, key.Private)
			wantText(t, w.passphrase(t, "admin", "secret"), 200, "Successfully updated signing pass-phrase")

			stored, err := w.rt.Store.Keys().Keypair(t.Context(), accessapi.DefaultGPGKey)
			if err != nil || stored.Type != accessapi.KeypairGPG || stored.Alias != accessapi.DefaultGPGKey ||
				stored.PrivateKey != key.Private || stored.PublicKey != key.Public || stored.Passphrase != "secret" {
				t.Fatalf("stored instance pair differs (key bytes withheld): %v", err)
			}
			read := w.call(t, "GET", "/artifactory/api/security/keypair/"+accessapi.DefaultGPGKey, "reader", "")
			privacyStatus(t, w, read, 200, key.Private)
			var summary map[string]string
			if json.Unmarshal(read.Body.Bytes(), &summary) != nil || summary["pairType"] != "GPG" ||
				summary["alias"] != accessapi.DefaultGPGKey || summary["publicKey"] != key.Public || len(summary) != 4 {
				t.Fatal("the key pair API does not list the instance pair as its public projection")
			}
			if strings.Contains(read.Body.String(), "secret") {
				t.Fatal("the key pair projection carries the passphrase")
			}

			if err := w.rt.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
			wantText(t, w.call(t, "GET", gpgPublicRoute, "", ""), 200, key.Public)

			// A stored public half holding private material, as an older
			// version could have admitted it, is never exported (#1727).
			contaminated := stored
			contaminated.PublicKey = key.Public + key.Private
			if err := w.rt.Store.Keys().UpdateKeypair(t.Context(), contaminated); err != nil {
				t.Fatal(err)
			}
			privacyStatus(t, w, w.call(t, "GET", gpgPublicRoute, "reader", ""), 500, key.Private)

			// The key pair API may hold the name as another type; it is not
			// given a GPG half.
			if err := w.rt.Store.Keys().DeleteKeypair(t.Context(), accessapi.DefaultGPGKey); err != nil {
				t.Fatal(err)
			}
			rsa := testKeys()
			privacyStatus(t, w, w.call(t, "POST", "/artifactory/api/security/keypair", "admin",
				privacyBody(t, accessapi.Keypair{
					Name: accessapi.DefaultGPGKey, Alias: "rsa", Type: accessapi.KeypairRSA,
					PrivateKey: rsa.private, PublicKey: rsa.public,
				})), 201, key.Private)
			privacyStatus(t, w, w.call(t, "PUT", gpgPublicRoute, "admin", key.Public), 400, key.Private)
			privacyStatus(t, w, w.passphrase(t, "admin", "secret"), 400, key.Private)
			privacyStatus(t, w, w.call(t, "GET", gpgPublicRoute, "reader", ""), 404, key.Private)
		})
	}
}

// signingRepo is one repository of the signing scenario and the metadata
// file and detached signature a client verifies.
type signingRepo struct {
	key, file, signature string
}

// TestAccInstanceGPGKeySignsMetadata: on SQLite and PostgreSQL, a local and
// a virtual Debian, RPM and Opkg repository that name no key pair serve
// unsigned metadata until the instance key is set, then metadata signed by
// it, and their public key download answers it; one naming its own pair
// keeps signing with that pair (#1654). Local metadata picks the key up at
// its next calculation; each virtual repository here is first read after
// the key is set, as virtual metadata is kept for its retrieval cache
// period. Alpine signs with RSA and never falls back to the GPG key, and a
// virtual Opkg repository's own key is its keyPair, which its feed signs
// with.
func TestAccInstanceGPGKeySignsMetadata(t *testing.T) {
	instance, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	own, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	rpm := repopath.Read(t, "internal/artifacts/ecosystem/rpm/testdata/artifactweave-alpha-1.0-1.fc42.noarch.rpm")
	ipk := repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/464xlat_13_x86_64.ipk")
	formats := []struct {
		kind            repo.PackageType
		path, body      string
		reindex         string
		file, signature string
	}{
		{
			repo.Debian, "pool/main/a/alpha/alpha_1.0_amd64.deb;deb.distribution=stable;deb.component=main;deb.architecture=amd64",
			string(deb), "/artifactory/api/deb/reindex/", "dists/stable/Release", "dists/stable/Release.gpg",
		},
		{
			repo.RPM, "artifactweave-alpha-1.0-1.fc42.noarch.rpm", string(rpm), "/artifactory/api/yum/",
			"repodata/repomd.xml", "repodata/repomd.xml.asc",
		},
		{
			repo.Opkg, "feed/464xlat_13_x86_64.ipk", string(ipk), "/artifactory/api/opkg/reindex/",
			"feed/Packages", "feed/Packages.sig",
		},
	}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newPrivacyWorld(t, backend)
			ctx := t.Context()
			q := indexQueue(t, w)
			if err := w.rt.Store.Keys().CreateKeypair(ctx, accessapi.Keypair{
				Name: "own", Alias: "own", Type: accessapi.KeypairGPG, PrivateKey: own.Private, PublicKey: own.Public,
			}); err != nil {
				t.Fatal(err)
			}
			var signed, ownSigned []signingRepo
			for _, f := range formats {
				name := strings.ToLower(string(f.kind))
				for _, c := range []repo.Config{
					{Key: name + "-local", Class: repo.Local},
					{Key: name + "-own", Class: repo.Local, PrimaryKeyPairRef: "own"},
					{Key: name + "-virtual", Class: repo.Virtual, Repositories: []string{name + "-local"}},
					{Key: name + "-virtual-own", Class: repo.Virtual, Repositories: []string{name + "-local"}, KeyPair: "own"},
				} {
					c.PackageType = f.kind
					if err := w.rt.Store.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
						t.Fatal(err)
					}
				}
				for _, local := range []string{name + "-local", name + "-own"} {
					if r := w.call(t, "PUT", "/artifactory/"+local+"/"+f.path, "admin", f.body); r.Code != http.StatusCreated {
						t.Fatalf("deploy to %s: %d %s", local, r.Code, r.Body)
					}
					reindex(t, w, f.reindex+local)
					index(t, q, f.kind, local)
				}
				// Without the instance key, a repository naming no pair
				// stays unsigned, and has no public key to download.
				if r := w.call(t, "GET", "/artifactory/"+name+"-local/"+f.signature, "admin", ""); r.Code != http.StatusNotFound {
					t.Fatalf("%s-local signed without a key: %d", name, r.Code)
				}
				if r := w.call(t, "GET", repoPublicRoute+name+"-local", "reader", ""); r.Code != http.StatusNotFound {
					t.Fatalf("%s-local has a public key without a key: %d", name, r.Code)
				}
				signed = append(signed,
					signingRepo{name + "-local", f.file, f.signature},
					signingRepo{name + "-virtual", f.file, f.signature})
				ownSigned = append(ownSigned,
					signingRepo{name + "-own", f.file, f.signature},
					signingRepo{name + "-virtual-own", f.file, f.signature})
			}

			// The private half alone signs nothing.
			wantText(t, w.call(t, "PUT", gpgPrivateRoute, "admin", instance.Private), 200, "Private key is installed")
			reindex(t, w, "/artifactory/api/deb/reindex/debian-local")
			index(t, q, repo.Debian, "debian-local")
			if r := w.call(t, "GET", "/artifactory/debian-local/dists/stable/Release.gpg", "admin", ""); r.Code != http.StatusNotFound {
				t.Fatalf("a private half alone signed: %d", r.Code)
			}
			wantText(t, w.call(t, "PUT", gpgPublicRoute, "admin", instance.Public), 200, "Public key is installed")
			for _, f := range formats {
				local := strings.ToLower(string(f.kind)) + "-local"
				reindex(t, w, f.reindex+local)
				index(t, q, f.kind, local)
			}
			for _, r := range signed {
				verifySigned(t, w, r, instance.Public)
				pub := w.call(t, "GET", repoPublicRoute+r.key, "reader", "")
				if pub.Code != http.StatusOK || pub.Body.String() != instance.Public ||
					!strings.Contains(pub.Header().Get("Content-Disposition"), "default-gpg-key.gpg.pub") {
					t.Errorf("%s public key: %d %q", r.key, pub.Code, pub.Header().Get("Content-Disposition"))
				}
			}
			for _, r := range ownSigned {
				verifySigned(t, w, r, own.Public)
				if pub := w.call(t, "GET", repoPublicRoute+r.key, "reader", ""); pub.Code != http.StatusOK || pub.Body.String() != own.Public {
					t.Errorf("%s public key: %d", r.key, pub.Code)
				}
			}
			// RPM publishes the key beside its signature.
			if r := w.call(t, "GET", "/artifactory/rpm-local/repodata/repomd.xml.key", "admin", ""); r.Body.String() != instance.Public {
				t.Errorf("rpm-local repomd.xml.key: %d", r.Code)
			}

			alpine := repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine}
			if err := w.rt.Store.Repos().Create(ctx, *repo.Defaults(&alpine)); err != nil {
				t.Fatal(err)
			}
			if r := w.call(t, "GET", repoPublicRoute+"alpine-local", "reader", ""); r.Code != http.StatusNotFound {
				t.Errorf("an Alpine repository answers the GPG instance key: %d", r.Code)
			}
		})
	}
}

// indexQueue is a work queue on w's store running the index work a reindex
// queues, as a node's worker does, with the key store the signers read.
func indexQueue(t *testing.T, w *privacyWorld) *work.Queue {
	t.Helper()
	q, err := work.New(w.rt.Store, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &item.Service{Store: w.rt.Store, Blobs: w.rt.Blobs, Work: q, Formats: ecosystem.For, Keys: w.rt.Store.Keys()}
	if err := svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	return q
}

// index runs the index work queued for the local repository key.
func index(t *testing.T, q *work.Queue, kind repo.PackageType, key string) {
	t.Helper()
	kinds := map[repo.PackageType]string{repo.Debian: debian.KindIndex, repo.RPM: rpm.KindIndex, repo.Opkg: opkg.KindIndex}
	if err := q.EnsureFresh(t.Context(), kinds[kind], key); err != nil {
		t.Fatalf("index %s: %v", key, err)
	}
}

func reindex(t *testing.T, w *privacyWorld, path string) {
	t.Helper()
	if r := w.call(t, "POST", path, "admin", ""); r.Code != http.StatusOK {
		t.Fatalf("POST %s: %d %s", path, r.Code, r.Body)
	}
}

// verifySigned fails unless r's metadata file carries a detached signature
// by the public key public.
func verifySigned(t *testing.T, w *privacyWorld, r signingRepo, public string) {
	t.Helper()
	file := w.call(t, "GET", "/artifactory/"+r.key+"/"+r.file, "admin", "")
	signature := w.call(t, "GET", "/artifactory/"+r.key+"/"+r.signature, "admin", "")
	if file.Code != http.StatusOK || signature.Code != http.StatusOK {
		t.Errorf("%s: %s %d, %s %d", r.key, r.file, file.Code, r.signature, signature.Code)
		return
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(file.Body.Bytes()), bytes.NewReader(signature.Body.Bytes()), nil,
	); err != nil {
		t.Errorf("%s: %s is not signed by the expected key: %v", r.key, r.file, err)
	}
}
