package terraform

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"

	"github.com/weaveplatform/artifactweave/internal/format"
)

// Release is a Terraform provider package's archive with the files a
// registry download is checked against (decision 0040): the stored
// archive, its version's SHA256SUMS and the SHA256SUMS's detached
// signature. Signatures are checked when an archive is downloaded through
// the registry, never on deploy, since the files arrive by separate PUTs in
// any order and the key pair may be replaced later.
type Release struct {
	// Repo is the repository's key, which the refusals name.
	Repo string
	// Archive is the archive's file name and ArchiveSHA256 its stored
	// content's SHA-256.
	Archive, ArchiveSHA256 string
	// SumsName and Sums are the SHA256SUMS file's name and content.
	SumsName string
	Sums     []byte
	// SignatureName and Signature are the signature's file name and
	// content.
	SignatureName string
	Signature     []byte
}

// Verified is what a release that passes Check proves.
type Verified struct {
	// Shasum is the SHA-256 the SHA256SUMS lists for the archive, the
	// registry download answer's shasum.
	Shasum string
	// KeyID is the long ID, in upper-case hex, of the key that signed the
	// SHA256SUMS.
	KeyID string
}

// Check makes decision 0040's checks of a registry download of r's
// archive, in its order, against keys, the ASCII-armored public keys of the
// repository's GPG key pairs, primary first:
//  1. there is a key;
//  2. the SHA256SUMS lists the archive's file name with the stored
//     archive's SHA-256, read as Terraform reads it, from the first line
//     whose second field is the file name;
//  3. the signature verifies over the SHA256SUMS with one of the keys.
//
// A failed check is a 409 RejectionError naming what is missing or wrong. A key
// that cannot be read verifies nothing; one that has expired since it
// signed still does, as Terraform accepts it.
func Check(r Release, keys []string) (Verified, error) {
	if len(keys) == 0 {
		return Verified{}, format.Rejectf(
			http.StatusConflict,
			"Repository '%s' has no GPG key pair to verify Terraform provider packages with",
			r.Repo,
		)
	}
	sum, ok := Shasum(r.Sums, r.Archive)
	switch {
	case !ok:
		return Verified{}, format.Rejectf(http.StatusConflict,
			"%s lists no SHA-256 for %s", r.SumsName, r.Archive)
	case !strings.EqualFold(sum, r.ArchiveSHA256):
		return Verified{}, format.Rejectf(http.StatusConflict,
			"%s lists SHA-256 %s for %s, but the stored archive's is %s",
			r.SumsName, sum, r.Archive, r.ArchiveSHA256)
	}
	for _, armored := range keys {
		ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
		if err != nil {
			continue
		}
		signer, err := openpgp.CheckDetachedSignature(ring,
			bytes.NewReader(r.Sums), bytes.NewReader(r.Signature), nil)
		if signer != nil && (err == nil || errors.Is(err, pgperrors.ErrKeyExpired)) {
			return Verified{Shasum: sum, KeyID: signer.PrimaryKey.KeyIdString()}, nil
		}
	}
	return Verified{}, format.Rejectf(http.StatusConflict,
		"%s is not a signature of %s by a GPG key pair of repository '%s'",
		r.SignatureName, r.SumsName, r.Repo)
}

// Shasum returns the SHA-256 that a SHA256SUMS document lists for the file
// name, as Terraform reads the document: the first field of the first line
// whose second field is the name. ok is false when no line names it.
func Shasum(sums []byte, name string) (string, bool) {
	for line := range bytes.SplitSeq(sums, []byte("\n")) {
		if f := bytes.Fields(line); len(f) > 1 && string(f[1]) == name {
			return string(f[0]), true
		}
	}
	return "", false
}

// KeyID is the long ID, in upper-case hex, of the first key in an
// ASCII-armored public key block: the key_id the registry protocol lists
// beside the key.
func KeyID(armored string) (string, error) {
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		return "", fmt.Errorf("terraform: read the GPG public key: %w", err)
	}
	if len(ring) == 0 {
		return "", errNoKey
	}
	return ring[0].PrimaryKey.KeyIdString(), nil
}

// errNoKey is an armored block that holds no key.
var errNoKey = errors.New("terraform: the GPG public key block holds no key")
