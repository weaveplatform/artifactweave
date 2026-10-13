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

// hashParams are Argon2id's cost parameters.
type hashParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
}

// defaultParams is RFC 9106's second recommended option (64 MiB, three
// passes, four lanes), for memory-constrained servers. Its cost per check is
// why verified credentials are cached (Authenticator).
var defaultParams = hashParams{memory: 64 << 10, time: 3, threads: 4}

// maxMemory bounds the memory a stored hash may ask for, so a tampered row
// cannot make one login allocate gigabytes.
const maxMemory = 1 << 20 // KiB, 1 GiB

const (
	saltLen = 16
	keyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword returns the Argon2id hash of password in PHC form.
func HashPassword(password string) ([]byte, error) {
	return hashWith(defaultParams, password)
}

func hashWith(p hashParams, password string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, keyLen)
	return fmt.Appendf(nil, "$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the stored hash.
func CheckPassword(stored []byte, password string) (bool, error) {
	p, salt, want, err := parseHash(string(stored))
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(s string) (hashParams, []byte, []byte, error) {
	var p hashParams
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, nil, nil, ErrHashFormat
	}
	if _, err := fmt.Sscanf(
		parts[3],
		"m=%d,t=%d,p=%d",
		&p.memory,
		&p.time,
		&p.threads,
	); err != nil ||
		p.memory == 0 || p.memory > maxMemory || p.time == 0 ||
		p.threads == 0 {
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
