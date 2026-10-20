package terraform_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// signer is a test GPG key pair, made as tools/gpgkey makes the Terraform
// fixture's: RSA, as JFrog's gpg-signing page generates.
type signer struct {
	entity *openpgp.Entity
	public string
}

var (
	keysOnce         sync.Once
	primary, rogue   signer
	expired          signer
	errKeys          error
	signingInThePast = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
)

// keys makes the test key pairs once: the repository's, another one, and
// one that expired after it signed.
func keys(t *testing.T) {
	t.Helper()
	keysOnce.Do(func() {
		if primary, errKeys = newSigner(&packet.Config{}); errKeys != nil {
			return
		}
		if rogue, errKeys = newSigner(&packet.Config{}); errKeys != nil {
			return
		}
		expired, errKeys = newSigner(&packet.Config{
			Time: func() time.Time { return signingInThePast }, KeyLifetimeSecs: 3600,
		})
	})
	if errKeys != nil {
		t.Fatal(errKeys)
	}
}

func newSigner(cfg *packet.Config) (signer, error) {
	cfg.Algorithm, cfg.RSABits = packet.PubKeyAlgoRSA, 2048
	e, err := openpgp.NewEntity("artifactweave test", "throwaway", "test@example.invalid", cfg)
	if err != nil {
		return signer{}, err //nolint:wrapcheck // test setup
	}
	var b bytes.Buffer
	w, err := armor.Encode(&b, openpgp.PublicKeyType, nil)
	if err == nil {
		err = e.Serialize(w)
	}
	if err == nil {
		err = w.Close()
	}
	return signer{entity: e, public: b.String()}, err //nolint:wrapcheck // test setup
}

// sign is s's binary detached signature of body, as HashiCorp's release
// tooling and JFrog's instructions make the .sig.
func (s signer) sign(t *testing.T, body []byte, cfg *packet.Config) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := openpgp.DetachSign(&b, s.entity, bytes.NewReader(body), cfg); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// signed is r's archive for platform checked against r's SHA256SUMS,
// signed by s.
func signed(t *testing.T, r release, platform string, s signer) terraform.Release {
	t.Helper()
	sums := r.files[r.name("SHA256SUMS")]
	return terraform.Release{
		Repo: "tf-providers", Archive: r.name(platform + ".zip"),
		ArchiveSHA256: sha256Hex(r.files[r.name(platform+".zip")]),
		SumsName:      r.name("SHA256SUMS"), Sums: sums,
		SignatureName: r.name("SHA256SUMS.sig"), Signature: s.sign(t, sums, nil),
	}
}

// conflict checks that err is a 409 Rejection whose message holds want.
func conflict(t *testing.T, err error, want string) {
	t.Helper()
	var r *format.Rejection
	if !errors.As(err, &r) || r.Status != http.StatusConflict ||
		!strings.Contains(r.Message, want) {
		t.Fatalf("%v, want a 409 naming %q", err, want)
	}
}

