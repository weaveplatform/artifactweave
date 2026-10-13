// Package auth authenticates requests and seeds the built-in principals
// (decision 0005). Passwords are Argon2id hashes in PHC string form, so the
// cost parameters travel with each hash and can be raised without breaking
// the hashes already stored.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrHashFormat reports a stored password hash that is not an Argon2id PHC
// string this package can verify.
var ErrHashFormat = errors.New("auth: unsupported password hash")

// HashParams are Argon2id's cost parameters. The zero value means the
// production parameters; the only other use is tests, which hash with tiny
// costs to stay fast. Nothing in configuration or on the command line can
// set them: verification reads the cost from each stored hash, so a cheap
// hash only ever comes from code that asked for one.
type HashParams struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// productionParams is RFC 9106's second recommended option (64 MiB, three
// passes, four lanes), for memory-constrained servers. Its cost per check is
// why verified credentials are cached (Authenticator).
func productionParams() HashParams { return HashParams{MemoryKiB: 64 << 10, Time: 3, Threads: 4} }

// defaultParams is what the zero HashParams means; the package's own tests
// lower it in TestMain.
var defaultParams = productionParams()

func (p HashParams) orDefault() HashParams {
	if p == (HashParams{}) {
		return defaultParams
	}
	return p
}

// maxMemory bounds the memory a stored hash may ask for, so a tampered row
// cannot make one login allocate gigabytes.
const maxMemory = 1 << 20 // KiB, 1 GiB

const (
	saltLen = 16
	keyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword returns the Argon2id hash of password in PHC form, with the
// production parameters.
func HashPassword(password string) ([]byte, error) {
	return HashPasswordWith(HashParams{}, password)
}

// HashPasswordWith is HashPassword with the given parameters.
func HashPasswordWith(p HashParams, password string) ([]byte, error) {
	p = p.orDefault()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	return fmt.Appendf(nil, "$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the stored hash.
func CheckPassword(stored []byte, password string) (bool, error) {
	p, salt, want, err := parseHash(string(stored))
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(s string) (HashParams, []byte, []byte, error) {
	var p HashParams
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, nil, nil, ErrHashFormat
	}
	if _, err := fmt.Sscanf(
		parts[3],
		"m=%d,t=%d,p=%d",
		&p.MemoryKiB,
		&p.Time,
		&p.Threads,
	); err != nil ||
		p.MemoryKiB == 0 || p.MemoryKiB > maxMemory || p.Time == 0 ||
		p.Threads == 0 {
		return p, nil, nil, ErrHashFormat
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return p, nil, nil, ErrHashFormat
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) != keyLen {
		return p, nil, nil, ErrHashFormat
	}
	return p, salt, key, nil
}
