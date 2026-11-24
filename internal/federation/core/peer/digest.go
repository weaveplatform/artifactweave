package peer

import (
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"strings"
)

// ErrDigest reports a Content-Digest that is missing, malformed or does not
// match the content.
var ErrDigest = errors.New("the content digest does not match the content")

// ContentDigest is the Content-Digest field value (RFC 9530) of content:
// its SHA-256, the algorithm every peer sends.
func ContentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	var b strings.Builder
	b.WriteString("sha-256=")
	serializeBare(&b, sum[:])
	return b.String()
}

// DigestFromSHA256 is the Content-Digest field value of content whose
// SHA-256 is already known, such as a blob addressed by it, so a streamed
// answer can be signed before its body is read.
func DigestFromSHA256(sum []byte) string {
	var b strings.Builder
	b.WriteString("sha-256=")
	serializeBare(&b, sum)
	return b.String()
}

// DigestSHA256 returns the sha-256 member of a Content-Digest field value,
// which a caller streaming the content compares with the SHA-256 it
// computes.
func DigestSHA256(value string) ([]byte, error) {
	d, err := parseDictionary(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDigest, err)
	}
	v, ok := d.get("sha-256")
	if !ok {
		return nil, fmt.Errorf("%w: no sha-256 digest", ErrDigest)
	}
	it, isItem := v.(item)
	sum, isBytes := it.value.([]byte)
	if !isItem || !isBytes || len(sum) != sha256.Size {
		return nil, fmt.Errorf("%w: sha-256 is not a SHA-256", ErrDigest)
	}
	return sum, nil
}

// CheckContentDigest checks the Content-Digest field value against content.
// It requires sha-256, which every peer sends, and also checks sha-512 when
// present; other algorithms are ignored, as RFC 9530 lets a recipient do.
func CheckContentDigest(value string, content []byte) error {
	d, err := parseDictionary(value)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDigest, err)
	}
	if _, ok := d.get("sha-256"); !ok {
		return fmt.Errorf("%w: no sha-256 digest", ErrDigest)
	}
	for _, alg := range []struct {
		name string
		h    func() hash.Hash
	}{{"sha-256", sha256.New}, {"sha-512", sha512.New}} {
		v, ok := d.get(alg.name)
		if !ok {
			continue
		}
		it, isItem := v.(item)
		want, isBytes := it.value.([]byte)
		if !isItem || !isBytes {
			return fmt.Errorf("%w: %s is not a byte sequence", ErrDigest, alg.name)
		}
		h := alg.h()
		h.Write(content)
		if subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
			return ErrDigest
		}
	}
	return nil
}
