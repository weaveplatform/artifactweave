package types

import "time"

// KeypairRole is how a repository uses a key pair: the primary key signs
// metadata, the secondary is published for clients rotating to it.
type KeypairRole string

// Key pair roles.
const (
	KeypairPrimary   KeypairRole = "primary"
	KeypairSecondary KeypairRole = "secondary"
)

// TrustedKey is a public GPG key trusted for verifying release bundles
// (/api/security/keys/trusted). Kid is server-assigned; the other derived
// fields are parsed from the key at upload.
type TrustedKey struct {
	Kid         string
	Alias       string
	PublicKey   string
	Fingerprint string
	IssuedOn    time.Time
	IssuedBy    string
	ValidUntil  time.Time
}
