package terraformbackend

import (
	"context"
	"crypto/md5" //nolint:gosec // the MD5 Terraform sends and compares, not a security primitive
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format"
)

// Errors of reading a state.
var (
	// ErrNotState is content that is not a Terraform state: not a JSON
	// object, or one without a whole, non-negative serial.
	ErrNotState = errors.New("terraformbackend: not a Terraform state")
	// ErrNoKeyring is a sealed state, or a state to seal, on a server
	// without a master key.
	ErrNoKeyring = errors.New(
		"terraformbackend: the state is sealed and no master key is configured",
	)
)

// Meta is what the package index keeps of a state version (decision 0041,
// "The package index"), so the API answers without reading the content:
// the state's own serial and lineage, the MD5 and size of the plain state,
// and the stored content it was read from.
type Meta struct {
	Serial  int64  `json:"serial"`
	Lineage string `json:"lineage,omitempty"`
	// MD5 is the hex MD5 of the plain state, which Terraform compares with
	// the one it computes.
	MD5 string `json:"md5,omitempty"`
	// Size is the plain state's length; a sealed item is longer.
	Size int64 `json:"size"`
	// Stored is the SHA-256 of the item's content the rest was read from.
	// A hook that finds the item's content unchanged reuses the rest
	// rather than unsealing it again.
	Stored string `json:"stored"`
	// Sealed marks content stored sealed under the master key.
	Sealed bool `json:"sealed,omitempty"`
	// Unreadable marks content that does not open under this server's
	// master key or is no state. It is indexed all the same, so a trash
	// restore or a reindex never fails on it: the API refuses it when it
	// is read.
	Unreadable bool `json:"unreadable,omitempty"`
}

// Encode is m as the package index stores it.
func (m Meta) Encode() []byte {
	// A struct of strings, numbers and booleans always encodes.
	b, _ := json.Marshal(m)
	return b
}

// DecodeMeta reads what Encode wrote; ok is false for anything else.
func DecodeMeta(b []byte) (Meta, bool) {
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil || m.Stored == "" {
		return Meta{}, false
	}
	return m, true
}

// Parse reads a state's serial and lineage. Terraform writes both at the
// top level of every state from format version 3 on.
func Parse(plain []byte) (serial int64, lineage string, err error) {
	var s struct {
		Serial  json.Number `json:"serial"`
		Lineage string      `json:"lineage"`
	}
	if err := json.Unmarshal(plain, &s); err != nil {
		return 0, "", fmt.Errorf("%w: %w", ErrNotState, err)
	}
	serial, err = strconv.ParseInt(s.Serial.String(), 10, 64)
	if err != nil || serial < 0 {
		return 0, "", fmt.Errorf("%w: no serial", ErrNotState)
	}
	return serial, s.Lineage, nil
}

// MD5 is the hex MD5 of b.
func MD5(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // as above
	return hex.EncodeToString(sum[:])
}

// SHA256 is the hex SHA-256 of b, the key the blob store files content by.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Seal seals a plain state of workspace ws in repository key.
func Seal(keys *crypt.Keyring, key, ws string, plain []byte) ([]byte, error) {
	if keys == nil {
		return nil, ErrNoKeyring
	}
	out, err := keys.Seal(Purpose, Row(key, ws), plain)
	if err != nil {
		return nil, fmt.Errorf("terraformbackend: seal: %w", err)
	}
	return out, nil
}

// Open returns the plain state stored as stored in workspace ws of
// repository key: stored itself unless it is sealed, which no state JSON
// can be mistaken for, as the envelope starts with a NUL. So a repository
// whose encryptStates changed reads every state in the form it was written
// in (decision 0041).
func Open(keys *crypt.Keyring, key, ws string, stored []byte) ([]byte, error) {
	if !crypt.IsSealed(stored) {
		return stored, nil
	}
	if keys == nil {
		return nil, ErrNoKeyring
	}
	plain, err := keys.Open(Purpose, Row(key, ws), stored)
	if err != nil {
		return nil, fmt.Errorf("terraformbackend: open the state of %s: %w", Row(key, ws), err)
	}
	return plain, nil
}

// MetaOf is the Meta of a state of workspace ws in repository key stored
// as stored. Content that does not open or is no state is Unreadable, with
// the stored size.
func MetaOf(keys *crypt.Keyring, key, ws string, stored []byte) Meta {
	plain, err := Open(keys, key, ws, stored)
	if err == nil {
		var m Meta
		if m, err = PlainMeta(plain, stored); err == nil {
			return m
		}
	}
	return Meta{
		Stored: SHA256(stored), Sealed: crypt.IsSealed(stored), Size: int64(len(stored)),
		Unreadable: true,
	}
}

// PlainMeta is the Meta of plain, a state stored as stored: plain itself,
// or sealed.
func PlainMeta(plain, stored []byte) (Meta, error) {
	serial, lineage, err := Parse(plain)
	if err != nil {
		return Meta{}, err
	}
	return Meta{
		Serial: serial, Lineage: lineage, MD5: MD5(plain), Size: int64(len(plain)),
		Stored: SHA256(stored), Sealed: crypt.IsSealed(stored),
	}, nil
}

// ReadStored reads the content stored under sha256 whole: a state is sealed
// and opened as one message.
func ReadStored(ctx context.Context, blobs format.Blobs, sha256 string) ([]byte, error) {
	r, _, err := blobs.Open(ctx, sha256)
	if err != nil {
		return nil, fmt.Errorf("terraformbackend: open content %s: %w", sha256, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("terraformbackend: read content %s: %w", sha256, err)
	}
	return b, nil
}
