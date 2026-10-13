package types

import "time"

// KeypairType is the kind of a stored key pair.
type KeypairType string

// Key pair types the security API accepts.
const (
	KeypairRSA KeypairType = "RSA"
	KeypairGPG KeypairType = "GPG"
)

// KeypairRole is how a repository uses a key pair: the primary key signs
// metadata, the secondary is published for clients rotating to it.
type KeypairRole string

// Key pair roles.
const (
	KeypairPrimary   KeypairRole = "primary"
	KeypairSecondary KeypairRole = "secondary"
)

// Keypair is a named signing key pair (the security API's pairName). The
// private key and passphrase are sealed at rest (decision 0017).
type Keypair struct {
	Name       string
	Type       KeypairType
	Alias      string
	PublicKey  string
	PrivateKey string `json:"-"`
	Passphrase string `json:"-"`
}

// Certificate is a client certificate that remote repositories present to
// upstreams (clientTlsCertificate). PEM holds the certificate chain and
// PrivateKey its key, sealed at rest (decision 0017). Every other field is
// parsed from the PEM once at upload, so listings never re-parse.
type Certificate struct {
	Alias       string
	PEM         string
	PrivateKey  string `json:"-"`
	IssuedTo    string
	IssuedBy    string
	IssuedOn    time.Time
	ValidUntil  time.Time
	Fingerprint string
}

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
