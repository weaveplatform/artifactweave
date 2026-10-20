// Package crypt seals secret columns at rest (decision record 0017): remote
// repository and proxy passwords, LDAP and mail credentials, webhook secrets,
// key pair private keys, OIDC secrets and the token signing key.
//
// A sealed value is
//
//	magic(4) | version(4, big endian) | key id(4) | nonce(12) | AES-256-GCM ciphertext and tag
//
// Keys are derived from the operator's master key with HKDF-SHA256, one per
// version, so rotation adds a version instead of replacing the key: values
// sealed under an older version still open until the store re-seals them
// (Reseal), and the old master key can then be retired. The key id is a
// fingerprint of the master key for that version; it turns "the server was
// started with a different master key" into ErrWrongKey rather than an
// authentication failure that looks like tampering.
//
// The purpose (which column) and row id (which record) are authenticated but
// not stored, so a sealed value copied into another row or column fails to
// open. This is the same design as hostweave's keyring, with a version number
// in place of a key name because artifactweave has one master key at a time.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Sentinel errors; callers test them with errors.Is.
var (
	// ErrWeakKey reports master key material shorter than 32 bytes.
	ErrWeakKey = errors.New("crypt: master key must be at least 32 bytes")
	// ErrNoKeys reports a keyring built from no keys.
	ErrNoKeys = errors.New("crypt: no master key")
	// ErrBadVersion reports a zero or duplicate key version.
	ErrBadVersion = errors.New("crypt: invalid key version")
	// ErrBadFormat reports input that is not a sealed value.
	ErrBadFormat = errors.New("crypt: not a sealed value")
	// ErrUnknownKey reports a value sealed under a version the ring lacks.
	ErrUnknownKey = errors.New("crypt: sealed under an unknown key version")
	// ErrWrongKey reports a value sealed under a different master key for the
	// same version: the configured master key is not the one that sealed it.
	ErrWrongKey = errors.New("crypt: sealed under a different master key")
	// ErrTampered reports failed authentication, including a value moved to
	// another purpose or row.
	ErrTampered = errors.New("crypt: authentication failed")
	// ErrBadPurpose reports an empty purpose.
	ErrBadPurpose = errors.New("crypt: purpose is required")
)

const (
	magicLen  = 4
	keyIDLen  = 4
	nonceLen  = 12
	tagLen    = 16
	headerLen = magicLen + 4 + keyIDLen
	keyLen    = 32
	// MinMasterKey is the shortest accepted master key.
	MinMasterKey = 32
	hkdfSalt     = "artifactweave/crypt/v1"
)

// magic starts every sealed value. The NUL keeps it apart from any text a
// column held before sealing was introduced.
var magic = [magicLen]byte{0x00, 'a', 'w', 's'}

// randReader supplies nonces; a variable so tests can make it fail.
var randReader io.Reader = rand.Reader

// MasterKey is one version of the operator's master key.
type MasterKey struct {
	Version  uint32
	Material []byte
}

type key struct {
	id   [keyIDLen]byte
	aead cipher.AEAD
	// root is what Derive derives from, so the master key material itself
	// is not kept.
	root []byte
}

// Keyring seals under its newest version and opens any version it holds. It
// is safe for concurrent use.
type Keyring struct {
	active uint32
	keys   map[uint32]key
}

// ParseMasterKey turns the configured master key (ARTIFACTWEAVE_MASTER_KEY)
// into key material. Base64 (standard or URL alphabet, padded or not) that
// decodes to at least 32 bytes is decoded, which is what `openssl rand
// -base64 32` produces; anything else is used as raw bytes and must itself be
// at least 32 bytes.
func ParseMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) >= MinMasterKey {
			return b, nil
		}
	}
	if len(s) < MinMasterKey {
		return nil, ErrWeakKey
	}
	return []byte(s), nil
}

