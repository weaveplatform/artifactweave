package model

import "time"

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