// A release whose SHA256SUMS lists the archive's content and is signed by
// the repository's primary or secondary key passes, naming the line's
// SHA-256 and the signing key's long ID.
func TestCheckPasses(t *testing.T) {
	keys(t)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64", "darwin_arm64")
	rel := signed(t, r, "darwin_arm64", primary)
	for _, ks := range [][]string{
		{primary.public},
		{rogue.public, primary.public},
		{"not a key", primary.public},
	} {
		got, err := terraform.Check(rel, ks)
		if err != nil || got.Shasum != rel.ArchiveSHA256 ||
			got.KeyID != primary.entity.PrimaryKey.KeyIdString() {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if id := primary.entity.PrimaryKey.KeyIdString(); id != strings.ToUpper(id) || len(id) != 16 {
		t.Fatalf("key ID %s", id)
	}
}

// A key that has expired since it signed still verifies, as Terraform
// accepts it: the signature was good when the release was made.
func TestCheckAcceptsAnExpiredKey(t *testing.T) {
	keys(t)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	rel := signed(t, r, "linux_amd64", primary)
	rel.Signature = expired.sign(t, rel.Sums,
		&packet.Config{Time: func() time.Time { return signingInThePast.Add(time.Minute) }})
	ring := openpgp.EntityList{expired.entity}
	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(rel.Sums),
		bytes.NewReader(rel.Signature), nil); !errors.Is(err, pgperrors.ErrKeyExpired) {
		t.Fatalf("the key has not expired: %v", err)
	}
	if _, err := terraform.Check(rel, []string{expired.public}); err != nil {
		t.Fatal(err)
	}
}

// Each failed check is a 409 naming what is missing or wrong, in decision
// 0040's order: no key, an archive the SHA256SUMS does not list, an archive
// whose content is not what it lists, and a signature none of the keys
// made.
func TestCheckRefuses(t *testing.T) {
	keys(t)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	good := signed(t, r, "linux_amd64", primary)
	_, err := terraform.Check(good, nil)
	conflict(t, err, "Repository 'tf-providers' has no GPG key pair")

	unlisted := good
	unlisted.Archive = r.name("windows_amd64.zip")
	_, err = terraform.Check(unlisted, []string{primary.public})
	conflict(t, err, "terraform-provider-null_3.3.2_SHA256SUMS lists no SHA-256 for "+
		"terraform-provider-null_3.3.2_windows_amd64.zip")

	tampered := good
	tampered.ArchiveSHA256 = sha256Hex([]byte("tampered"))
	_, err = terraform.Check(tampered, []string{primary.public})
	conflict(t, err, "but the stored archive's is "+tampered.ArchiveSHA256)

	for name, rel := range map[string]terraform.Release{
		"another key": signed(t, r, "linux_amd64", rogue),
		"other content": func() terraform.Release {
			rel := good
			rel.Signature = primary.sign(t, []byte("other"), nil)
			return rel
		}(),
		"no signature": func() terraform.Release {
			rel := good
			rel.Signature = []byte("garbage")
			return rel
		}(),
	} {
		_, err := terraform.Check(rel, []string{"not a key", primary.public})
		if name == "another key" {
			_, err = terraform.Check(rel, []string{primary.public})
		}
		conflict(t, err, "terraform-provider-null_3.3.2_SHA256SUMS.sig is not a signature of "+
			"terraform-provider-null_3.3.2_SHA256SUMS by a GPG key pair of repository 'tf-providers'")
	}
}

// A tampered archive, deployed over the one the release's SHA256SUMS
// lists, fails the check when the stored files are read back.
func TestTamperedArchiveFailsTheShasumCheck(t *testing.T) {
	keys(t)
	w := newWorld(t)
	c := w.repo("tf-providers", repo.TerraformProvider)
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	r.files[r.name("SHA256SUMS.sig")] = primary.sign(t, r.files[r.name("SHA256SUMS")], nil)
	for name, body := range r.files {
		w.must(w.deploy(c, r.dir+"/"+name, body))
	}
	check := func() error {
		v, _ := w.version(c, "hashicorp/null", "3.3.2")
		a := w.meta(v).Archives["linux_amd64"]
		_, err := terraform.Check(terraform.Release{
			Repo:          c.Key,
			Archive:       r.name("linux_amd64.zip"),
			ArchiveSHA256: a.SHA256,
			SumsName:      r.name("SHA256SUMS"),
			Sums:          w.stored(c, r.path("SHA256SUMS")),
			SignatureName: r.name(
				"SHA256SUMS.sig",
			),
			Signature: w.stored(c, r.path("SHA256SUMS.sig")),
		}, []string{primary.public})
		return err
	}
	w.must(check())
	w.must(w.deploy(c, r.path("linux_amd64.zip"), zipOf(t, [2]string{"x", "tampered"})))
	conflict(t, check(), "but the stored archive's is")
}

// stored is the content of the file at p in c.
func (w *world) stored(c repo.Config, p string) []byte {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, c.Key, false, p)
	w.must(err)
	rc, _, err := w.blobs.Open(w.ctx, it.Actual.SHA256)
	w.must(err)
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	w.must(err)
	return b
}

// Shasum reads a SHA256SUMS as Terraform does: the first line whose
// second field is the name, whatever spaces or line ends surround it, and
// not a line in binary mode, whose second field starts with "*".
func TestShasum(t *testing.T) {
	sums := []byte("aaa  other.zip\r\n\nbbb\tthe.zip\r\nccc  the.zip\nddd *star.zip\n")
	for name, want := range map[string]string{"the.zip": "bbb", "other.zip": "aaa"} {
		if got, ok := terraform.Shasum(sums, name); !ok || got != want {
			t.Errorf("%s: %s %v", name, got, ok)
		}
	}
	for _, name := range []string{"star.zip", "missing.zip", ""} {
		if got, ok := terraform.Shasum(sums, name); ok {
			t.Errorf("%s: %s", name, got)
		}
	}
}

// KeyID names the first key of an armored block, and refuses a block that
// is no key or holds none.
func TestKeyID(t *testing.T) {
	keys(t)
	if got, err := terraform.KeyID(primary.public); err != nil ||
		got != primary.entity.PrimaryKey.KeyIdString() {
		t.Fatalf("%s %v", got, err)
	}
	var empty bytes.Buffer
	w, err := armor.Encode(&empty, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, armored := range []string{"not a key", empty.String()} {
		if got, err := terraform.KeyID(armored); err == nil {
			t.Errorf("%q: %s", armored, got)
		}
	}
}