// ParseVersionedKey reads one configured master key, optionally prefixed
// with its version as "vN:" (N >= 1); a key without a prefix is version 1.
// Versions must be stable across restarts because sealed values name the
// version that sealed them, so rotation adds the next version explicitly:
// the old key moves to master_key_previous unchanged and the new one is
// configured as "v2:<key>", then "v3:<key>" at the next rotation. Base64
// never contains ':', so the prefix cannot be mistaken for key material.
func ParseVersionedKey(s string) (MasterKey, error) {
	s = strings.TrimSpace(s)
	version := uint64(1)
	if rest, ok := strings.CutPrefix(s, "v"); ok {
		if num, key, ok := strings.Cut(rest, ":"); ok {
			n, err := strconv.ParseUint(num, 10, 32)
			if err != nil || n == 0 {
				return MasterKey{}, fmt.Errorf("%w: %q", ErrBadVersion, "v"+num)
			}
			version, s = n, key
		}
	}
	material, err := ParseMasterKey(s)
	if err != nil {
		return MasterKey{}, err
	}
	return MasterKey{Version: uint32(version), Material: material}, nil
}

// New returns a keyring holding a single version-1 master key.
func New(material []byte) (*Keyring, error) {
	return NewKeyring(MasterKey{Version: 1, Material: material})
}

// NewKeyring returns a keyring holding every given version. The highest
// version seals; all of them open.
func NewKeyring(keys ...MasterKey) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	k := &Keyring{keys: make(map[uint32]key, len(keys))}
	for _, mk := range keys {
		if mk.Version == 0 {
			return nil, fmt.Errorf("%w: versions start at 1", ErrBadVersion)
		}
		if _, dup := k.keys[mk.Version]; dup {
			return nil, fmt.Errorf("%w: version %d given twice", ErrBadVersion, mk.Version)
		}
		derived, err := derive(mk)
		if err != nil {
			return nil, err
		}
		k.keys[mk.Version] = derived
		k.active = max(k.active, mk.Version)
	}
	return k, nil
}

func derive(mk MasterKey) (key, error) {
	if len(mk.Material) < MinMasterKey {
		return key{}, fmt.Errorf("%w: version %d", ErrWeakKey, mk.Version)
	}
	v := strconv.FormatUint(uint64(mk.Version), 10)
	// HKDF with fixed inputs and lengths within its limits cannot fail.
	enc, _ := hkdf.Key(sha256.New, mk.Material, []byte(hkdfSalt), "sealed-columns/v"+v, keyLen)
	id, _ := hkdf.Key(sha256.New, mk.Material, []byte(hkdfSalt), "key-id/v"+v, keyIDLen)
	root, _ := hkdf.Key(sha256.New, mk.Material, []byte(hkdfSalt), "derived/v"+v, keyLen)
	// A 32-byte key always yields an AES block and a standard GCM.
	block, _ := aes.NewCipher(enc)
	aead, _ := cipher.NewGCM(block)
	var k key
	copy(k.id[:], id)
	k.aead = aead
	k.root = root
	return k, nil
}

// Active is the version Seal uses.
func (k *Keyring) Active() uint32 { return k.active }

// Derive returns a 32-byte key for purpose from the active version of the
// master key, for a secret that every node must hold without storing it,
// such as the key that signs Terraform's download URLs (decision 0040).
// Each purpose gets an unrelated key, and a rotation new ones: a value
// signed under the old version stops verifying, so only short-lived values
// may use it.
func (k *Keyring) Derive(purpose string) []byte {
	// HKDF with fixed lengths within its limits cannot fail.
	out, _ := hkdf.Key(
		sha256.New,
		k.keys[k.active].root,
		[]byte(hkdfSalt),
		"purpose/"+purpose,
		keyLen,
	)
	return out
}

