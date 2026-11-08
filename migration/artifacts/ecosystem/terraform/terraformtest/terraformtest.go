// Package terraformtest builds Terraform registry content for tests (role 2
// of epic #526: Terraform modules and Terraform provider packages that
// artifactweave stores): GPG key pairs, the files of a Terraform provider
// package's release laid out and signed as HashiCorp publishes them, and
// module zips. The registry's and the server's tests share it, so they
// deploy the bytes the terraform CLI installs.
package terraformtest

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Key is a GPG key pair, made as tools/gpgkey makes the Terraform fixture's.
type Key struct {
	entity *openpgp.Entity
	// Public and Private are the ASCII-armored halves, as the keypair API
	// stores them.
	Public, Private string
}

// NewKey makes a key pair. It is RSA, as JFrog's gpg-signing page, which
// its Terraform page links for the provider registry's key, generates.
func NewKey() (Key, error) {
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048}
	e, err := openpgp.NewEntity("artifactweave test", "throwaway", "test@example.invalid", cfg)
	if err != nil {
		return Key{}, fmt.Errorf("terraformtest: generate: %w", err)
	}
	return Key{
		entity: e,
		Public: armored(openpgp.PublicKeyType, e.Serialize),
		Private: armored(openpgp.PrivateKeyType, func(w io.Writer) error {
			return e.SerializePrivate(w, cfg)
		}),
	}, nil
}

// armored is what serialize writes, as one ASCII-armored block. Writes to
// a buffer cannot fail, and a fresh entity always serializes.
func armored(blockType string, serialize func(io.Writer) error) string {
	var out bytes.Buffer
	w, _ := armor.Encode(&out, blockType, nil)
	_ = serialize(w)
	_ = w.Close()
	return out.String() + "\n"
}

// KeyID is the key's long ID in upper-case hex, as the registry protocol
// lists it.
func (k Key) KeyID() string { return k.entity.PrimaryKey.KeyIdString() }

// Sign is k's binary detached signature of body, as HashiCorp's release
// tooling and JFrog's instructions make a SHA256SUMS's .sig.
func (k Key) Sign(body []byte) []byte {
	var b bytes.Buffer
	// A key that made its own self-signature signs anything.
	_ = openpgp.DetachSign(&b, k.entity, bytes.NewReader(body), nil)
	return b.Bytes()
}

// Release is a version of a Terraform provider package.
type Release struct {
	// Type and Version name it, as terraform-provider-<type>_<version>.
	Type, Version string
	// Platforms are its archives' "<os>_<arch>".
	Platforms []string
	// Protocols are its manifest's protocol_versions; nil writes no
	// manifest.
	Protocols []string
}

// Name is the file name of the release's file with suffix, such as
// "SHA256SUMS" or "linux_amd64.zip".
func (r Release) Name(suffix string) string {
	return "terraform-provider-" + r.Type + "_" + r.Version + "_" + suffix
}

// Files are the release's files by name, as HashiCorp publishes them: an
// archive per platform, the SHA256SUMS listing them, its signature by k,
// and the manifest when the release has protocols.
func (r Release) Files(k Key) map[string][]byte {
	files := map[string][]byte{}
	for _, p := range r.Platforms {
		files[r.Name(p+".zip")] = ProviderZip(r.Type, r.Version, p)
	}
	if r.Protocols != nil {
		files[r.Name("manifest.json")] = []byte(`{"version":1,"metadata":{"protocol_versions":["` +
			strings.Join(r.Protocols, `","`) + `"]}}`)
	}
	var sums strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		sum := sha256.Sum256(files[name])
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	files[r.Name("SHA256SUMS")] = []byte(sums.String())
	files[r.Name("SHA256SUMS.sig")] = k.Sign([]byte(sums.String()))
	return files
}

// stamp dates every zip entry, so the same content always zips to the same
// bytes and the same h1: hash.
var stamp = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

// ProviderZip is a Terraform provider package's archive for a platform: the
// executable terraform-provider-<type>_v<version>_x5 that terraform init
// looks for, which it installs without running. Its content names the
// platform, so each platform's archive differs.
func ProviderZip(typ, version, platform string) []byte {
	return zipOf(map[string]zipEntry{
		"terraform-provider-" + typ + "_v" + version + "_x5": {
			body: "#!/bin/sh\n# " + platform + "\nexit 1\n", mode: 0o755,
		},
	})
}

// ModuleZip is a module version's zip holding files, by name.
func ModuleZip(files map[string]string) []byte {
	entries := map[string]zipEntry{}
	for name, body := range files {
		entries[name] = zipEntry{body: body, mode: 0o644}
	}
	return zipOf(entries)
}

type zipEntry struct {
	body string
	mode fs.FileMode
}

// zipOf zips entries in name order. Writes to a buffer cannot fail.
func zipOf(entries map[string]zipEntry) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp}
		h.SetMode(entries[name].mode)
		w, _ := z.CreateHeader(h)
		_, _ = io.WriteString(w, entries[name].body)
	}
	_ = z.Close()
	return b.Bytes()
}