// Versions lists the versions the keyring opens, oldest first.
func (k *Keyring) Versions() []uint32 {
	out := make([]uint32, 0, len(k.keys))
	for v := range k.keys {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// Rotate returns a keyring that seals under newMaster as the next version and
// still opens every version this one holds. The receiver is unchanged.
func (k *Keyring) Rotate(newMaster []byte) (*Keyring, error) {
	next := MasterKey{Version: k.active + 1, Material: newMaster}
	derived, err := derive(next)
	if err != nil {
		return nil, err
	}
	out := &Keyring{active: next.Version, keys: make(map[uint32]key, len(k.keys)+1)}
	for v, kk := range k.keys {
		out.keys[v] = kk
	}
	out.keys[next.Version] = derived
	return out, nil
}

// aad binds the header, the purpose and the row. The purpose is length
// prefixed so ("a", "bc") and ("ab", "c") authenticate differently.
func aad(header []byte, purpose, rowID string) []byte {
	out := make([]byte, 0, len(header)+4+len(purpose)+len(rowID))
	out = append(out, header...)
	out = binary.BigEndian.AppendUint32(
		out,
		uint32(len(purpose)),
	) // #nosec G115 -- purposes are short constants
	out = append(out, purpose...)
	return append(out, rowID...)
}

// Seal encrypts plaintext for one column (purpose) of one record (rowID).
func (k *Keyring) Seal(purpose, rowID string, plaintext []byte) ([]byte, error) {
	if purpose == "" {
		return nil, ErrBadPurpose
	}
	kk := k.keys[k.active]
	out := make([]byte, 0, headerLen+nonceLen+len(plaintext)+tagLen)
	out = append(out, magic[:]...)
	out = binary.BigEndian.AppendUint32(out, k.active)
	out = append(out, kk.id[:]...)
	header := out[:headerLen]
	nonce := out[headerLen : headerLen+nonceLen]
	if _, err := io.ReadFull(randReader, nonce); err != nil {
		return nil, fmt.Errorf("crypt: nonce: %w", err)
	}
	out = out[:headerLen+nonceLen]
	return kk.aead.Seal(out, nonce, plaintext, aad(header, purpose, rowID)), nil
}

// Open decrypts a value sealed by Seal with the same purpose and rowID.
func (k *Keyring) Open(purpose, rowID string, sealed []byte) ([]byte, error) {
	if purpose == "" {
		return nil, ErrBadPurpose
	}
	version, err := Version(sealed)
	if err != nil {
		return nil, err
	}
	kk, ok := k.keys[version]
	if !ok {
		return nil, fmt.Errorf(
			"%w: version %d (the keyring holds %v)",
			ErrUnknownKey,
			version,
			k.Versions(),
		)
	}
	header := sealed[:headerLen]
	if subtle.ConstantTimeCompare(header[magicLen+4:], kk.id[:]) != 1 {
		return nil, fmt.Errorf("%w: version %d", ErrWrongKey, version)
	}
	nonce := sealed[headerLen : headerLen+nonceLen]
	plain, err := kk.aead.Open(nil, nonce, sealed[headerLen+nonceLen:], aad(header, purpose, rowID))
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}

// Reseal re-encrypts a value under the active version if it was sealed under
// an older one, and reports whether it changed. Stores run it over every
// sealed column after Rotate; once nothing changes, the old master key can be
// removed from the configuration.
func (k *Keyring) Reseal(purpose, rowID string, sealed []byte) ([]byte, bool, error) {
	version, err := Version(sealed)
	if err != nil {
		return nil, false, err
	}
	if version == k.active {
		if _, err := k.Open(purpose, rowID, sealed); err != nil {
			return nil, false, err
		}
		return sealed, false, nil
	}
	plain, err := k.Open(purpose, rowID, sealed)
	if err != nil {
		return nil, false, err
	}
	out, err := k.Seal(purpose, rowID, plain)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// IsSealed reports whether b has the sealed-value header.
func IsSealed(b []byte) bool {
	return len(b) >= headerLen+nonceLen+tagLen && [magicLen]byte(b[:magicLen]) == magic
}

// Version returns the key version a sealed value names.
func Version(sealed []byte) (uint32, error) {
	if !IsSealed(sealed) {
		return 0, ErrBadFormat
	}
	return binary.BigEndian.Uint32(sealed[magicLen : magicLen+4]), nil
}

// Masked is what reads return in place of a stored secret. Research 03 and 15
// record that Artifactory returns secrets masked rather than in clear; the
// exact string Artifactory uses is unverified, so this one is artifactweave's.
const Masked = "********"

// Mask returns Masked for a set secret and "" for an unset one, so a client
// can still tell whether a password is configured.
func Mask(secret string) string {
	if secret == "" {
		return ""
	}
	return Masked
}

// KeepIfMasked resolves an incoming secret on update. Clients such as the
// Terraform providers and `jf` read a configuration, change a field and send
// the whole document back; the secret then arrives as Masked and must not
// overwrite the stored value.
func KeepIfMasked(incoming, stored string) string {
	if incoming == Masked {
		return stored
	}
	return incoming
}
